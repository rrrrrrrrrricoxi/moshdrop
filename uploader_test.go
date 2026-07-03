package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner 按队列吐出预设结果，并记录每次调用。
type fakeCall struct {
	argv     []string
	hadStdin bool
}

func fakeRunner(results ...cmdResult) (*[]fakeCall, func(context.Context, io.Reader, []string) cmdResult) {
	calls := &[]fakeCall{}
	i := 0
	return calls, func(_ context.Context, stdin io.Reader, argv []string) cmdResult {
		*calls = append(*calls, fakeCall{argv: argv, hadStdin: stdin != nil})
		if i >= len(results) {
			return cmdResult{err: fmt.Errorf("fakeRunner: 队列耗尽")}
		}
		r := results[i]
		i++
		return r
	}
}

func tmpFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUploaderDisabledFastFail(t *testing.T) {
	u := NewUploader("", nil, t.TempDir())
	if !u.Disabled() {
		t.Fatal("空 target 必须进入 disabled 态")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	t0 := time.Now()
	if _, err := u.Upload(ctx, []string{"/etc/hosts"}); err == nil {
		t.Fatal("disabled 态 Upload 必须报错")
	}
	if time.Since(t0) > 100*time.Millisecond {
		t.Fatal("disabled 态必须立即失败，不许等待任何超时（审计 A1）")
	}
}

// 审计 A2 回归：Prewarm 失败绝不粘住——下一次 Upload 必须重试建连。
func TestEnsureRetryNotSticky(t *testing.T) {
	f := tmpFile(t, "shot.png", "DATA")
	calls, run := fakeRunner(
		cmdResult{err: fmt.Errorf("exit 255"), stderr: []byte("ssh: connect to host ccc: Connection timed out")},
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{stdout: []byte("shot.png\n")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = run
	u.Prewarm() // 第一次建连失败 —— 不得缓存
	ctx := context.Background()
	remotes, err := u.Upload(ctx, []string{f})
	if err != nil {
		t.Fatalf("重试建连应成功: %v", err)
	}
	if remotes[0] != "/r/.moshdrop/shot.png" {
		t.Fatalf("got %v", remotes)
	}
	if len(*calls) != 3 {
		t.Fatalf("期望 3 次调用(失败ensure/重试ensure/上传), got %d", len(*calls))
	}
}

// 上传中途失败 → 重建连接静默重试一次。
func TestUploadSilentRetryOnce(t *testing.T) {
	f := tmpFile(t, "a.txt", "X")
	calls, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},                                     // ensure
		cmdResult{err: fmt.Errorf("exit 255"), stderr: []byte("Broken pipe")},         // upload 失败
		cmdResult{stdout: []byte("/r/.moshdrop")},                                     // 重建 ensure
		cmdResult{stdout: []byte("a.txt\n")},                                          // 重试成功
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = run
	remotes, err := u.Upload(context.Background(), []string{f})
	if err != nil || remotes[0] != "/r/.moshdrop/a.txt" {
		t.Fatalf("got %v %v", remotes, err)
	}
	if len(*calls) != 4 {
		t.Fatalf("期望 4 次调用, got %d", len(*calls))
	}
}

// 审计 C4 回归：只剥协议尾换行，文件名自身首尾空白原样保留。
func TestUploadNamePreservesEdgeWhitespace(t *testing.T) {
	f := tmpFile(t, "x.png", "X")
	_, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{stdout: []byte(" weird name.png \n")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = run
	remotes, err := u.Upload(context.Background(), []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if remotes[0] != "/r/.moshdrop/ weird name.png " {
		t.Fatalf("首尾空白被破坏: %q", remotes[0])
	}
}

// 审计 C1 回归：--ssh 自定义命令必须成为上传通道基座。
func TestSSHCmdUsesCustomArgv(t *testing.T) {
	u := NewUploader("host", []string{"ssh", "-p", "2222"}, t.TempDir())
	argv := u.sshCmd("x")
	if strings.Join(argv[:3], " ") != "ssh -p 2222" {
		t.Fatalf("自定义 ssh 基座丢失: %v", argv[:3])
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "BatchMode=yes") || argv[len(argv)-2] != "host" {
		t.Fatalf("缺少必备选项或目标: %v", argv)
	}
	// 审计 C3 回归：远端命令必须包 sh -c
	if !strings.HasPrefix(argv[len(argv)-1], "x") && !strings.Contains(argv[len(argv)-1], "sh -c") {
		t.Fatalf("script 位置错误: %q", argv[len(argv)-1])
	}
}

func TestShellQuote(t *testing.T) {
	if shellQuote(`a'b c`) != `'a'\''b c'` {
		t.Fatalf("got %q", shellQuote(`a'b c`))
	}
}

// 审计 E2 回归：这次真正触达空名兜底分支。
func TestSanitizeName(t *testing.T) {
	if sanitizeName("/tmp/a b.png") != "a b.png" {
		t.Fatal("应取 basename")
	}
	if sanitizeName("/tmp/we\x07ird") != "weird" {
		t.Fatal("应剔除控制字符")
	}
	if got := sanitizeName("/tmp/\x07\x08"); got != "file" {
		t.Fatalf("纯控制字符名必须兜底为 file, got %q", got)
	}
	if got := sanitizeName("/"); got != "file" {
		t.Fatalf("根路径必须兜底为 file, got %q", got)
	}
}

// —— 真机 e2e（审计 E5：主机可配置，默认跳过）——
func e2eHost(t *testing.T) string {
	if os.Getenv("MOSHDROP_E2E") != "1" {
		t.Skip("需 MOSHDROP_E2E=1（可用 MOSHDROP_E2E_HOST 指定主机）")
	}
	if h := os.Getenv("MOSHDROP_E2E_HOST"); h != "" {
		return h
	}
	return "ccc"
}

func TestUploaderRealSSH(t *testing.T) {
	host := e2eHost(t)
	f1 := tmpFile(t, "moshdrop test 图 1.png", "PAYLOAD-ONE")
	f2 := tmpFile(t, "plain.txt", "PAYLOAD-TWO")

	u := NewUploader(host, []string{"ssh"}, t.TempDir())
	go u.Prewarm()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	remotes, err := u.Upload(ctx, []string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"PAYLOAD-ONE", "PAYLOAD-TWO"} {
		out, err := exec.Command("ssh", "-o", "BatchMode=yes", host,
			"cat "+shellQuote(remotes[i])+" && rm -f "+shellQuote(remotes[i])).Output()
		if err != nil || string(out) != want {
			t.Fatalf("远端内容校验失败 %s: %q %v", remotes[i], out, err)
		}
	}
	// 重名并存时第二次必须换名
	r1, _ := u.Upload(ctx, []string{f2})
	r2, err := u.Upload(ctx, []string{f2})
	if err != nil || r1[0] == r2[0] {
		t.Fatalf("重名未加后缀: %v vs %v (%v)", r1, r2, err)
	}
	exec.Command("ssh", "-o", "BatchMode=yes", host,
		"rm -f "+shellQuote(r1[0])+" "+shellQuote(r2[0])).Run()
	u.Close()
}
