package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runThrough 启动 RunProxy 包 `cat`，写入 input，等待 wait 后收集全部输出。
func runThrough(t *testing.T, input []byte, up *Uploader, wait time.Duration) string {
	t.Helper()
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	done := make(chan struct{})
	go func() { RunProxy(exec.Command("cat"), up); close(done) }()
	inW.Write(input)
	time.Sleep(wait)
	inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunProxy 未随 stdin 关闭而退出")
	}
	outW.Close()
	var buf bytes.Buffer
	buf.ReadFrom(outR)
	return buf.String()
}

func TestProxyTransparent(t *testing.T) {
	up := NewUploader("invalid-host-should-not-be-touched", t.TempDir())
	got := runThrough(t, []byte("hello\rworld"), up, 300*time.Millisecond)
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Fatalf("普通输入必须透传, got %q", got)
	}
}

func TestProxyNonFilePastePassthrough(t *testing.T) {
	up := NewUploader("invalid-host-should-not-be-touched", t.TempDir())
	in := []byte(bpS + "just some words" + bpE)
	got := runThrough(t, in, up, 300*time.Millisecond)
	if !strings.Contains(got, "just some words") {
		t.Fatalf("非文件粘贴必须原样放行, got %q", got)
	}
}

func TestProxyInterceptE2E(t *testing.T) {
	if os.Getenv("MOSHDROP_E2E") != "1" {
		t.Skip("需 MOSHDROP_E2E=1")
	}
	// 清掉此前失败运行可能留下的远端残留，保证 basename 可复现
	exec.Command("ssh", "-o", "BatchMode=yes", "ccc",
		`rm -f "$HOME/.moshdrop/drop me"*`).Run()

	dir := t.TempDir()
	local := filepath.Join(dir, "drop me.png")
	os.WriteFile(local, []byte("E2E-BYTES"), 0o644)
	up := NewUploader("ccc", t.TempDir())
	go up.Prewarm()

	in := []byte(bpS + strings.ReplaceAll(local, " ", `\ `) + bpE)
	got := runThrough(t, in, up, 8*time.Second)
	if strings.Contains(got, dir) {
		t.Fatalf("本地路径泄漏(未拦截): %q", got)
	}
	if !strings.Contains(got, "/.moshdrop/drop") {
		t.Fatalf("未注入远端路径: %q", got)
	}
	// 提取注入路径：pty 回显会把 ESC 显示成 "^[" 字面量，
	// 只有 cat 原样输出的那份含真实括号标记——按它定位。
	i := strings.Index(got, bpS+"/")
	if i < 0 {
		t.Fatalf("输出中找不到真实括号标记: %q", got)
	}
	frag := got[i+len(bpS):]
	j := strings.Index(frag, bpE)
	if j < 0 {
		t.Fatalf("注入缺少结束标记: %q", frag)
	}
	remote := strings.TrimSpace(strings.ReplaceAll(frag[:j], `\ `, " "))
	out, err := exec.Command("ssh", "-o", "BatchMode=yes", "ccc",
		"cat "+shellQuote(remote)+" && rm -f "+shellQuote(remote)).Output()
	if err != nil || string(out) != "E2E-BYTES" {
		t.Fatalf("远端校验失败 %q: %q %v", remote, out, err)
	}
}
