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

	tmp, err := clipboardToTempPNG()
	if err != nil {
		fmt.Fprintln(os.Stderr, msg("p.noimg"))
		return 1
	}
	defer os.RemoveAll(filepath.Dir(tmp))

	u := NewUploader(target, sshArgv, stateDir)
	u.ApplyConfig(cfg) // paste 是显式命令：不受 intercept 开关影响
	defer u.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var size int64
	if fi, serr := os.Stat(tmp); serr == nil {
		size = fi.Size()
	}
	start := time.Now()
	remotes, err := u.Upload(ctx, []string{tmp})
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Sprintf(msg("n.failed"), err.Error()))
		logEvent(stateDir, dropEvent{Target: target, Source: "paste-cmd", Files: baseNames([]string{tmp}), Bytes: size, Ms: time.Since(start).Milliseconds(), Ok: false, Err: err.Error()})
		return 1
	}
	logEvent(stateDir, dropEvent{Target: target, Source: "paste-cmd", Files: baseNames([]string{tmp}), Bytes: size, Ms: time.Since(start).Milliseconds(), Ok: true})
	_ = copyToClipboard(remotes[0])
	fmt.Println(remotes[0])
	fmt.Println(msg("p.ok"))
	return 0
}

// clipboardToTempPNG 把剪贴板图片落成独占临时目录里的 PNG（时间戳命名，仿 macOS 截图
// 习惯；独占目录杜绝同秒两次触发互相覆盖）。调用方负责删除整个父目录。
func clipboardToTempPNG() (string, error) {
	dir, err := os.MkdirTemp("", "moshdrop-clip-")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, time.Now().Format("Clipboard 2006-01-02 at 15.04.05")+".png")
	if err := readClipboardPNG(p); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	_ = os.Chmod(p, 0o600) // AppleScript 默认写出 0644；截图可能含敏感内容，双层收紧
	return p, nil
}

// sweepStaleClipTemps 清扫上次进程中途死亡残留的剪贴板临时目录（>1h 的 moshdrop-clip-*）。
// 与远端 .part-* 孤儿清扫同款兜底；截图可能含敏感内容，不能指望 macOS 数天后的 TMPDIR 轮转。
// 1 小时余量远大于上传超时上限，绝不会误伤在途目录。
func sweepStaleClipTemps() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "moshdrop-clip-*"))
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && time.Since(fi.ModTime()) > time.Hour {
			_ = os.RemoveAll(d)
		}
	}
}

// realReadClipboardPNG 把剪贴板图片写为 PNG 文件（无图片则报错）。
// 硬超时：剪贴板读取可能挂死（Universal Clipboard 懒取等）——挂死会让已吞的 Ctrl+V
// 永无下文，且每次重按累积泄漏 goroutine/子进程/临时目录。与 notify.go 同款防线。
func realReadClipboardPNG(dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := `try
	set d to the clipboard as «class PNGf»
on error
	error "no image in clipboard"
end try
set f to open for access POSIX file ` + appleScriptQuote(dst) + ` with write permission
set eof of f to 0
write d to f
close access f`
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).CombinedOutput()
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
