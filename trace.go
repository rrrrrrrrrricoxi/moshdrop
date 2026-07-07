package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

// trace: 临时诊断钩子。MOSHDROP_TRACE=<file> 时把关键站点追加写入文件。
func trace(format string, a ...any) {
	p := os.Getenv("MOSHDROP_TRACE")
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, time.Now().Format("15:04:05.000 ")+format+"\n", a...)
}

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

func init() {
	if os.Getenv("MOSHDROP_TRACE") == "" {
		return
	}
	go dumpOnUSR1()
}
