package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// runPaste 全链路（剪贴板/ssh/pbcopy 全替身）：
// 有图 → 上传 → 打印远端路径 + 回填剪贴板；无图 → 报错退出 1。
func TestRunPaste(t *testing.T) {
	t.Setenv("MOSHDROP_STATE_DIR", t.TempDir())
	oldRead, oldCopy, oldRun := readClipboardPNG, copyToClipboard, realRunHook
	defer func() { readClipboardPNG, copyToClipboard = oldRead, oldCopy; realRunHook = oldRun }()

	var copied string
	copyToClipboard = func(s string) error { copied = s; return nil }
	readClipboardPNG = func(dst string) error { return os.WriteFile(dst, []byte("PNGDATA"), 0o600) }
	calls := 0
	realRunHook = func(_ context.Context, _ io.Reader, argv []string) (cmdResult, bool) {
		calls++
		if calls == 1 {
			return cmdResult{stdout: []byte("/r/.moshdrop")}, true // ensure
		}
		if strings.Contains(argv[len(argv)-1], "cat >") {
			return cmdResult{stdout: []byte("Clipboard test.png\n")}, true
		}
		return cmdResult{}, true // Close 的 -O exit
	}

	if code := runPaste([]string{"ccc"}); code != 0 {
		t.Fatalf("应成功, got exit %d", code)
	}
	if copied != "/r/.moshdrop/Clipboard test.png" {
		t.Fatalf("剪贴板未回填远端路径: %q", copied)
	}

	// 无图场景
	readClipboardPNG = func(dst string) error { return os.ErrNotExist }
	if code := runPaste([]string{"ccc"}); code != 1 {
		t.Fatalf("无图应退出 1, got %d", code)
	}
	// 无 host 场景
	if code := runPaste(nil); code != 1 {
		t.Fatal("无 host 应退出 1")
	}
}
