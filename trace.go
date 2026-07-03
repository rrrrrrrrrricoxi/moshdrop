package main

import (
	"fmt"
	"os"
	"time"
)

// trace: 临时诊断钩子。MOSHDROP_TRACE=<file> 时把关键站点追加写入文件。
func trace(format string, a ...interface{}) {
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

func init() {
	if os.Getenv("MOSHDROP_TRACE") == "" {
		return
	}
	go dumpOnUSR1()
}
