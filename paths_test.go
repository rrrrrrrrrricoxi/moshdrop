package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDraggedPaste(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "shot.png")
	spaced := filepath.Join(dir, "Screenshot 2026-07-03 at 11.00.00.png")
	cjk := filepath.Join(dir, "文档 汇总.xlsx")
	for _, p := range []string{plain, spaced, cjk} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	esc := func(s string) string { // 模拟 Ghostty 的空格转义
		out := ""
		for _, r := range s {
			if r == ' ' {
				out += "\\"
			}
			out += string(r)
		}
		return out
	}

	t.Run("单文件", func(t *testing.T) {
		got, ok := ParseDraggedPaste(plain)
		if !ok || len(got) != 1 || got[0] != plain {
			t.Fatalf("got %v %v", got, ok)
		}
	})
	t.Run("转义空格+中文", func(t *testing.T) {
		got, ok := ParseDraggedPaste(esc(spaced) + " " + esc(cjk))
		if !ok || len(got) != 2 || got[0] != spaced || got[1] != cjk {
			t.Fatalf("got %v %v", got, ok)
		}
	})
	t.Run("文件不存在则拒绝", func(t *testing.T) {
		if _, ok := ParseDraggedPaste(plain + " /no/such/file.png"); ok {
			t.Fatal("应整体拒绝")
		}
	})
	t.Run("目录拒绝", func(t *testing.T) {
		if _, ok := ParseDraggedPaste(dir); ok {
			t.Fatal("目录不是普通文件")
		}
	})
	t.Run("相对路径拒绝", func(t *testing.T) {
		if _, ok := ParseDraggedPaste("shot.png"); ok {
			t.Fatal("非绝对路径")
		}
	})
	t.Run("普通文字拒绝", func(t *testing.T) {
		if _, ok := ParseDraggedPaste("hello world"); ok {
			t.Fatal("聊天文本不能拦")
		}
	})
	t.Run("空载荷拒绝", func(t *testing.T) {
		if _, ok := ParseDraggedPaste("  "); ok {
			t.Fatal("空白不能拦")
		}
	})
	t.Run("换行分隔列表拒绝(审计D1回归)", func(t *testing.T) {
		if _, ok := ParseDraggedPaste(plain + "\n" + esc(cjk)); ok {
			t.Fatal("换行分隔的文本列表不是拖拽，不得劫持改写")
		}
	})
	t.Run("含回车拒绝", func(t *testing.T) {
		if _, ok := ParseDraggedPaste(plain + "\r"); ok {
			t.Fatal("含回车的载荷不是拖拽")
		}
	})
}

func TestParsePasteSyntax(t *testing.T) {
	if paths, ok := parsePasteSyntax(`/a/b\ c.png /d.txt`); !ok || len(paths) != 2 ||
		paths[0] != "/a/b c.png" || paths[1] != "/d.txt" {
		t.Fatalf("语法解析失败: %v %v", paths, ok)
	}
	if _, ok := parsePasteSyntax("hello world"); ok {
		t.Fatal("非路径文本必须判否")
	}
	if _, ok := parsePasteSyntax("/a\n/b"); ok {
		t.Fatal("未转义换行必须判否")
	}
}

func TestFormatInjection(t *testing.T) {
	got := FormatInjection([]string{"/r/a b.png", "/r/文档.xlsx"})
	want := `/r/a\ b.png /r/文档.xlsx`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
