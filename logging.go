package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// dropEvent 是一次拖拽处理的流水账（只记元数据，不记文件内容）。
type dropEvent struct {
	Ts     string   `json:"ts"`
	Target string   `json:"target"`
	Files  []string `json:"files"`
	Bytes  int64    `json:"bytes"`
	Ms     int64    `json:"ms"`
	Ok     bool     `json:"ok"`
	Err    string   `json:"err,omitempty"`
}

// logEvent 追加一行 JSONL 到 ~/.moshdrop/events.log；超 1MB 轮转为 .old。
func logEvent(stateDir string, ev dropEvent) {
	ev.Ts = time.Now().Format(time.RFC3339)
	p := filepath.Join(stateDir, "events.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 1<<20 {
		_ = os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

func baseNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}
