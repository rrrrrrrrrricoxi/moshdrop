package main

import (
	"bytes"
	"math/rand"
	"testing"
	"time"
)

const bpS = "\x1b[200~"
const bpE = "\x1b[201~"

// collect 把事件流折叠成 (转发字节, 粘贴载荷列表)
func collect(evs []Event) ([]byte, [][]byte) {
	var fwd []byte
	var pastes [][]byte
	for _, e := range evs {
		if e.Type == EvForward {
			fwd = append(fwd, e.Data...)
		} else {
			pastes = append(pastes, e.Data)
		}
	}
	return fwd, pastes
}

func TestScannerPlainPassthrough(t *testing.T) {
	var s Scanner
	in := []byte("ls -la\rvim foo\x1b[A\x03")
	fwd, pastes := collect(s.Feed(in))
	fwd2, _ := collect(s.Idle(time.Second))
	fwd = append(fwd, fwd2...)
	if !bytes.Equal(fwd, in) || len(pastes) != 0 {
		t.Fatalf("普通输入必须逐字节透传, got %q", fwd)
	}
}

func TestScannerWholePaste(t *testing.T) {
	var s Scanner
	fwd, pastes := collect(s.Feed([]byte("A" + bpS + "/tmp/x.png" + bpE + "B")))
	if string(fwd) != "AB" || len(pastes) != 1 || string(pastes[0]) != "/tmp/x.png" {
		t.Fatalf("got fwd=%q pastes=%q", fwd, pastes)
	}
}

// 关键：起止标记在任意字节边界被切碎，结果必须与整块喂入完全一致
func TestScannerSplitMarkers(t *testing.T) {
	msg := []byte("hi" + bpS + "/tmp/a b.png" + bpE + "yo")
	for cut1 := 1; cut1 < len(msg)-1; cut1++ {
		for _, cut2 := range []int{cut1 + 1, len(msg) - 1} {
			if cut2 <= cut1 || cut2 >= len(msg) {
				continue
			}
			var s Scanner
			var evs []Event
			evs = append(evs, s.Feed(msg[:cut1])...)
			evs = append(evs, s.Feed(msg[cut1:cut2])...)
			evs = append(evs, s.Feed(msg[cut2:])...)
			evs = append(evs, s.Idle(time.Second)...)
			fwd, pastes := collect(evs)
			if string(fwd) != "hiyo" || len(pastes) != 1 || string(pastes[0]) != "/tmp/a b.png" {
				t.Fatalf("cut(%d,%d): fwd=%q pastes=%q", cut1, cut2, fwd, pastes)
			}
		}
	}
}

// 半截 ESC[20 停在末尾：Idle 后必须原样吐出（用户真的敲了 ESC 序列前缀）
func TestScannerPartialMarkerFlush(t *testing.T) {
	var s Scanner
	fwd1, _ := collect(s.Feed([]byte("x\x1b[20")))
	fwd2, _ := collect(s.Idle(60 * time.Millisecond))
	got := string(fwd1) + string(fwd2)
	if got != "x\x1b[20" {
		t.Fatalf("got %q", got)
	}
}

// 粘贴永不闭合：2s 后必须中止并把 ESC[200~+已收内容原样放行
func TestScannerPasteAbort(t *testing.T) {
	var s Scanner
	s.Feed([]byte(bpS + "half"))
	fwd, pastes := collect(s.Idle(3 * time.Second))
	if len(pastes) != 0 || string(fwd) != bpS+"half" {
		t.Fatalf("got fwd=%q pastes=%q", fwd, pastes)
	}
	if s.InPaste() {
		t.Fatal("中止后应退出粘贴态")
	}
}

// 模糊测试：随机字节随机切块（不含标记），透传必须逐字节无损
func TestScannerFuzzTransparency(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 200; iter++ {
		raw := make([]byte, 512)
		rng.Read(raw)
		clean := bytes.ReplaceAll(raw, []byte("\x1b[200~"), []byte("......"))
		clean = bytes.ReplaceAll(clean, []byte("\x1b[201~"), []byte("......"))
		var s Scanner
		var fwd []byte
		for i := 0; i < len(clean); {
			n := 1 + rng.Intn(64)
			if i+n > len(clean) {
				n = len(clean) - i
			}
			f, _ := collect(s.Feed(clean[i : i+n]))
			fwd = append(fwd, f...)
			i += n
		}
		f, _ := collect(s.Idle(time.Second))
		fwd = append(fwd, f...)
		if !bytes.Equal(fwd, clean) {
			t.Fatalf("iter %d: 透传失真", iter)
		}
	}
}
