package main

import (
	"fmt"
	"os/exec"
)

// Notify 弹 macOS 系统通知，best-effort（失败静默）。
func Notify(title, msg string) {
	script := fmt.Sprintf("display notification %q with title %q", msg, title)
	_ = exec.Command("osascript", "-e", script).Run()
}
