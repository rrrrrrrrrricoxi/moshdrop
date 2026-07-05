package main

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureNotify 用捕获替身替换全局 Notify，返回收集到的消息与还原函数。
func captureNotify(t *testing.T) (get func() []string, restore func()) {
	t.Helper()
	var mu sync.Mutex
	var msgs []string
	old := Notify
	Notify = func(_, m string) { mu.Lock(); msgs = append(msgs, m); mu.Unlock() }
	// 断言依赖英文文案：固定语言，杜绝跨测试的 curLang 串扰。
	oldLang := curLang
	curLang = "en"
	return func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), msgs...)
		}, func() {
			Notify = old
			curLang = oldLang
		}
}

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// 大文件护栏：超过大小上限的拖拽绝不触发上传，原样放行本地路径（保留括号语义）。
func TestProxyOversizeCeilingPassesThrough(t *testing.T) {
	f := tmpFile(t, "huge.png", "0123456789") // 10 字节
	u := NewUploader("ccc", nil, t.TempDir())
	u.maxInterceptBytes = 5 // 上限 5 字节 → 10 字节文件超限
	u.run = func(_ context.Context, _ io.Reader, _ []string) cmdResult {
		t.Error("超过大小上限的文件绝不能触发上传")
		return cmdResult{}
	}
	getNotes, restore := captureNotify(t)
	defer restore()
	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	got := runProxyHarness(t, []byte(in), u, func(s string) bool { return len(s) >= len(in) }, 3*time.Second)
	if got != in {
		t.Fatalf("超限文件必须原样放行（保留括号标记）:\n got %q\nwant %q", got, in)
	}
	if !anyContains(getNotes(), "over the") {
		t.Fatalf("超限拖拽必须弹「超过上限」告知用户手动 scp，实得通知: %v", getNotes())
	}
}

// 并发安全：msg() 绝不能写全局 curLang（迟到通知的 AfterFunc 会与 Upload 内的 msg 并发）。
// 未配置 lang（curLang==""）时，旧的惰性写实现会在 -race 下报 data race。
func TestMsgConcurrentNoRace(t *testing.T) {
	oldLang := curLang
	curLang = "" // 模拟无 lang 配置的默认态
	defer func() { curLang = oldLang }()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = msg("n.uploading")
			_ = msg("n.toobig")
		}()
	}
	wg.Wait()
}

