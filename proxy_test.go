package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var errFake = errors.New("exit status 255")

// rawCat 造一个"回显关闭的透明远端"：输出 == cat 实际收到的字节，可做精确断言（审计 E1）。
// 就绪后打哨兵，避免输入抢在 stty 生效前到达（否则回显/SIGINT 会污染断言）。
const rdy = "@RDY@"

func rawCat() *exec.Cmd {
	return exec.Command("sh", "-c", "stty raw -echo 2>/dev/null; printf "+rdy+"; exec cat")
}

// runProxyHarness 驱动 RunProxy：持续收集输出，直到 pred 满足或超时，再关 stdin 收尾。
func runProxyHarness(t *testing.T, input []byte, up *Uploader, pred func(string) bool, timeout time.Duration) string {
	t.Helper()
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	var mu sync.Mutex
	var buf bytes.Buffer
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, err := outR.Read(tmp)
			if n > 0 {
				mu.Lock()
				buf.Write(tmp[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() { RunProxy(rawCat(), up); close(done) }()

	// 等替身就绪（stty 已生效）再发输入
	start := -1
	for d := time.Now().Add(3 * time.Second); time.Now().Before(d); {
		mu.Lock()
		if i := strings.Index(buf.String(), rdy); i >= 0 {
			start = i + len(rdy)
		}
		mu.Unlock()
		if start >= 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if start < 0 {
		t.Fatal("替身未就绪")
	}
	inW.Write(input)

	out := func() string {
		mu.Lock()
		defer mu.Unlock()
		s := buf.String()
		if len(s) < start {
			return ""
		}
		return s[start:]
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred(out()) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunProxy 未随 stdin 关闭而退出")
	}
	outW.Close()
	time.Sleep(50 * time.Millisecond)
	return out()
}

func noopUploader(t *testing.T) *Uploader {
	// 永不该被触碰的替身：一旦被调用即测试失败
	u := NewUploader("never-touch", nil, t.TempDir())
	u.run = func(_ context.Context, _ io.Reader, _ []string) cmdResult {
		t.Error("uploader 不应被调用")
		return cmdResult{}
	}
	return u
}

func TestProxyTransparentExact(t *testing.T) {
	in := []byte("hello\rworld\x1b[A\x03")
	got := runProxyHarness(t, in, noopUploader(t), func(s string) bool { return len(s) >= len(in) }, 3*time.Second)
	if got != string(in) {
		t.Fatalf("必须逐字节透传:\n got %q\nwant %q", got, in)
	}
}

// 审计 E3 回归：非文件粘贴必须原样放行且括号标记完整保留。
func TestProxyNonFilePasteKeepsMarkers(t *testing.T) {
	in := bpS + "just some words" + bpE
	got := runProxyHarness(t, []byte(in), noopUploader(t), func(s string) bool { return len(s) >= len(in) }, 3*time.Second)
	if got != in {
		t.Fatalf("括号粘贴语义被破坏:\n got %q\nwant %q", got, in)
	}
}

// 形如路径但文件不存在：异步验真失败后原样回放（标记保留）。
func TestProxyNonexistentPathReplay(t *testing.T) {
	in := bpS + `/no/such/file\ here.png` + bpE
	got := runProxyHarness(t, []byte(in), noopUploader(t), func(s string) bool { return len(s) >= len(in) }, 3*time.Second)
	if got != in {
		t.Fatalf("不存在的路径必须原样回放:\n got %q\nwant %q", got, in)
	}
}

// 审计 A1 回归：disabled 态（无 ssh 目标）＝真·纯透传，真实文件路径也立刻放行。
func TestProxyDisabledPassthroughImmediate(t *testing.T) {
	f := tmpFile(t, "real.png", "X")
	u := NewUploader("", nil, t.TempDir())
	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	t0 := time.Now()
	got := runProxyHarness(t, []byte(in), u, func(s string) bool { return len(s) >= len(in) }, 3*time.Second)
	if got != in {
		t.Fatalf("disabled 态必须原样透传: %q", got)
	}
	if time.Since(t0) > 2*time.Second {
		t.Fatal("disabled 态不得有任何等待")
	}
}

// 上传失败 → fail-open 立即回放（用 fake runner 模拟网络死亡，不依赖真机）。
func TestProxyFailOpenFastReplay(t *testing.T) {
	f := tmpFile(t, "real.png", "X")
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = func(_ context.Context, _ io.Reader, _ []string) cmdResult {
		return cmdResult{err: errFake, stderr: []byte("ssh: connect to host ccc: Connection refused")}
	}
	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	t0 := time.Now()
	got := runProxyHarness(t, []byte(in), u, func(s string) bool { return strings.Contains(s, filepath.Base(f)) }, 5*time.Second)
	if !strings.Contains(got, bpS) || !strings.Contains(got, bpE) {
		t.Fatalf("fail-open 回放必须保留括号标记: %q", got)
	}
	if time.Since(t0) > 3*time.Second {
		t.Fatal("连接拒绝类失败必须秒级回放，不许拖 90s（审计 A4）")
	}
}

func TestProxyInterceptRealE2E(t *testing.T) {
	host := e2eHost(t)
	f := tmpFile(t, "drop me.png", "E2E-BYTES")
	u := NewUploader(host, []string{"ssh"}, t.TempDir())
	go u.Prewarm()

	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	got := runProxyHarness(t, []byte(in), u,
		func(s string) bool { return strings.Contains(s, "/.moshdrop/") && strings.Contains(s, bpE) }, 15*time.Second)

	// 输出应恰为一段括号粘贴：ESC[200~<远端路径>ESC[201~，本地路径零泄漏
	if strings.Contains(got, filepath.Dir(f)) {
		t.Fatalf("本地路径泄漏: %q", got)
	}
	i := strings.Index(got, bpS)
	j := strings.Index(got, bpE)
	if i < 0 || j <= i {
		t.Fatalf("注入不是完整括号粘贴: %q", got)
	}
	remote := strings.ReplaceAll(got[i+len(bpS):j], `\ `, " ")
	out, err := exec.Command("ssh", "-o", "BatchMode=yes", host,
		"cat "+shellQuote(remote)+" && rm -f "+shellQuote(remote)).Output()
	if err != nil || string(out) != "E2E-BYTES" {
		t.Fatalf("远端校验失败 %q: %q %v", remote, out, err)
	}
	u.Close()
}
