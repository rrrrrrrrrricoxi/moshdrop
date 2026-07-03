package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
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

	// 外部信号（kill/终端关闭）：转发给子进程让其自然退出；终端态由下方 defer 恢复
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		for s := range sigs {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(s)
			}
			go func() {
				time.Sleep(3 * time.Second)
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}()
		}
	}()
	defer signal.Stop(sigs)

	// 本地终端进 raw（仅当 stdin 是 tty；测试管道自动跳过）
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer term.Restore(fd, old)
		}
	}

	// 远端→屏幕：零解析直通
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, ptmx)
		close(copyDone)
	}()

	p := &proxyState{ptmx: ptmx, up: up, stateDir: stateDirPath(), lastFeed: time.Now(), stop: make(chan struct{})}
	go p.idleLoop()
	go p.stdinLoop(cmd)

	err = cmd.Wait()
	close(p.stop)
	// 排空最后一屏输出再返回（子进程退出瞬间可能还有未读的 pty 缓冲）
	select {
	case <-copyDone:
	case <-time.After(300 * time.Millisecond):
	}
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

// injectWhenClean 等输入流到达干净点（不在粘贴中、无半截转义序列滞留）再注入，
// 避免把注入内容劈进用户正在传输的序列中间；最多等 1s 兜底强注。
func (p *proxyState) injectWhenClean(payload string, bracketed bool) {
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		if (!p.sc.InPaste() && !p.sc.HasPending()) || time.Now().After(deadline) {
			p.writePasteLocked(payload, bracketed)
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *proxyState) idleLoop() {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
			p.mu.Lock()
			evs := p.sc.Idle(time.Since(p.lastFeed))
			for _, ev := range evs {
				p.write(ev.Data)
			}
			p.mu.Unlock()
		}
	}
}

func (p *proxyState) stdinLoop(cmd *exec.Cmd) {
	buf := make([]byte, 32*1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			chunk := append([]byte{}, buf[:n]...)
			p.mu.Lock()
			p.lastFeed = time.Now()
			// 非括号后备：无括号粘贴模式下拖拽产生的"整块裸路径"
			if !p.sc.InPaste() && looksLikeBarePathChunk(chunk) {
				p.mu.Unlock()
				p.handlePaste(string(chunk), false)
				continue
			}
			evs := p.sc.Feed(chunk)
			var pastes []string
			for _, ev := range evs {
				if ev.Type == EvPaste {
					pastes = append(pastes, string(ev.Data))
				} else {
					p.write(ev.Data)
				}
			}
			p.mu.Unlock()
			for _, pl := range pastes {
				p.handlePaste(pl, true)
			}
		}
		if err != nil {
			// 终端没了：按真实终端关闭的语义给子进程发 HUP。
			// 绝不注入 ^D 之类字节——那会误杀远端 tmux 里的前台进程。
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGHUP)
			}
			go func() {
				time.Sleep(2 * time.Second)
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}()
			return
		}
	}
}

// handlePaste：语法匹配则暂扣载荷，验真/上传全部走异步——输入热路径零阻塞。
// 验真失败（非文件/网络卷挂死）立即原样回放；上传失败 fail-open 回放 + 通知 + 留痕。
func (p *proxyState) handlePaste(payload string, bracketed bool) {
	if p.up.Disabled() {
		p.replay(payload, bracketed)
		return
	}
	tokens, ok := parsePasteSyntax(payload)
	if !ok {
		p.replay(payload, bracketed)
		return
	}
	go func() {
		start := time.Now()
		total, ok := verifyLocalFiles(tokens, 500*time.Millisecond)
		if !ok {
			p.injectWhenClean(payload, bracketed)
			return
		}
		if total > 8<<20 {
			Notify("moshdrop", fmt.Sprintf("正在上传 %.1f MB → %s …", float64(total)/(1<<20), p.up.target))
		}
		ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout(total)+30*time.Second)
		defer cancel()
		remotes, err := p.up.Upload(ctx, tokens)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			Notify("moshdrop", "上传失败，已放行本地路径。原因："+err.Error())
			logEvent(p.stateDir, dropEvent{Target: p.up.target, Files: baseNames(tokens), Bytes: total, Ms: ms, Ok: false, Err: err.Error()})
			p.injectWhenClean(payload, bracketed)
			return
		}
		if total > 8<<20 {
			Notify("moshdrop", "已送达 ✓")
		}
		logEvent(p.stateDir, dropEvent{Target: p.up.target, Files: baseNames(tokens), Bytes: total, Ms: ms, Ok: true})
		p.injectWhenClean(FormatInjection(remotes), bracketed)
	}()
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
