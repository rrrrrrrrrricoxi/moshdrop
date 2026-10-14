package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// No PTY, tmux, SSH, notification center, permissions, or system clipboard.
// An ordinary temporary file captures precisely what insertion would write.
func deliveryFixture(t *testing.T, delay time.Duration, fail bool, count int) (*proxyState, *os.File, []string, *[]PendingResult) {
	t.Helper()
	state := t.TempDir()
	terminal, err := os.CreateTemp(t.TempDir(), "terminal-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminal.Close() })
	paths := make([]string, count)
	for i := range paths {
		paths[i] = tmpFile(t, string(rune('a'+i))+" shot.png", "data")
	}
	u := NewUploader("test-host", nil, state)
	calls := 0
	u.run = func(_ context.Context, _ io.Reader, _ []string) cmdResult {
		calls++
		if calls == 1 {
			return cmdResult{stdout: []byte("/remote/.moshdrop")}
		}
		time.Sleep(delay)
		if fail {
			return cmdResult{err: errors.New("disk full"), stderr: []byte("No space left on device")}
		}
		return cmdResult{stdout: []byte(filepath.Base(paths[calls-2]) + "\n")}
	}
	saved := &[]PendingResult{}
	oldNotify, oldNative := Notify, notifyPending
	Notify = func(string, string) {}
	notifyPending = func(_ string, r PendingResult) error { *saved = append(*saved, r); return nil }
	t.Cleanup(func() { Notify = oldNotify; notifyPending = oldNative })
	p := &proxyState{ptmx: terminal, up: u, stateDir: state}
	return p, terminal, paths, saved
}

func terminalContents(t *testing.T, f *os.File) string {
	t.Helper()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDeliveryQuickAndLateGrouped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		age    time.Duration
		window time.Duration
		late   bool
	}{
		{"quick", 0, 5 * time.Second, false},
		{"already_queued_over_five_seconds", 6 * time.Second, 5 * time.Second, true},
		{"cutoff_disabled", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, f, paths, offered := deliveryFixture(t, 0, false, 2)
			p.up.autoInsertWindow = tc.window
			beforeCopy := copyToClipboard
			copyToClipboard = func(string) error { t.Fatal("delivery must not copy automatically"); return nil }
			t.Cleanup(func() { copyToClipboard = beforeCopy })
			d := drop{payload: FormatInjection(paths), tokens: paths, bracketed: true, accepted: time.Now().Add(-tc.age)}
			p.processDrop(d)
			expected := FormatInjection([]string{"/remote/.moshdrop/a shot.png", "/remote/.moshdrop/b shot.png"})
			results, err := listPending(p.stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.late {
				if got := terminalContents(t, f); got != "" {
					t.Fatalf("late bytes: %q", got)
				}
				if len(results) != 1 || len(*offered) != 1 || results[0].Clipboard != expected || len(results[0].Paths) != 2 {
					t.Fatalf("group not preserved: %+v / %+v", results, *offered)
				}
			} else {
				if got := terminalContents(t, f); got != bpS+expected+bpE {
					t.Fatalf("quick bytes: %q", got)
				}
				if len(results) != 0 || len(*offered) != 0 {
					t.Fatalf("quick delivery should not create history: %+v", results)
				}
			}
		})
	}
}

func TestDeliveryBecomesLateDuringTransfer(t *testing.T) {
	p, f, paths, offered := deliveryFixture(t, 40*time.Millisecond, false, 1)
	p.up.autoInsertWindow = 15 * time.Millisecond
	p.processDrop(drop{payload: FormatInjection(paths), tokens: paths, accepted: time.Now()})
	if terminalContents(t, f) != "" || len(*offered) != 1 {
		t.Fatal("transfer crossed cutoff but was inserted")
	}
}

func TestDeliveryBecomesLateWaitingForCleanInput(t *testing.T) {
	p, f, _, offered := deliveryFixture(t, 0, false, 1)
	p.sc.Feed([]byte(bpS + "unfinished paste"))
	p.up.autoInsertWindow = 25 * time.Millisecond
	d := drop{accepted: time.Now(), bracketed: true}
	start := time.Now()
	if p.insertDrop(d, "/remote/path") {
		t.Fatal("forced insertion past cutoff")
	}
	if terminalContents(t, f) != "" || time.Since(start) > 250*time.Millisecond || len(*offered) != 0 {
		t.Fatal("dirty input did not stop delivery at the cutoff")
	}
}

