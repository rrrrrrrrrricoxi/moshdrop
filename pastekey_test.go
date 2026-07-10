package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubClipboard 替换剪贴板读取替身，返回还原函数。
func stubClipboard(fn func(dst string) error) func() {
	old := readClipboardPNG
	readClipboardPNG = fn
	return func() { readClipboardPNG = old }
}

// Ctrl+V 有图：孤立 0x16 被吞，图片走拖拽同款上传管线，注入远端路径，临时目录事后清理。
func TestPasteKeyImageUploadsAndInjects(t *testing.T) {
	var mu sync.Mutex
	var dirs []string
	restore := stubClipboard(func(dst string) error {
		mu.Lock()
		dirs = append(dirs, filepath.Dir(dst))
		mu.Unlock()
		return os.WriteFile(dst, []byte("PNG"), 0o600)
	})
	defer restore()
	_, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{stdout: []byte("clip.png\n")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = run
	got := runProxyHarness(t, []byte("\x16"), u,
		func(s string) bool { return strings.Contains(s, "/r/.moshdrop/clip.png") }, 5*time.Second)
	if !strings.Contains(got, "/r/.moshdrop/clip.png") {
		t.Fatalf("应注入远端路径: %q", got)
	}
	if strings.Contains(got, "\x16") {
		t.Fatalf("被吞的 Ctrl+V 绝不能泄漏到远端: %q", got)
	}
	mu.Lock()
	dir := dirs[0]
	mu.Unlock()
	for d := time.Now().Add(2 * time.Second); time.Now().Before(d); {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("剪贴板临时目录未被清理: %s", dir)
}

// Ctrl+V 无图（快速报错）：按键原样透传，绝不触碰 uploader。
func TestPasteKeyNoImageForwardsCtrlV(t *testing.T) {
	restore := stubClipboard(func(string) error { return errors.New("no image") })
	defer restore()
	got := runProxyHarness(t, []byte("\x16"), noopUploader(t),
		func(s string) bool { return strings.Contains(s, "\x16") }, 3*time.Second)
	if got != "\x16" {
		t.Fatalf("无图时 Ctrl+V 应原样透传: %q", got)
	}
}

// 保序：探测挂起期间到达的后续按键，必须排在被放行的 0x16 之后（审计 R2 同款要求）。
func TestPasteKeyProbeHoldPreservesOrder(t *testing.T) {
	block := make(chan struct{})
	restore := stubClipboard(func(string) error { <-block; return errors.New("no image") })
	defer restore()
	got := runProxyHarnessFn(t, noopUploader(t), func(feed func([]byte)) {
		feed([]byte("\x16"))
		time.Sleep(50 * time.Millisecond) // 探测挂起中，后续按键到达
		feed([]byte("abc"))
		time.Sleep(20 * time.Millisecond)
		close(block) // 放行探测：无图 → 0x16 必须先于 abc 冲刷
	}, func(s string) bool { return strings.Contains(s, "abc") }, 5*time.Second)
	if got != "\x16abc" {
		t.Fatalf("扣押期间保序失败: %q (want %q)", got, "\x16abc")
	}
}

// 括号粘贴内的 0x16 是载荷字节：不触发探测，随粘贴原样回放。
func TestPasteKeyInsidePasteNotIntercepted(t *testing.T) {
	var probes atomic.Int32
	restore := stubClipboard(func(string) error { probes.Add(1); return errors.New("no") })
	defer restore()
	want := bpS + "\x16" + bpE
	got := runProxyHarnessFn(t, noopUploader(t), func(feed func([]byte)) {
		feed([]byte(bpS))
		time.Sleep(20 * time.Millisecond)
		feed([]byte("\x16"))
		time.Sleep(20 * time.Millisecond)
		feed([]byte(bpE))
	}, func(s string) bool { return strings.Contains(s, bpE) }, 5*time.Second)
	if got != want {
		t.Fatalf("粘贴中的 0x16 应随粘贴原样回放: %q (want %q)", got, want)
	}
	if probes.Load() != 0 {
		t.Fatal("粘贴中的 0x16 绝不能触发剪贴板探测")
	}
}

// paste_key = off：零探测零拦截，Ctrl+V 是普通字节。
func TestPasteKeyConfigOff(t *testing.T) {
	old := pasteKeyEnabled
	pasteKeyEnabled = false
	defer func() { pasteKeyEnabled = old }()
	var probes atomic.Int32
	restore := stubClipboard(func(string) error { probes.Add(1); return nil })
	defer restore()
	got := runProxyHarness(t, []byte("\x16"), noopUploader(t),
		func(s string) bool { return strings.Contains(s, "\x16") }, 3*time.Second)
	if got != "\x16" || probes.Load() != 0 {
		t.Fatalf("paste_key=off 时 Ctrl+V 必须零探测零拦截: got=%q probes=%d", got, probes.Load())
	}
}

// 探测超时后剪贴板确有图：已承诺拦截，0x16 绝不冲刷，图片最终照常送达。
func TestPasteKeySlowProbeStillDelivers(t *testing.T) {
	oldD := pasteProbeDeadline
	pasteProbeDeadline = 30 * time.Millisecond
	defer func() { pasteProbeDeadline = oldD }()
	restore := stubClipboard(func(dst string) error {
		time.Sleep(90 * time.Millisecond) // 慢于 deadline
		return os.WriteFile(dst, []byte("PNG"), 0o600)
	})
	defer restore()
	_, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{stdout: []byte("slowclip.png\n")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = run
	got := runProxyHarness(t, []byte("\x16"), u,
		func(s string) bool { return strings.Contains(s, "/r/.moshdrop/slowclip.png") }, 5*time.Second)
	if strings.Contains(got, "\x16") {
		t.Fatalf("超时已承诺拦截, 0x16 绝不能再冲刷: %q", got)
	}
	if !strings.Contains(got, "/r/.moshdrop/slowclip.png") {
		t.Fatalf("慢探测最终应完成注入: %q", got)
	}
}

// 探测超时后发现无图：0x16 绝不迟发（远端 CC 会读远端旧剪贴板吃错图），只通知。
func TestPasteKeySlowProbeNoImageNeverForwards(t *testing.T) {
	oldD := pasteProbeDeadline
	pasteProbeDeadline = 30 * time.Millisecond
	defer func() { pasteProbeDeadline = oldD }()
	getNotes, restoreN := captureNotify(t)
	defer restoreN()
	restore := stubClipboard(func(string) error {
		time.Sleep(90 * time.Millisecond)
		return errors.New("no image")
	})
	defer restore()
	got := runProxyHarnessFn(t, noopUploader(t), func(feed func([]byte)) {
		feed([]byte("\x16"))
		time.Sleep(200 * time.Millisecond) // 等后台探测出结果
		feed([]byte("Z"))
	}, func(s string) bool { return strings.Contains(s, "Z") }, 5*time.Second)
	if got != "Z" {
		t.Fatalf("超时后无图: 0x16 绝不迟发, 只应看到后续按键: %q", got)
	}
	if !anyContains(getNotes(), "held back") {
		t.Fatalf("吞掉按键必须通知用户, 实得: %v", getNotes())
	}
}

// clean 闸之 HasPending：块尾滞留半截标记时，孤立 Ctrl+V 并入普通字节流，绝不探测。
func TestPasteKeyPendingNotIntercepted(t *testing.T) {
	var probes atomic.Int32
	restore := stubClipboard(func(string) error { probes.Add(1); return errors.New("no") })
	defer restore()
	got := runProxyHarnessFn(t, noopUploader(t), func(feed func([]byte)) {
		feed([]byte("\x1b[20"))           // bpStart 的真前缀 → pending 滞留
		time.Sleep(15 * time.Millisecond) // <50ms，idle 尚未冲刷
		feed([]byte("\x16"))
	}, func(s string) bool { return strings.Contains(s, "\x16") }, 5*time.Second)
	if got != "\x1b[20\x16" {
		t.Fatalf("pending 期间 0x16 应并入普通字节流保序透传: %q", got)
	}
	if probes.Load() != 0 {
		t.Fatal("pending 未清时绝不能触发剪贴板探测")
	}
}

// clean 闸之 RawPasteOpen：溢出中止后的裸粘贴未闭合期，Ctrl+V 是粘贴中段字节，绝不探测。
func TestPasteKeyRawPasteOpenNotIntercepted(t *testing.T) {
	var probes atomic.Int32
	restore := stubClipboard(func(string) error { probes.Add(1); return errors.New("no") })
	defer restore()
	huge := strings.Repeat("A", maxPaste+4096) // 溢出 → 中止拦截 → rawOpen
	got := runProxyHarnessFn(t, noopUploader(t), func(feed func([]byte)) {
		feed([]byte(bpS))
		feed([]byte(huge))
		time.Sleep(50 * time.Millisecond)
		feed([]byte("\x16"))
	}, func(s string) bool { return strings.HasSuffix(s, "\x16") }, 15*time.Second)
	if probes.Load() != 0 {
		t.Fatal("rawOpen(裸粘贴未闭合)期间绝不能触发剪贴板探测")
	}
	if !strings.HasSuffix(got, "\x16") {
		t.Fatalf("0x16 应作为普通字节透传(尾部): %q", got[max(0, len(got)-8):])
	}
}

// 安全闸：uploader 停用(无 ssh 目标/intercept=off)时，Ctrl+V 零探测零拦截。
func TestPasteKeyDisabledUploaderNotIntercepted(t *testing.T) {
	var probes atomic.Int32
	restore := stubClipboard(func(string) error { probes.Add(1); return nil })
	defer restore()
	u := NewUploader("", nil, t.TempDir()) // 无 ssh 目标 → Disabled
	got := runProxyHarness(t, []byte("\x16"), u,
		func(s string) bool { return strings.Contains(s, "\x16") }, 3*time.Second)
	if got != "\x16" || probes.Load() != 0 {
		t.Fatalf("uploader 停用时 Ctrl+V 必须零探测零拦截: got=%q probes=%d", got, probes.Load())
	}
}

// noReplay 失败语义：慢探测吞键后上传失败——绝不迟发 0x16、绝无注入，弹剪贴板专用文案。
func TestPasteKeySlowProbeUploadFailOnlyNotifies(t *testing.T) {
	oldD := pasteProbeDeadline
	pasteProbeDeadline = 30 * time.Millisecond
	defer func() { pasteProbeDeadline = oldD }()
	getNotes, restoreN := captureNotify(t)
	defer restoreN()
	restore := stubClipboard(func(dst string) error {
		time.Sleep(80 * time.Millisecond)
		return os.WriteFile(dst, []byte("PNG"), 0o600)
	})
	defer restore()
	_, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{err: errFake, stderr: []byte("ssh: connect to host ccc: Connection refused")},
		cmdResult{err: errFake, stderr: []byte("ssh: connect to host ccc: Connection refused")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = run
	got := runProxyHarnessFn(t, u, func(feed func([]byte)) {
		feed([]byte("\x16"))
		time.Sleep(300 * time.Millisecond)
		feed([]byte("Z"))
	}, func(s string) bool { return strings.Contains(s, "Z") }, 5*time.Second)
	if got != "Z" {
		t.Fatalf("剪贴板上传失败: 绝不迟发 0x16、绝无注入，只应看到后续按键: %q", got)
	}
	if !anyContains(getNotes(), "clipboard image upload failed") {
		t.Fatalf("上传失败必须弹剪贴板专用文案，实得: %v", getNotes())
	}
}

// 超限剪贴板图片：弹剪贴板专用文案(不是拖拽的 scp 文案)，无可放行物，只通知。
func TestPasteKeyOversizeNotifiesClipMessage(t *testing.T) {
	getNotes, restoreN := captureNotify(t)
	defer restoreN()
	restore := stubClipboard(func(dst string) error { return os.WriteFile(dst, []byte("0123456789"), 0o600) })
	defer restore()
	u := NewUploader("ccc", nil, t.TempDir())
	u.maxInterceptBytes = 5
	u.run = func(_ context.Context, _ io.Reader, _ []string) cmdResult {
		t.Error("超限剪贴板图片绝不能触发上传")
		return cmdResult{}
	}
	got := runProxyHarnessFn(t, u, func(feed func([]byte)) {
		feed([]byte("\x16"))
		time.Sleep(150 * time.Millisecond)
		feed([]byte("Z"))
	}, func(s string) bool { return strings.Contains(s, "Z") }, 5*time.Second)
	if got != "Z" {
		t.Fatalf("超限剪贴板贴图无可放行物，只通知: %q", got)
	}
	if !anyContains(getNotes(), "clipboard image is") {
		t.Fatalf("超限剪贴板必须弹专用文案，实得: %v", getNotes())
	}
}

// 连按合并：自动重复/连按在管道里合并成 n>1 的全 0x16 块——有图时按一次处理，一个字节都不漏。
func TestPasteKeyBurstCoalescedIntercepts(t *testing.T) {
	restore := stubClipboard(func(dst string) error { return os.WriteFile(dst, []byte("PNG"), 0o600) })
	defer restore()
	_, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{stdout: []byte("burst.png\n")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = run
	got := runProxyHarness(t, []byte("\x16\x16\x16"), u,
		func(s string) bool { return strings.Contains(s, "/r/.moshdrop/burst.png") }, 5*time.Second)
	if strings.Contains(got, "\x16") {
		t.Fatalf("连按合并的 0x16 一个都不能漏给远端(远端 CC 会读旧剪贴板): %q", got)
	}
}

// 连按合并 + 无图：整块原样透传，语义与单按一致。
func TestPasteKeyBurstNoImageForwardsAll(t *testing.T) {
	restore := stubClipboard(func(string) error { return errors.New("no") })
	defer restore()
	got := runProxyHarness(t, []byte("\x16\x16"), noopUploader(t),
		func(s string) bool { return strings.Contains(s, "\x16\x16") }, 3*time.Second)
	if got != "\x16\x16" {
		t.Fatalf("无图连按应原样透传: %q", got)
	}
}

// 队列满兜底：立即清理临时目录 + 通知(文案本地化)。
func TestEnqueueClipboardQueueFull(t *testing.T) {
	getNotes, restoreN := captureNotify(t)
	defer restoreN()
	dir, err := os.MkdirTemp("", "moshdrop-clip-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "x.png")
	if err := os.WriteFile(path, []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &proxyState{drops: make(chan drop)} // 无 worker 且无缓冲 → 必满
	p.enqueueClipboard(path)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("队列满应立即清理临时目录")
	}
	if !anyContains(getNotes(), "too many transfers") {
		t.Fatalf("队列满必须通知，实得: %v", getNotes())
	}
}

// events.log 按来源分账：src 字段可查(退出条款的数据基础)。
func TestLogEventSourceField(t *testing.T) {
	dir := t.TempDir()
	logEvent(dir, dropEvent{Target: "ccc", Source: "clipboard", Files: []string{"a.png"}, Ok: true})
	b, err := os.ReadFile(filepath.Join(dir, "events.log"))
	if err != nil || !strings.Contains(string(b), `"src":"clipboard"`) {
		t.Fatalf("events.log 应带 src 分账字段: %s err=%v", b, err)
	}
}

// 启动清扫：进程中途死亡残留的 >1h 剪贴板临时目录被扫掉，新目录(可能在途)绝不误伤。
func TestSweepStaleClipTemps(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	old, err := os.MkdirTemp("", "moshdrop-clip-")
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	fresh, err := os.MkdirTemp("", "moshdrop-clip-")
	if err != nil {
		t.Fatal(err)
	}
	sweepStaleClipTemps()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超过 1 小时的残留目录应被清扫")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("新目录(可能在途)绝不能误扫")
	}
}

// 敏感截图落盘权限：文件必须收紧到 0600(AppleScript 默认写出 0644)。
func TestClipboardTempPNGPerms(t *testing.T) {
	restore := stubClipboard(func(dst string) error { return os.WriteFile(dst, []byte("PNG"), 0o644) })
	defer restore()
	p, err := clipboardToTempPNG()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(p))
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("剪贴板 PNG 应收紧为 0600: %v", fi.Mode().Perm())
	}
}
