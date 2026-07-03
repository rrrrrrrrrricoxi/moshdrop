package main

import (
	"os"
	"strings"
	"time"
)

// parsePasteSyntax 纯语法判定（零 IO，可在输入热路径安全调用）：
// 载荷是否形如 1..N 个绝对路径。含未转义换行/回车直接判否——
// 换行分隔的文本列表不是拖拽（终端拖拽用空格分隔），不得劫持改写。
func parsePasteSyntax(payload string) ([]string, bool) {
	esc := false
	for _, r := range payload {
		if esc {
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		if r == '\n' || r == '\r' {
			return nil, false
		}
	}
	tokens := splitUnescaped(payload)
	if len(tokens) == 0 {
		return nil, false
	}
	paths := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		p := unescape(tok)
		if !strings.HasPrefix(p, "/") {
			return nil, false
		}
		paths = append(paths, p)
	}
	return paths, true
}

// verifyLocalFiles 带硬超时的 stat 验真（在异步 goroutine 里调用，绝不阻塞输入热路径：
// 半死的网络挂载点上 stat 可挂几十秒，超时即放弃拦截）。返回总字节数。
func verifyLocalFiles(paths []string, timeout time.Duration) (int64, bool) {
	type result struct {
		total int64
		ok    bool
	}
	ch := make(chan result, 1)
	go func() {
		var total int64
		for _, p := range paths {
			fi, err := os.Stat(p)
			if err != nil || !fi.Mode().IsRegular() {
				ch <- result{0, false}
				return
			}
			total += fi.Size()
		}
		ch <- result{total, true}
	}()
	select {
	case r := <-ch:
		return r.total, r.ok
	case <-time.After(timeout):
		return 0, false // stat 挂死（网络卷）：放弃拦截，原样放行
	}
}

// ParseDraggedPaste 组合判定（语法 + 500ms 验真），供测试与简单调用方使用。
func ParseDraggedPaste(payload string) ([]string, bool) {
	paths, ok := parsePasteSyntax(payload)
	if !ok {
		return nil, false
	}
	if _, ok := verifyLocalFiles(paths, 500*time.Millisecond); !ok {
		return nil, false
	}
	return paths, true
}

// splitUnescaped 按"未被反斜杠转义的空格/制表符"切分，token 保留原始转义。
func splitUnescaped(s string) []string {
	var out []string
	var cur strings.Builder
	esc := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if esc {
			cur.WriteRune('\\')
			cur.WriteRune(r)
			esc = false
			continue
		}
		switch r {
		case '\\':
			esc = true
		case ' ', '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if esc {
		cur.WriteRune('\\')
	}
	flush()
	return out
}

// unescape 去掉反斜杠转义：`\<c>` → `<c>`。
func unescape(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		if esc {
			b.WriteRune(r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		b.WriteRune(r)
	}
	if esc {
		b.WriteRune('\\')
	}
	return b.String()
}

// FormatInjection 按终端粘贴风格转义远端路径（空格等加反斜杠），空格连接。
func FormatInjection(remotePaths []string) string {
	esc := make([]string, len(remotePaths))
	for i, p := range remotePaths {
		esc[i] = escapePath(p)
	}
	return strings.Join(esc, " ")
}

func escapePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		safe := r == '/' || r == '.' || r == '-' || r == '_' || r == '~' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r > 127 // 中文等多字节保持原样
		if !safe {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
