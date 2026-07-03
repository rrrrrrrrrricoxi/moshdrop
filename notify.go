package main

import (
	"fmt"
	"os"
	"os/exec"
)

// Notify 弹 macOS 系统通知，best-effort（失败静默）。
// MOSHDROP_MUTE_NOTIFY=1 时静音——测试套件模拟故障时绝不能骚扰真用户的通知中心。
func Notify(title, msg string) {
	if os.Getenv("MOSHDROP_MUTE_NOTIFY") != "" {
		return
	}
	script := fmt.Sprintf("display notification %q with title %q", msg, title)
	_ = exec.Command("osascript", "-e", script).Run()
}
