package main

import (
	"os"
	"strings"
)

// ParseDraggedPaste 判定载荷是否恰为 1..N 个被拖入的本地文件。
// 仅当每个 token 还原转义后都是"绝对路径 + 本地存在的普通文件"才返回 true；
// 否则调用方必须原样放行。
func ParseDraggedPaste(payload string) ([]string, bool) {
	tokens := splitUnescaped(payload)
	if len(tokens) == 0 {
		return nil, false
	}
	paths := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		p := unescape(tok)
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\n\r") {
			return nil, false
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, false
		}
		paths = append(paths, p)
	}
	return paths, true
}

// splitUnescaped 按"未被反斜杠转义的空白"切分，token 保留原始转义。
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
		case ' ', '\t', '\r', '\n':
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