func TestDeliveryFailuresNeverReplayWhenLate(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "quick", true: "late"}[late], func(t *testing.T) {
			p, f, paths, offered := deliveryFixture(t, 0, true, 1)
			accepted := time.Now()
			if late {
				accepted = accepted.Add(-6 * time.Second)
			}
			payload := FormatInjection(paths)
			p.processDrop(drop{payload: payload, tokens: paths, bracketed: true, accepted: accepted})
			if late {
				results, err := listPending(p.stateDir)
				if err != nil {
					t.Fatal(err)
				}
				if terminalContents(t, f) != "" || len(results) != 1 || results[0].OK || results[0].Clipboard != "" || results[0].Error == "" || len(*offered) != 0 {
					t.Fatalf("late failure mishandled: %+v", results)
				}
			} else if terminalContents(t, f) != bpS+payload+bpE {
				t.Fatal("quick fail-open changed")
			}
		})
	}
}

func TestDeliveryLateClipboardPreservesClipboardAndCleansImage(t *testing.T) {
	p, f, _, offered := deliveryFixture(t, 0, false, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "image.png")
	os.WriteFile(path, []byte("image"), 0600)
	// This runner names the temporary clipboard image exactly as the real upload would.
	_, p.up.run = fakeRunner(cmdResult{stdout: []byte("/remote/.moshdrop")}, cmdResult{stdout: []byte("image.png\n")})
	oldCopy := copyToClipboard
	copyToClipboard = func(string) error { t.Fatal("late clipboard upload overwrote the clipboard"); return nil }
	defer func() { copyToClipboard = oldCopy }()
	p.processDrop(drop{tokens: []string{path}, noReplay: true, cleanup: dir, source: "clipboard", accepted: time.Now().Add(-6 * time.Second)})
	if terminalContents(t, f) != "" || len(*offered) != 1 || (*offered)[0].Source != "clipboard" {
		t.Fatal("clipboard delivery was not held")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("temporary image not cleaned")
	}
}

func TestDeliveryClipboardProbeTimeCountsTowardCutoff(t *testing.T) {
	p, f, _, offered := deliveryFixture(t, 0, false, 1)
	p.drops = make(chan drop, 1)
	p.up.autoInsertWindow = 10 * time.Millisecond
	oldRead := readClipboardPNG
	readClipboardPNG = func(path string) error {
		time.Sleep(35 * time.Millisecond)
		return os.WriteFile(path, []byte("image"), 0600)
	}
	defer func() { readClipboardPNG = oldRead }()
	if !p.interceptPasteKey() {
		t.Fatal("image action not intercepted")
	}
	d := <-p.drops
	if time.Since(d.accepted) < 30*time.Millisecond {
		t.Fatal("probe time was discarded")
	}
	p.processDrop(d)
	if terminalContents(t, f) != "" || len(*offered) != 1 {
		t.Fatal("slow probe was inserted automatically")
	}
}

