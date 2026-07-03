package main

import (
	"testing"
	"time"
)

// TEMP: verify same-chunk ordering of non-intercepted paste + trailing bytes.
func TestZZReorderRepro(t *testing.T) {
	in := bpS + "just some words" + bpE + "XYZ"
	got := runProxyHarness(t, []byte(in), noopUploader(t), func(s string) bool { return len(s) >= len(in) }, 3*time.Second)
	if got != in {
		t.Fatalf("reordered:\n got %q\nwant %q", got, in)
	}
}
