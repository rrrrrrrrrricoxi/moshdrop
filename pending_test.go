package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func pendingFixture(t *testing.T, state, target string) PendingResult {
	t.Helper()
	r, err := savePending(state, PendingResult{Target: target, Files: []string{"a.png", "b.png"}, Paths: []string{"/remote/a image.png", "/remote/图;$.png"}, OK: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rewritePendingFixture(t *testing.T, state string, r PendingResult) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "pending", r.ID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPendingPersistencePrivacyAndExplicitCopy(t *testing.T) {
	state := t.TempDir()
	t.Setenv("MOSHDROP_STATE_DIR", state)
	a := pendingFixture(t, state, "host-A")
	b := pendingFixture(t, state, "host-B")
	if a.ID == b.ID || !validPendingID(a.ID) {
		t.Fatal("IDs are not unique")
	}
	for path, mode := range map[string]os.FileMode{filepath.Join(state, "pending"): 0700, filepath.Join(state, "pending", a.ID+".json"): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s: %v %v", path, info, err)
		}
	}
	// A fresh load has no originating-process pointer/state dependency.
	loaded, err := loadPending(state, a.ID)
	if err != nil || loaded.Clipboard != FormatInjection(a.Paths) {
		t.Fatalf("persisted group: %+v %v", loaded, err)
	}
	oldCopy := copyToClipboard
	defer func() { copyToClipboard = oldCopy }()
	var copied string
	copyToClipboard = func(s string) error { copied = s; return nil }
	out, errs, code := captureCommand(t, func() int { return runPending([]string{"host-A"}) })
	if code != 0 || errs != "" || !strings.Contains(out, a.ID) || strings.Contains(out, b.ID) || copied != "" {
		t.Fatalf("list copied or leaked wrong host: %d %q %q", code, out, errs)
	}
	_, _, code = captureCommand(t, func() int { return runPending([]string{"--copy", b.ID}) })
	if code != 0 || copied != FormatInjection(b.Paths) {
		t.Fatalf("wrong group copied: %q", copied)
	}
	copyToClipboard = func(string) error { return errors.New("pbcopy fixture failed") }
	out, errs, code = captureCommand(t, func() int { return runPending([]string{"--copy", a.ID}) })
	if code != 1 || strings.Contains(out, msg("pending.copied")) || !strings.Contains(errs, "pbcopy fixture failed") {
		t.Fatalf("copy failure misreported: %d %q %q", code, out, errs)
	}
	// Listing/copying does not consume a result or depend on a notification.
	if _, err := loadPending(state, a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPendingRejectsInvalidAndUnsafeFiles(t *testing.T) {
	for _, tc := range []string{"id", "version", "clipboard", "symlink", "hardlink", "permissions", "directory", "expired", "oversize"} {
		t.Run(tc, func(t *testing.T) {
			state := t.TempDir()
			r := pendingFixture(t, state, "host")
			path := filepath.Join(state, "pending", r.ID+".json")
			id := r.ID
			switch tc {
			case "id":
				id = "../config"
			case "version":
				r.Version = 99
				rewritePendingFixture(t, state, r)
			case "clipboard":
				r.Clipboard = "not these paths"
				rewritePendingFixture(t, state, r)
			case "symlink":
				other := filepath.Join(state, "other")
				os.Rename(path, other)
				os.Symlink(other, path)
			case "hardlink":
				os.Link(path, filepath.Join(state, "other"))
			case "permissions":
				os.Chmod(path, 0644)
			case "directory":
				os.Remove(path)
				os.Mkdir(path, 0700)
			case "expired":
				r.Created = time.Now().Add(-pendingRetention - time.Hour)
				rewritePendingFixture(t, state, r)
			case "oversize":
				file, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				file.Truncate(pendingMaxBytes + 1)
				file.Close()
			}
			if _, err := loadPending(state, id); err == nil {
				t.Fatal("unsafe record accepted")
			}
		})
	}
	for _, kind := range []string{"symlink", "public_mode"} {
		t.Run("directory_"+kind, func(t *testing.T) {
			state := t.TempDir()
			dir := filepath.Join(state, "pending")
			if kind == "symlink" {
				target := t.TempDir()
				os.Chmod(target, 0700)
				os.Symlink(target, dir)
			} else {
				os.Mkdir(dir, 0755)
			}
			if _, err := savePending(state, PendingResult{}); err == nil {
				t.Fatal("unsafe directory accepted")
			}
		})
	}
}

func TestPendingPrunesByAgeAndCountButNotUnknownFiles(t *testing.T) {
	state := t.TempDir()
	old := pendingFixture(t, state, "old")
	old.Created = time.Now().Add(-pendingRetention - time.Hour)
	rewritePendingFixture(t, state, old)
	unknown := filepath.Join(state, "pending", "unrelated.txt")
	os.WriteFile(unknown, []byte("leave alone"), 0600)
	for i := 0; i < pendingMaxRecords+4; i++ {
		pendingFixture(t, state, "current")
	}
	rows, err := listPending(state)
	if err != nil || len(rows) != pendingMaxRecords {
		t.Fatalf("unbounded history %d: %v", len(rows), err)
	}
	if _, err := os.Stat(filepath.Join(state, "pending", old.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("expired result survived")
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("deleted unrelated file")
	}
}

func TestPendingConcurrentSessions(t *testing.T) {
	state := t.TempDir()
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := savePending(state, PendingResult{Target: "parallel", Paths: []string{"/r/x.png"}, Files: []string{"x.png"}, OK: true})
			if err != nil {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	rows, err := listPending(state)
	if err != nil || len(rows) != 20 {
		t.Fatalf("parallel results lost: %d %v", len(rows), err)
	}
}

func TestPendingFailureAndEmptyHistory(t *testing.T) {
	state := t.TempDir()
	t.Setenv("MOSHDROP_STATE_DIR", state)
	oldCopy := copyToClipboard
	copyToClipboard = func(string) error { t.Fatal("copied a failed upload"); return nil }
	defer func() { copyToClipboard = oldCopy }()
	out, _, code := captureCommand(t, func() int { return runPending(nil) })
	if code != 0 || out == "" {
		t.Fatal("empty history failed")
	}
	r, err := savePending(state, PendingResult{Target: "host", OK: false, Error: "disk full", Paths: []string{"/must/not/copy"}, Clipboard: "must not copy"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Clipboard != "" || len(r.Paths) != 0 {
		t.Fatal("failure has copyable paths")
	}
	_, errs, code := captureCommand(t, func() int { return runPending([]string{"--copy", r.ID}) })
	if code != 1 || !strings.Contains(errs, "disk full") {
		t.Fatal("failed upload was copyable")
	}
	for _, args := range [][]string{{"--copy"}, {"--copy", "../unsafe"}, {"--unknown"}, {"host", "extra"}} {
		_, _, code := captureCommand(t, func() int { return runPending(args) })
		if code != 1 {
			t.Fatalf("bad arguments accepted: %v", args)
		}
	}
}

func TestNotifierReadsGoRecordWithoutUI(t *testing.T) {
	helper := os.Getenv("MOSHDROP_TEST_NOTIFIER")
	if helper == "" {
		t.Skip("set MOSHDROP_TEST_NOTIFIER to the built helper for pure cross-language record checks")
	}
	state := t.TempDir()
	r := pendingFixture(t, state, "host")
	output, err := exec.Command(helper, "--check-record", state, r.ID).CombinedOutput()
	if err != nil || strings.TrimSuffix(string(output), "\n") != r.Clipboard {
		t.Fatalf("Swift/Go record mismatch: %q %v", output, err)
	}
	r.Clipboard = "tampered"
	rewritePendingFixture(t, state, r)
	if err := exec.Command(helper, "--check-record", state, r.ID).Run(); err == nil {
		t.Fatal("Swift accepted mismatched paths")
	}
	r.Clipboard = FormatInjection(r.Paths)
	r.Created = time.Now().Add(-pendingRetention - time.Hour)
	rewritePendingFixture(t, state, r)
	if err := exec.Command(helper, "--check-record", state, r.ID).Run(); err == nil {
		t.Fatal("Swift accepted expired result")
	}
}

func TestNotifierReceiptAndFailureBoundariesWithoutUI(t *testing.T) {
	t.Setenv("MOSHDROP_MUTE_NOTIFY", "")
	app := filepath.Join(t.TempDir(), "Fixture.app")
	binDir := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "MoshdropNotifier"), []byte("unused fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOSHDROP_NOTIFIER_APP", app)
	state := t.TempDir()
	r := pendingFixture(t, state, "host-A")
	pendingFixture(t, state, "host-B") // A must not be replaced by the most recent result.
	oldRun := runNotifierCommand
	defer func() { runNotifierCommand = oldRun }()
	for _, tc := range []struct {
		name, payload string
		wantError     bool
	}{
		{"posted", `{"ok":true,"error":""}`, false},
		{"denied", `{"ok":false,"error":"permission denied"}`, true},
		{"malformed", "not JSON", true},
		{"oversize", strings.Repeat("x", 4097), true},
		{"launch_failed", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var receiptPath string
			runNotifierCommand = func(cmd *exec.Cmd) ([]byte, error) {
				args := cmd.Args
				if len(args) != 10 || args[6] != "--notify" || args[7] != state || args[8] != r.ID || !validPendingID(args[9]) {
					t.Fatalf("incorrect helper association: %v", args)
				}
				receiptPath = filepath.Join(state, "pending", ".receipt-"+args[9]+".json")
				fi, err := os.Stat(receiptPath)
				if err != nil || fi.Mode().Perm() != 0600 {
					t.Fatal("receipt not precreated privately")
				}
				if tc.name == "launch_failed" {
					return nil, errors.New("launch fixture failed")
				}
				return nil, os.WriteFile(receiptPath, []byte(tc.payload), 0600)
			}
			err := realNotifyPending(state, r)
			if (err != nil) != tc.wantError {
				t.Fatalf("receipt error: %v", err)
			}
			if _, err := os.Stat(receiptPath); !os.IsNotExist(err) {
				t.Fatal("receipt leaked after helper invocation")
			}
		})
	}
}

func TestNotifierUnavailableOrMutedDoesNotLaunch(t *testing.T) {
	t.Setenv("MOSHDROP_NOTIFIER_APP", filepath.Join(t.TempDir(), "missing.app"))
	if _, err := notifierApp(); err == nil {
		t.Fatal("missing helper accepted")
	}
	t.Setenv("MOSHDROP_MUTE_NOTIFY", "1")
	if err := realNotifyPending(t.TempDir(), PendingResult{OK: true, ID: strings.Repeat("a", 32)}); err == nil {
		t.Fatal("mute attempted native delivery")
	}
}