func TestDeliveryMissingHelperFallsBackWithoutInsertion(t *testing.T) {
	p, f, paths, _ := deliveryFixture(t, 0, false, 1)
	var notices []string
	Notify = func(_ string, body string) { notices = append(notices, body) }
	notifyPending = func(string, PendingResult) error { return errors.New("helper unavailable or permission denied") }
	p.processDrop(drop{tokens: paths, accepted: time.Now().Add(-6 * time.Second)})
	results, err := listPending(p.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if terminalContents(t, f) != "" || len(results) != 1 || len(notices) != 1 || !strings.Contains(notices[0], "moshdrop pending --copy "+results[0].ID) {
		t.Fatalf("unrecoverable fallback: %v", notices)
	}
}

func TestDeliveryCacheFailureNeverInserts(t *testing.T) {
	p, f, paths, offered := deliveryFixture(t, 0, false, 1)
	os.WriteFile(filepath.Join(p.stateDir, "pending"), []byte("not a directory"), 0600)
	var notice string
	Notify = func(_ string, body string) { notice = body }
	p.processDrop(drop{tokens: paths, accepted: time.Now().Add(-6 * time.Second)})
	if terminalContents(t, f) != "" || len(*offered) != 0 || notice == "" {
		t.Fatal("failed storage caused an unsafe fallback")
	}
}

func TestDeliveryAcceptanceTimestamps(t *testing.T) {
	p, _, paths, _ := deliveryFixture(t, 0, false, 1)
	p.drops = make(chan drop, 1)
	before := time.Now().Add(-6 * time.Second)
	p.enqueueClipboardAt(paths[0], before)
	d := <-p.drops
	if !d.accepted.Equal(before) || !d.noReplay {
		t.Fatal("clipboard probe time was discarded")
	}
	p.enqueue([]drop{{tokens: paths, accepted: before}})
	if d := <-p.drops; !d.accepted.Equal(before) {
		t.Fatal("queueing reset acceptance time")
	}
}

func TestDeliveryFullQueueDoesNotReplayLateInput(t *testing.T) {
	p, f, paths, _ := deliveryFixture(t, 0, false, 1)
	p.drops = make(chan drop, 1)
	p.drops <- drop{} // Occupy the slot without starting a worker.
	done := make(chan struct{})
	Notify = func(string, string) { close(done) }
	start := time.Now()
	p.enqueue([]drop{{payload: FormatInjection(paths), tokens: paths, bracketed: true, accepted: time.Now().Add(-6 * time.Second)}})
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("late overflow blocked input with storage/notification IO")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("overflow failure not recorded")
	}
	results, err := listPending(p.stateDir)
	if err != nil || len(results) != 1 || results[0].OK || terminalContents(t, f) != "" {
		t.Fatalf("late overflow replayed: %+v %v", results, err)
	}
}

func TestRunPasteReportsCopyFailure(t *testing.T) {
	t.Setenv("MOSHDROP_STATE_DIR", t.TempDir())
	t.Setenv("MOSHDROP_MUTE_NOTIFY", "1")
	oldRead, oldCopy, oldRun := readClipboardPNG, copyToClipboard, realRunHook
	defer func() { readClipboardPNG = oldRead; copyToClipboard = oldCopy; realRunHook = oldRun }()
	readClipboardPNG = func(path string) error { return os.WriteFile(path, []byte("image"), 0600) }
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "pbcopy"), []byte("#!/bin/sh\n/bin/cat >/dev/null\nexit 23\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	copyToClipboard = realCopyToClipboard // Real wrapper, harmless subprocess fixture.
	calls := 0
	realRunHook = func(_ context.Context, _ io.Reader, argv []string) (cmdResult, bool) {
		calls++
		if calls == 1 {
			return cmdResult{stdout: []byte("/remote/.moshdrop")}, true
		}
		if strings.Contains(argv[len(argv)-1], "cat >") {
			return cmdResult{stdout: []byte("image.png\n")}, true
		}
		return cmdResult{}, true
	}
	stdout, stderr, code := captureCommand(t, func() int { return runPaste([]string{"test-host"}) })
	if code != 1 || !strings.Contains(stdout, "/remote/.moshdrop/image.png") || strings.Contains(stdout, msg("p.ok")) || !strings.Contains(stderr, "exit status 23") {
		t.Fatalf("misreported copy: code=%d out=%q err=%q", code, stdout, stderr)
	}
}

func captureCommand(t *testing.T, fn func() int) (string, string, int) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	errs, err := os.CreateTemp(t.TempDir(), "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	defer errs.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errs
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	code := fn()
	out.Seek(0, 0)
	errs.Seek(0, 0)
	var o, e bytes.Buffer
	io.Copy(&o, out)
	io.Copy(&e, errs)
	return o.String(), e.String(), code
}

// Feed stdin-sized chunks and capture writes without a terminal or remote shell.
func feedDeliveryBytes(p *proxyState, data string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for offset := 0; offset < len(data); offset += 32 * 1024 {
		end := min(offset+32*1024, len(data))
		for _, ev := range p.sc.Feed([]byte(data[offset:end])) {
			if ev.Type == EvPaste {
				p.writePasteLocked(string(ev.Data), true)
			} else {
				p.write(ev.Data)
			}
		}
	}
}

