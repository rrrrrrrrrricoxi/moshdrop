package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func e2eTarget(t *testing.T) string {
	if os.Getenv("MOSHDROP_E2E") != "1" {
		t.Skip("需 MOSHDROP_E2E=1 且 ccc 可达")
	}
	return "ccc"
}

func TestUploaderRealSSH(t *testing.T) {
	target := e2eTarget(t)
	dir := t.TempDir()
	f1 := filepath.Join(dir, "moshdrop test 图 1.png")
	f2 := filepath.Join(dir, "plain.txt")
	os.WriteFile(f1, []byte("PAYLOAD-ONE"), 0o644)
	os.WriteFile(f2, []byte("PAYLOAD-TWO"), 0o644)

	u := NewUploader(target, t.TempDir())
	go u.Prewarm()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	remotes, err := u.Upload(ctx, []string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 2 || !strings.HasPrefix(remotes[0], "/") {
		t.Fatalf("要求返回绝对路径: %v", remotes)
	}
	// 远端逐一校验内容，然后清理
	for i, want := range []string{"PAYLOAD-ONE", "PAYLOAD-TWO"} {
		out, err := exec.Command("ssh", "-o", "BatchMode=yes", target,
			"cat "+shellQuote(remotes[i])+" && rm -f "+shellQuote(remotes[i])).Output()
		if err != nil || string(out) != want {
			t.Fatalf("远端内容校验失败 %s: %q %v", remotes[i], out, err)
		}
	}

	// 重名冲突：并存时第二次上传必须换名
	r1, err := u.Upload(ctx, []string{f2})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := u.Upload(ctx, []string{f2})
	if err != nil {
		t.Fatal(err)
	}
	if r1[0] == r2[0] {
		t.Fatalf("重名未加后缀: %v vs %v", r1, r2)
	}
	exec.Command("ssh", "-o", "BatchMode=yes", target,
		"rm -f "+shellQuote(r1[0])+" "+shellQuote(r2[0])).Run()
}

func TestShellQuote(t *testing.T) {
	if shellQuote(`a'b c`) != `'a'\''b c'` {
		t.Fatalf("got %q", shellQuote(`a'b c`))
	}
}

func TestSanitizeName(t *testing.T) {
	if sanitizeName("/tmp/a b.png") != "a b.png" {
		t.Fatal("应取 basename")
	}
	if sanitizeName("/tmp/we\x07ird") != "weird" {
		t.Fatal("应剔除控制字符")
	}
	if sanitizeName("/tmp/") == "" {
		t.Fatal("空名要有兜底")
	}
}
