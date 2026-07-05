package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Notify 弹 macOS 系统通知，best-effort（失败静默）。
// MOSHDROP_MUTE_NOTIFY=1 时静音——测试套件模拟故障时绝不能骚扰真用户的通知中心。
// 是可替换的包变量：测试可覆盖它以捕获通知，无需真的弹窗。
var Notify = realNotify

func realNotify(title, msg string) {
	if os.Getenv("MOSHDROP_MUTE_NOTIFY") != "" {
		return
	}
	// 限时执行：osascript 万一卡住也不能拖住串行的 dropWorker（迟到横幅那条路径会等它发完）。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	script := fmt.Sprintf("display notification %q with title %q", msg, title)
	_ = exec.CommandContext(ctx, "osascript", "-e", script).Run()
}
