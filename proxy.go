package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// proxyState 把「scanner 状态变更」与「写 ptmx」放进同一把锁的同一临界区——
// 事件产出即写入，字节顺序绝不重排（审计 B1 的根治）。
type proxyState struct {
	ptmx     *os.File
	up       *Uploader
	stateDir string

	mu       sync.Mutex
	sc       Scanner
	lastFeed time.Time
	stop     chan struct{}

	shutdown     chan struct{} // 收尾已启动（终端没了/收到外部信号）
	shutdownOnce sync.Once

	drops chan drop     // 拖拽处理队列：单 worker 串行消费 → 注入顺序 = 拖拽顺序
	kick  chan struct{} // scanner 有滞留状态时唤醒 idle 巡逻（平时定时器熟睡，空载 0% CPU）
}

// drop 是一次已通过语法门的拖拽候选（或会话内 Ctrl+V 落地的剪贴板图片）。
type drop struct {
	payload   string
	tokens    []string
	bracketed bool
	noReplay  bool   // 失败时不回放 payload——剪贴板贴图无本地路径可放行，只通知
	cleanup   string // 非空：处理完毕删除该临时目录（剪贴板图片的落地处）
	source    string // 事件日志来源标注；空=拖拽
}

// Ctrl+V 贴图（config: paste_key）：孤立 0x16 触发本地剪贴板探测。
// pasteProbeDeadline 是探测硬时限——超时即承诺拦截（无图报错远快于此值）。均可在测试中调整。
const ctrlV = 0x16

var (
	pasteKeyEnabled    = true
	pasteProbeDeadline = 200 * time.Millisecond
)

func (p *proxyState) beginShutdown() {
	p.shutdownOnce.Do(func() { close(p.shutdown) })
}