func outsidePasteBytes(data []byte) string {
	var outside strings.Builder
	inside := false
	for len(data) > 0 {
		if bytes.HasPrefix(data, []byte(bpStart)) {
			inside = true
			data = data[len(bpStart):]
			continue
		}
		if bytes.HasPrefix(data, []byte(bpEnd)) {
			inside = false
			data = data[len(bpEnd):]
			continue
		}
		if !inside {
			outside.WriteByte(data[0])
		}
		data = data[1:]
	}
	return outside.String()
}

func TestDeliveryDirtyPasteNeverForcesInsertion(t *testing.T) {
	for _, failed := range []bool{false, true} {
		for _, aborted := range []bool{false, true} {
			for _, held := range []bool{false, true} {
				name := fmt.Sprintf("failed=%t/aborted=%t/held=%t", failed, aborted, held)
				t.Run(name, func(t *testing.T) {
					p, f, paths, _ := deliveryFixture(t, 0, failed, 1)
					if p.up.autoInsertWindow != 5*time.Second {
						t.Fatal("regression must use default five-second window")
					}
					original := bpStart + strings.Repeat("x", maxPaste+1)
					if aborted {
						original = bpStart + "user paste waiting for its end"
					}
					feedDeliveryBytes(p, original)
					if aborted {
						p.mu.Lock()
						for _, ev := range p.sc.Idle(3 * time.Second) {
							p.write(ev.Data)
						}
						p.mu.Unlock()
					}
					if !p.sc.RawPasteOpen() {
						t.Fatal("fixture did not open forwarded raw paste")
					}
					before := terminalContents(t, f)
					done := make(chan struct{})
					go func() {
						p.processDrop(drop{payload: FormatInjection(paths), tokens: paths, bracketed: true, accepted: time.Now()})
						close(done)
					}()
					// The old parser fallback forced insertion after one second.
					select {
					case <-done:
						t.Fatal("delivery finished while paste was dirty")
					case <-time.After(1200 * time.Millisecond):
					}
					p.mu.Lock()
					during := terminalContents(t, f)
					p.mu.Unlock()
					if during != before {
						t.Fatal("inserted bytes into an open user paste")
					}
					tail := "echo USER-TEXT\n" + bpEnd
					if !held {
						feedDeliveryBytes(p, tail)
					}
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("delivery did not finish at the cutoff")
					}
					results, err := listPending(p.stateDir)
					if err != nil {
						t.Fatal(err)
					}
					if held {
						if terminalContents(t, f) != before || len(results) != 1 || results[0].OK == failed {
							t.Fatalf("dirty result was not held: %+v", results)
						}
						feedDeliveryBytes(p, tail)
					} else {
						if len(results) != 0 {
							t.Fatal("clean quick delivery was held")
						}
						payload := FormatInjection([]string{"/remote/.moshdrop/a shot.png"})
						if failed {
							payload = FormatInjection(paths)
						}
						if terminalContents(t, f) != before+tail+bpStart+payload+bpEnd {
							t.Fatal("clean insertion or failure replay changed byte order")
						}
					}
					if outside := outsidePasteBytes([]byte(terminalContents(t, f))); outside != "" {
						t.Fatalf("user text escaped paste markers: %q", outside)
					}
				})
			}
		}
	}
}

func TestDeliveryFullQueueHoldsDirtyReplay(t *testing.T) {
	p, f, paths, _ := deliveryFixture(t, 0, false, 1)
	p.drops = make(chan drop, 1)
	p.drops <- drop{}
	feedDeliveryBytes(p, bpStart+strings.Repeat("x", maxPaste+1))
	before := terminalContents(t, f)
	done := make(chan struct{})
	Notify = func(string, string) { close(done) }
	start := time.Now()
	p.enqueue([]drop{{payload: FormatInjection(paths), tokens: paths, bracketed: true, accepted: time.Now()}})
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("queue fallback blocked input")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dirty queue overflow was not recorded")
	}
	results, err := listPending(p.stateDir)
	if err != nil || len(results) != 1 || results[0].OK || terminalContents(t, f) != before {
		t.Fatalf("dirty overflow replayed: %+v %v", results, err)
	}
	feedDeliveryBytes(p, "echo USER-TEXT\n"+bpEnd)
	if outside := outsidePasteBytes([]byte(terminalContents(t, f))); outside != "" {
		t.Fatalf("user text escaped paste markers: %q", outside)
	}
}
