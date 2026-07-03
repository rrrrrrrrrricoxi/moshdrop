package main

import (
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

// dumpOnUSR1: MOSHDROP_TRACE 模式下收到 SIGUSR1 就把全部 goroutine 栈写进 trace 文件。
func dumpOnUSR1() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	for range ch {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		trace("=== GOROUTINE DUMP ===\n%s", buf[:n])
	}
}