// RunProxy 在 pty 上运行 cmd，本端 stdio 与之互接；返回子进程退出码。
func RunProxy(cmd *exec.Cmd, up *Uploader) (int, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 1, err
	}
	defer ptmx.Close()

	// 终端尺寸：启动时 + SIGWINCH；尺寸不可得时兜底 80x24（0x0 会让 mosh 崩溃）
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	resize := func() {
		if ws, err := pty.GetsizeFull(os.Stdin); err == nil && ws.Cols > 0 && ws.Rows > 0 {
			_ = pty.Setsize(ptmx, ws)
		} else {
			_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80})
		}
	}
	resize()
	go func() {
		for range winch {
			resize()
		}
	}()
	defer signal.Stop(winch)

	p := &proxyState{ptmx: ptmx, up: up, stateDir: stateDirPath(), lastFeed: time.Now(),
		stop: make(chan struct{}), shutdown: make(chan struct{}),
		drops: make(chan drop, 64), kick: make(chan struct{}, 1)}
	go p.dropWorker()

	// 外部信号（kill/终端关闭）：转发给子进程并启动限时收尾；终端态由下方 defer 恢复
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		for s := range sigs {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(s)
			}
			p.beginShutdown()
		}
	}()
	defer signal.Stop(sigs)

	// 本地终端进 raw（仅当 stdin 是 tty；测试管道自动跳过）
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer term.Restore(fd, old)
		}
	}

	// 远端→屏幕：持续排空 pty。stdout 死了也要继续读并丢弃——
	// mosh 退出时会等待终端输出被取走(tcdrain)，无人排空它会卡死在
	// 内核退出路径（连 SIGKILL 都免疫），进而把我们拖成僵尸。
	copyDone := make(chan struct{})
	go func() {
		buf := make([]byte, 32*1024)
		stdoutOK := true
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 && stdoutOK {
				if _, werr := os.Stdout.Write(buf[:n]); werr != nil {
					stdoutOK = false
				}
			}
			if rerr != nil {
				close(copyDone)
				return
			}
		}
	}()

	go p.idleLoop()
	go p.stdinLoop(cmd)

	// Wait 放到 goroutine：正常情况无限等（会话可活数天），
	// 一旦收尾启动则限时——5s 不死补 SIGKILL，再 3s 放弃尸体自行退出。
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err = <-waitDone:
	case <-p.shutdown:
		select {
		case err = <-waitDone:
		case <-time.After(5 * time.Second):
			trace("子进程 5s 未退出, SIGKILL")
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			select {
			case err = <-waitDone:
			case <-time.After(3 * time.Second):
				trace("子进程卡死在内核退出路径, 放弃等待")
				err = nil
			}
		}
	}
	trace("cmd.Wait 阶段结束: %v", err)
	close(p.stop)
	// 排空最后一屏输出再返回
	select {
	case <-copyDone:
	case <-time.After(300 * time.Millisecond):
	}
	trace("输出已排空, 准备返回")
	if ee, ok := err.(*exec.ExitError); ok {
		code := ee.ExitCode()
		if code < 0 {
			code = 1 // 被信号终止：以通用错误码退出
		}
		return code, nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// write 仅可在持有 p.mu 时调用。
func (p *proxyState) write(b []byte) { _, _ = p.ptmx.Write(b) }

func (p *proxyState) writePasteLocked(payload string, bracketed bool) {
	if bracketed {
		p.write([]byte(bpStart + payload + bpEnd))
	} else {
		p.write([]byte(payload))
	}
}

func (p *proxyState) replay(payload string, bracketed bool) {
	p.mu.Lock()
	p.writePasteLocked(payload, bracketed)
	p.mu.Unlock()
}

// injectWhenClean 等输入流到达干净点再注入：不在粘贴中、无半截转义序列滞留、
// 也不处于"溢出/中止后未闭合的裸粘贴流"窗口（否则注入的 bpEnd 会提前终结远端粘贴态）。
// 最多等 1s 兜底强注。
func (p *proxyState) injectWhenClean(payload string, bracketed bool) {
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		clean := !p.sc.InPaste() && !p.sc.HasPending() && !p.sc.RawPasteOpen()
		if clean || time.Now().After(deadline) {
			p.writePasteLocked(payload, bracketed)
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
}

// idleLoop 事件驱动：只有 scanner 真的扣押着东西（半截标记/未闭合粘贴）才开
// 25ms 巡逻，状态干净立即回去熟睡——空载 CPU ≈ 0%。
func (p *proxyState) idleLoop() {
	for {
		select {
		case <-p.stop:
			return
		case <-p.kick:
		}
		tick := time.NewTicker(25 * time.Millisecond)
		for dirty := true; dirty; {
			select {
			case <-p.stop:
				tick.Stop()
				return
			case <-tick.C:
				p.mu.Lock()
				evs := p.sc.Idle(time.Since(p.lastFeed))
				for _, ev := range evs {
					p.write(ev.Data)
				}
				dirty = p.sc.HasPending() || p.sc.InPaste()
				p.mu.Unlock()
			}
		}
		tick.Stop()
	}
}

func (p *proxyState) stdinLoop(cmd *exec.Cmd) {
	buf := make([]byte, 32*1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			// Ctrl+V 贴图：整块全为 0x16（单按或连按在管道里合并）且流处于干净点
			// 才进门。探测期间本循环不再读 stdin，后续按键滞留在管道里——保序是
			// 结构性的，无需扣押缓冲。连按按一次处理：有图时一个 0x16 都不能漏给
			// 远端（远端 CC 收到会读远端自己的旧剪贴板，安静吃错图）。
			if pasteKeyEnabled && !p.up.Disabled() && isCtrlVBurst(buf[:n]) {
				p.mu.Lock()
				clean := !p.sc.InPaste() && !p.sc.HasPending() && !p.sc.RawPasteOpen()
				p.mu.Unlock()
				if clean && p.interceptPasteKey() {
					continue
				}
			}
			chunk := append([]byte{}, buf[:n]...)
			var enq []drop
			p.mu.Lock()
			p.lastFeed = time.Now()
			// 非括号后备：整块裸路径。必须无滞留 pending（否则被扣押的
			// 用户按键会被排到载荷之后——审计 R2），且语法门须命中；
			// 不命中则走正常 Feed 逐字节保序透传。
			if !p.sc.InPaste() && !p.sc.HasPending() && looksLikeBarePathChunk(chunk) {
				if tokens, ok := p.syntaxGate(string(chunk)); ok {
					enq = append(enq, drop{payload: string(chunk), tokens: tokens})
					p.mu.Unlock()
					p.enqueue(enq)
					continue
				}
			}
			evs := p.sc.Feed(chunk)
			for _, ev := range evs {
				if ev.Type == EvPaste {
					payload := string(ev.Data)
					// 同步判定在锁内按事件序完成（审计 R1）：
					// 非拖拽粘贴当场回放，与后续 EvForward 保序；
					// 只有真拖拽候选才延后到队列异步处理。
					if tokens, ok := p.syntaxGate(payload); ok {
						enq = append(enq, drop{payload: payload, tokens: tokens, bracketed: true})
					} else {
						p.writePasteLocked(payload, true)
					}
				} else {
					p.write(ev.Data)
				}
			}
			dirty := p.sc.HasPending() || p.sc.InPaste()
			p.mu.Unlock()
			if dirty { // 有滞留：唤醒 idle 巡逻
				select {
				case p.kick <- struct{}{}:
				default:
				}
			}
			p.enqueue(enq)
		}
		if err != nil {
			trace("stdin EOF/err: %v → SIGHUP 子进程并启动限时收尾", err)
			// 终端没了：按真实终端关闭的语义给子进程发 HUP。
			// 绝不注入 ^D 之类字节——那会误杀远端 tmux 里的前台进程。
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGHUP)
			}
			p.beginShutdown()
			return
		}
	}
}

