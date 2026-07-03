package main

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TEMP verification: lone ESC in scanner pending, then within 50ms a bare-path
// chunk that fails parsePasteSyntax → replay writes before pending ESC flushes.
func runProxyHarnessMulti(t *testing.T, writes [][]byte, gap time.Duration, up *Uploader, pred func(string) bool, timeout time.Duration) string {
	t.Helper()
	t.Setenv("MOSHDROP_STATE_DIR", t.TempDir())
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	var mu sync.Mutex
	var buf bytes.Buffer
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, err := outR.Read(tmp)
			if n > 0 {
				mu.Lock()
				buf.Write(tmp[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() { RunProxy(rawCat(), up); close(done) }()

	start := -1
	for d := time.Now().Add(3 * time.Second); time.Now().Before(d); {
		mu.Lock()
		if i := strings.Index(buf.String(), rdy); i >= 0 {
			start = i + len(rdy)
		}
		mu.Unlock()
		if start >= 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if start < 0 {
		t.Fatal("替身未就绪")
	}
	for i, w := range writes {
		if i > 0 {
			time.Sleep(gap)
		}
		inW.Write(w)
	}

	out := func() string {
		mu.Lock()
		defer mu.Unlock()
		s := buf.String()
		if len(s) < start {
			return ""
		}
		return s[start:]
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred(out()) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunProxy 未随 stdin 关闭而退出")
	}
	outW.Close()
	time.Sleep(50 * time.Millisecond)
	return out()
}

// Case A: uploader enabled, chunk fails parsePasteSyntax ("bar" not absolute).
func TestZZEscThenBarePathParseFail(t *testing.T) {
	want := "\x1b/foo bar baz quux"
	got := runProxyHarnessMulti(t, [][]byte{{0x1b}, []byte("/foo bar baz quux")}, 20*time.Millisecond,
		noopUploader(t), func(s string) bool { return len(s) >= len(want) }, 3*time.Second)
	if got != want {
		t.Logf("REORDERED: got %q want %q", got, want)
		t.Fail()
	}
}

// Case B: uploader Disabled (claimed pure passthrough).
func TestZZEscThenBarePathDisabled(t *testing.T) {
	up := NewUploader("", nil, t.TempDir())
	want := "\x1b/foo bar baz quux"
	got := runProxyHarnessMulti(t, [][]byte{{0x1b}, []byte("/foo bar baz quux")}, 20*time.Millisecond,
		up, func(s string) bool { return len(s) >= len(want) }, 3*time.Second)
	if got != want {
		t.Logf("REORDERED (disabled): got %q want %q", got, want)
		t.Fail()
	}
}

// Case C: syntax parses but verify fails (nonexistent paths) → injectWhenClean path.
func TestZZEscThenBarePathVerifyFail(t *testing.T) {
	want := "\x1b/no/such/aaa /no/such/bbb"
	got := runProxyHarnessMulti(t, [][]byte{{0x1b}, []byte("/no/such/aaa /no/such/bbb")}, 20*time.Millisecond,
		noopUploader(t), func(s string) bool { return len(s) >= len(want) }, 3*time.Second)
	if got != want {
		t.Logf("ORDER (verify-fail path): got %q want %q", got, want)
		t.Fail()
	}
}
