package main

import (
	"context"
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

// RunProxy 在 pty 上运行 cmd，本端 stdio 与之互接；
// stdin 方向拦截拖拽文件粘贴，其余逐字节透传。返回子进程退出码。
func RunProxy(cmd *exec.Cmd, up *Uploader) (int, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 1, err
	}
	defer ptmx.Close()

	// 终端尺寸：启动时 + SIGWINCH
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	resize := func() {
		if ws, err := pty.GetsizeFull(os.Stdin); err == nil && ws.Cols > 0 && ws.Rows > 0 {
			_ = pty.Setsize(ptmx, ws)
		} else {
			// 非 tty / 尺寸不可得：兜底 80x24，0x0 会让 mosh 客户端崩溃
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

	// 本地终端进 raw（仅当 stdin 是 tty；测试管道自动跳过）
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			defer term.Restore(fd, old)
		}
	}

	// 远端→屏幕：零解析直通
	go func() { _, _ = io.Copy(os.Stdout, ptmx) }()

	var mu sync.Mutex // 串行化 stdin 循环与异步注入对 ptmx 的写
	write := func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = ptmx.Write(b)
	}
	writePaste := func(payload string, bracketed bool) {
		if bracketed {
			write([]byte(bpStart + payload + bpEnd))
		} else {
			write([]byte(payload))
		}
	}
	intercept := func(payload string, bracketed bool) bool {
		paths, ok := ParseDraggedPaste(payload)
		if !ok {
			return false
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			remotes, err := up.Upload(ctx, paths)
			if err != nil {
				Notify("moshdrop", "上传失败，已放行本地路径："+err.Error())
				writePaste(payload, bracketed)
				return
			}
			writePaste(FormatInjection(remotes), bracketed)
		}()
		return true
	}

	// 空闲定时器：吐半截标记 / 中止超时粘贴
	var sc Scanner
	var scMu sync.Mutex
	lastFeed := time.Now()
	stopTick := make(chan struct{})
	defer close(stopTick)
	go func() {
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopTick:
				return
			case <-tick.C:
				scMu.Lock()
				evs := sc.Idle(time.Since(lastFeed))
				scMu.Unlock()
				for _, ev := range evs {
					write(ev.Data)
				}
			}
		}
	}()

	// stdin 循环
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk := append([]byte{}, buf[:n]...)
				scMu.Lock()
				lastFeed = time.Now()
				inPaste := sc.InPaste()
				scMu.Unlock()
				// 非括号后备：整块裸路径（不处于粘贴态时）
				if !inPaste && looksLikeBarePathChunk(chunk) &&
					intercept(string(chunk), false) {
					continue
				}
				scMu.Lock()
				evs := sc.Feed(chunk)
				scMu.Unlock()
				for _, ev := range evs {
					if ev.Type == EvPaste {
						payload := string(ev.Data)
						if !intercept(payload, true) {
							writePaste(payload, true)
						}
					} else {
						write(ev.Data)
					}
				}
			}
			if err != nil {
				// 本端 stdin 关闭（终端没了/管道测试结束）：
				// 双 EOT（规范模式下第一个只冲刷行缓冲，第二个才是 EOF），
				// 不退再 SIGHUP，最后 KILL 兜底，绝不留僵尸。
				write([]byte{0x04, 0x04})
				go func() {
					time.Sleep(2 * time.Second)
					if cmd.Process != nil {
						_ = cmd.Process.Signal(syscall.SIGHUP)
					}
					time.Sleep(2 * time.Second)
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
				}()
				return
			}
		}
	}()

	err = cmd.Wait()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// looksLikeBarePathChunk：无括号粘贴模式下拖拽产生的"整块裸路径"启发式。
// 以 / 开头、无 ESC 及（除空白外）控制字节、长度合理。真正把关靠 stat 验真。
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