// syntaxGate：零 IO 的同步判定——可安全在锁内调用。
// 返回 ok=false 表示"不是拖拽，按普通字节保序处理"。
func (p *proxyState) syntaxGate(payload string) ([]string, bool) {
	if p.up.Disabled() {
		return nil, false
	}
	return parsePasteSyntax(payload)
}

// enqueue 把拖拽候选按事件序推入队列（stdin 单 goroutine 保证全局有序）。
// 队列满（64 个在途拖拽，几乎不可能）则降级为当场回放，绝不阻塞输入。
func (p *proxyState) enqueue(ds []drop) {
	for _, d := range ds {
		select {
		case p.drops <- d:
		default:
			p.replay(d.payload, d.bracketed)
		}
	}
}

// interceptPasteKey：Ctrl+V（单按或连按合并块）到达且流处于干净点时调用
// （stdinLoop goroutine 上同步执行，整块只触发一次）。
// 探测本地剪贴板（硬时限 pasteProbeDeadline）：
//   - 有图 → 吞掉按键，图片落临时文件走拖拽同款上传管线，返回 true；
//   - 无图（快速报错）→ 返回 false，调用方按普通字节放行；
//   - 超时 → 判「有图但慢」（无图报错远快于时限），吞掉按键、后台等结果，
//     最终无图只通知——绝不迟发 0x16：远端 CC 收到它会去读远端自己的剪贴板，
//     安静吃进一张旧图，比「贴不上」更危险。
func (p *proxyState) interceptPasteKey() bool {
	type probe struct {
		path string
		err  error
	}
	ch := make(chan probe, 1)
	go func() {
		path, err := clipboardToTempPNG()
		ch <- probe{path, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return false
		}
		p.enqueueClipboard(r.path)
		return true
	case <-time.After(pasteProbeDeadline):
		go func() {
			if r := <-ch; r.err == nil {
				p.enqueueClipboard(r.path)
			} else {
				Notify("moshdrop", msg("n.clipslow"))
			}
		}()
		return true
	}
}

// isCtrlVBurst：整块全为 0x16。键盘自动重复/快速连按会在探测窗口内于管道中合并，
// 以 n>1 到达——必须整块视作一次触发，而不是漏成普通字节。
func isCtrlVBurst(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c != ctrlV {
			return false
		}
	}
	return true
}

// enqueueClipboard 把剪贴板落地的 PNG 作为候选入队（与拖拽同管线）。
// 快速探测命中时由 stdinLoop 同步调用，与拖拽全局保序；探测超时的后台路径迟到入队——
// 期间发起的拖拽会排在它前面（窗口有界：剪贴板读取有 5s 硬超时）。
func (p *proxyState) enqueueClipboard(path string) {
	d := drop{tokens: []string{path}, bracketed: true, noReplay: true,
		cleanup: filepath.Dir(path), source: "clipboard"}
	select {
	case p.drops <- d:
	default: // 64 个在途才可能：放弃本次贴图，清理并告知
		_ = os.RemoveAll(filepath.Dir(path))
		Notify("moshdrop", fmt.Sprintf(msg("n.clipfailed"), msg("r.queuefull")))
	}
}

// slowNotifyDelay：上传超过此时长仍未完成才弹「上传中」——按时间触发（与文件大小无关），
// 治「弱网上传数十秒毫无反应 → 用户走神切窗格」的根因。可在测试中调小。
var slowNotifyDelay = 1500 * time.Millisecond

