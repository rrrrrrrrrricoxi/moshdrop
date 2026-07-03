package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 可注入的系统边界（测试替身用）。
var readClipboardPNG = realReadClipboardPNG
var copyToClipboard = realCopyToClipboard

// runPaste: 剪贴板图片 → 上传 → 远端路径回填剪贴板并打印。
// 典型用法：Cmd+Ctrl+Shift+4 截图进剪贴板 → moshdrop paste ccc → 到 mosh 窗口 Cmd+V。
func runPaste(args []string) int {
	target := os.Getenv("MOSHDROP_TARGET")
	sshArgv := []string{"ssh"}
	if t, sa, err := DeriveSSHTarget(args); err == nil {
		if target == "" {
			target = t
		}
		sshArgv = sa
	}
	stateDir := stateDirPath()
	_ = os.MkdirAll(stateDir, 0o700)
	cfg := LoadConfig(stateDir, target)
	setLang(cfg.Lang)
	if target == "" {
		fmt.Fprintln(os.Stderr, msg("p.usage"))
		return 1
	}

	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("moshdrop-clip-%d.png", os.Getpid()))
	defer os.Remove(tmp)
	if err := readClipboardPNG(tmp); err != nil {
		fmt.Fprintln(os.Stderr, msg("p.noimg"))
		return 1
	}
	// 远端落名用时间戳，仿 macOS 截图命名习惯
	nice := filepath.Join(os.TempDir(), time.Now().Format("Clipboard 2006-01-02 at 15.04.05")+".png")
	if err := os.Rename(tmp, nice); err == nil {
		tmp = nice
		defer os.Remove(nice)
	}

	u := NewUploader(target, sshArgv, stateDir)
	u.ApplyConfig(cfg) // paste 是显式命令：不受 intercept 开关影响
	defer u.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	remotes, err := u.Upload(ctx, []string{tmp})
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Sprintf(msg("n.failed"), err.Error()))
		return 1
	}
	_ = copyToClipboard(remotes[0])
	fmt.Println(remotes[0])
	fmt.Println(msg("p.ok"))
	return 0
}

// realReadClipboardPNG 把剪贴板图片写为 PNG 文件（无图片则报错）。
func realReadClipboardPNG(dst string) error {
	script := `try
	set d to the clipboard as «class PNGf»
on error
	error "no image in clipboard"
end try
set f to open for access POSIX file ` + appleScriptQuote(dst) + ` with write permission
set eof of f to 0
write d to f
close access f`
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	if fi, err := os.Stat(dst); err != nil || fi.Size() == 0 {
		return fmt.Errorf("clipboard dump empty")
	}
	return nil
}

func realCopyToClipboard(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

func appleScriptQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