// 边界：文件大小恰等于上限（未超过）应照常上传——守住 `>` 不被改成 `>=`。
func TestProxyCeilingExactBoundaryUploads(t *testing.T) {
	f := tmpFile(t, "atlimit.png", "01234") // 恰 5 字节
	_, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{stdout: []byte("atlimit.png\n")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.maxInterceptBytes = 5 // 上限 = 文件大小 → 不超过，应上传
	u.run = run
	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	got := runProxyHarness(t, []byte(in), u,
		func(s string) bool { return strings.Contains(s, "/r/.moshdrop/atlimit.png") }, 5*time.Second)
	if !strings.Contains(got, "/r/.moshdrop/atlimit.png") {
		t.Fatalf("大小恰等于上限应上传（`>` 不得为 `>=`）: %q", got)
	}
}

// 端到端单位换算：MaxInterceptMB(MB) 必须经 ApplyConfig 换成字节（守住 << 20）。
func TestApplyConfigMaxInterceptMBToBytes(t *testing.T) {
	u := NewUploader("ccc", nil, t.TempDir())
	u.ApplyConfig(Config{RemoteDir: ".moshdrop", MaxInterceptMB: 50})
	if u.maxInterceptBytes != 50<<20 {
		t.Fatalf("MaxInterceptMB=50 应为 %d 字节，实得 %d", 50<<20, u.maxInterceptBytes)
	}
	u2 := NewUploader("ccc", nil, t.TempDir())
	u2.ApplyConfig(LoadConfig(t.TempDir(), "ccc")) // 默认 50MB 走完整链路
	if u2.maxInterceptBytes != 50<<20 {
		t.Fatalf("默认 50MB 未端到端换算成字节: %d", u2.maxInterceptBytes)
	}
	u3 := NewUploader("ccc", nil, t.TempDir())
	u3.ApplyConfig(Config{RemoteDir: ".moshdrop", MaxInterceptMB: 0})
	if u3.maxInterceptBytes != 0 {
		t.Fatalf("MaxInterceptMB=0 应为 0（不限制），实得 %d", u3.maxInterceptBytes)
	}
}

// 迟到反馈 + 上传失败：横幅已弹后上传仍失败，绝不能谎报「已送达」。
// 覆盖 announced=true ∧ err!=nil 这一格（另三格已有测试）。
func TestProxySlowUploadThenFailNeverDelivers(t *testing.T) {
	old := slowNotifyDelay
	slowNotifyDelay = 20 * time.Millisecond
	defer func() { slowNotifyDelay = old }()

	getNotes, restore := captureNotify(t)
	defer restore()

	f := tmpFile(t, "slowfail.png", "X")
	u := NewUploader("ccc", nil, t.TempDir())
	var calls int
	var cmu sync.Mutex
	u.run = func(_ context.Context, _ io.Reader, _ []string) cmdResult {
		cmu.Lock()
		n := calls
		calls++
		cmu.Unlock()
		if n == 0 {
			return cmdResult{stdout: []byte("/r/.moshdrop")} // ensure 成功
		}
		time.Sleep(80 * time.Millisecond) // 慢于 slowNotifyDelay → 横幅先弹
		return cmdResult{err: errFake, stderr: []byte("ssh: connect to host ccc: Connection refused")}
	}
	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	got := runProxyHarness(t, []byte(in), u,
		func(s string) bool { return strings.Contains(s, bpS) && strings.Contains(s, bpE) }, 5*time.Second)
	if got != in { // fail-open：原样回放本地路径（保留括号标记）
		t.Fatalf("慢传失败必须 fail-open 原样回放: %q", got)
	}
	notes := getNotes()
	if !anyContains(notes, "uploading") {
		t.Fatalf("慢传应按时间弹「上传中」(证明 announced=true)，实得: %v", notes)
	}
	if anyContains(notes, "delivered") {
		t.Fatalf("上传失败绝不能谎报「已送达」，实得: %v", notes)
	}
}

// 上限=0（默认未配置）表示不限制：大文件照常上传。
func TestProxyCeilingZeroMeansUnlimited(t *testing.T) {
	f := tmpFile(t, "big.png", "0123456789")
	_, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{stdout: []byte("big.png\n")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.maxInterceptBytes = 0 // 不限制
	u.run = run
	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	got := runProxyHarness(t, []byte(in), u,
		func(s string) bool { return strings.Contains(s, "/r/.moshdrop/big.png") }, 5*time.Second)
	if !strings.Contains(got, "/r/.moshdrop/big.png") {
		t.Fatalf("上限=0 应不限制、照常上传: %q", got)
	}
}

// 迟到反馈：上传慢于 slowNotifyDelay 时，「上传中」按时间触发（与文件大小无关），
// 成功后补「已送达」。这是治「弱网上传数十秒毫无反应」根因的核心行为。
func TestProxySlowUploadNotifiesByTime(t *testing.T) {
	old := slowNotifyDelay
	slowNotifyDelay = 20 * time.Millisecond
	defer func() { slowNotifyDelay = old }()

	getNotes, restore := captureNotify(t)
	defer restore()

	f := tmpFile(t, "slow.png", "X") // 1 字节，远小于旧的 8MiB 阈值
	u := NewUploader("ccc", nil, t.TempDir())
	var calls int
	var cmu sync.Mutex
	u.run = func(_ context.Context, _ io.Reader, _ []string) cmdResult {
		cmu.Lock()
		n := calls
		calls++
		cmu.Unlock()
		if n == 0 {
			return cmdResult{stdout: []byte("/r/.moshdrop")} // ensure
		}
		time.Sleep(80 * time.Millisecond) // 上传慢于 slowNotifyDelay
		return cmdResult{stdout: []byte("slow.png\n")}
	}
	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	got := runProxyHarness(t, []byte(in), u,
		func(s string) bool { return strings.Contains(s, "/r/.moshdrop/slow.png") }, 5*time.Second)
	if !strings.Contains(got, "/r/.moshdrop/slow.png") {
		t.Fatalf("慢上传仍应最终注入远端路径: %q", got)
	}
	notes := getNotes()
	if !anyContains(notes, "uploading") {
		t.Fatalf("慢上传应按时间弹「上传中」，实得通知: %v", notes)
	}
	if !anyContains(notes, "delivered") {
		t.Fatalf("弹过「上传中」后成功应补「已送达」，实得通知: %v", notes)
	}
}

// 快上传静默：上传快于 slowNotifyDelay 时不弹任何横幅（不制造噪音）。
func TestProxyFastUploadIsSilent(t *testing.T) {
	old := slowNotifyDelay
	slowNotifyDelay = 2 * time.Second
	defer func() { slowNotifyDelay = old }()

	getNotes, restore := captureNotify(t)
	defer restore()

	f := tmpFile(t, "fast.png", "X")
	_, run := fakeRunner(
		cmdResult{stdout: []byte("/r/.moshdrop")},
		cmdResult{stdout: []byte("fast.png\n")},
	)
	u := NewUploader("ccc", nil, t.TempDir())
	u.run = run
	in := bpS + strings.ReplaceAll(f, " ", `\ `) + bpE
	runProxyHarness(t, []byte(in), u,
		func(s string) bool { return strings.Contains(s, "/r/.moshdrop/fast.png") }, 5*time.Second)
	notes := getNotes()
	if anyContains(notes, "uploading") || anyContains(notes, "delivered") {
		t.Fatalf("快上传不应弹任何横幅，实得通知: %v", notes)
	}
}