// dropWorker 串行消费拖拽队列：注入顺序 = 入队顺序（uploader 的承诺在此兑现；
// 唯一例外见 enqueueClipboard 的超时后台路径）。
func (p *proxyState) dropWorker() {
	for d := range p.drops {
		p.processDrop(d)
	}
}

// processDrop 处理一次候选。验真失败（非文件/网络卷挂死）原样回放；
// 上传失败 fail-open 回放 + 通知 + 留痕。剪贴板贴图（noReplay）无可回放物：只通知。
func (p *proxyState) processDrop(d drop) {
	if d.cleanup != "" {
		defer os.RemoveAll(d.cleanup)
	}
	start := time.Now()
	total, ok := verifyLocalFiles(d.tokens, 500*time.Millisecond)
	if !ok {
		if d.noReplay { // 刚落盘的临时文件消失了：不可静默吞掉用户按键
			Notify("moshdrop", fmt.Sprintf(msg("n.clipfailed"), msg("r.tmpgone")))
		}
		p.failOpen(d)
		return
	}
	// 大文件护栏：超过上限不自动上传（弱网传大文件会拖很久、占满带宽）。
	// 拖拽：原样放行本地路径 + 建议手动 scp；剪贴板贴图：临时文件随即删除、
	// 用户从未见过其路径，scp 文案不适用，弹专用文案。0=不限制。
	if limit := p.up.maxInterceptBytes; limit > 0 && total > limit {
		if d.noReplay {
			Notify("moshdrop", fmt.Sprintf(msg("n.cliptoobig"), float64(total)/(1<<20), limit>>20))
		} else {
			Notify("moshdrop", fmt.Sprintf(msg("n.toobig"), float64(total)/(1<<20), limit>>20))
		}
		p.failOpen(d)
		return
	}
	// 迟到反馈：上传超过 slowNotifyDelay 仍未完成才弹「上传中」，治弱网长时间静默的根因。
	uploadingShown := make(chan struct{})
	announce := time.AfterFunc(slowNotifyDelay, func() {
		Notify("moshdrop", fmt.Sprintf(msg("n.uploading"), float64(total)/(1<<20), p.up.target))
		close(uploadingShown)
	})
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout(total)+30*time.Second)
	remotes, err := p.up.Upload(ctx, d.tokens)
	cancel()
	ms := time.Since(start).Milliseconds()
	// Stop()==false ⇒ 定时器已触发、「上传中」goroutine 已启动；等它真弹完再往下，
	// 保证「上传中」严格早于「已送达/失败」（两条横幅跨 goroutine，否则毫秒级窗口会倒序）。
	announced := !announce.Stop()
	if announced {
		<-uploadingShown
	}
	if err != nil {
		if d.noReplay {
			Notify("moshdrop", fmt.Sprintf(msg("n.clipfailed"), err.Error()))
		} else {
			Notify("moshdrop", fmt.Sprintf(msg("n.failed"), err.Error()))
		}
		logEvent(p.stateDir, dropEvent{Target: p.up.target, Source: d.source, Files: baseNames(d.tokens), Bytes: total, Ms: ms, Ok: false, Err: err.Error()})
		p.failOpen(d)
		return
	}
	if announced {
		Notify("moshdrop", msg("n.delivered"))
	}
	logEvent(p.stateDir, dropEvent{Target: p.up.target, Source: d.source, Files: baseNames(d.tokens), Bytes: total, Ms: ms, Ok: true})
	p.injectWhenClean(FormatInjection(remotes), d.bracketed)
}

// failOpen：拖拽原样回放本地路径；剪贴板贴图（noReplay）无可回放物，具体原因已由调用方通知。
func (p *proxyState) failOpen(d drop) {
	if !d.noReplay {
		p.injectWhenClean(d.payload, d.bracketed)
	}
}

// looksLikeBarePathChunk：以 / 开头、无 ESC 及（除空白外）控制字节、长度合理。
// 只是语法门，真正把关靠异步 stat 验真。
func looksLikeBarePathChunk(b []byte) bool {
	if len(b) < 6 || len(b) > maxPaste || b[0] != '/' {
		return false
	}
	for _, c := range b {
		if c == 0x1b || (c < 0x20 && c != '\t' && c != '\r' && c != '\n') {
			return false
		}
	}
	return true
}
