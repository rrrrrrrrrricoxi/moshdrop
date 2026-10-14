package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Run generated scripts in a private HOME. Never invoke SSH.
func sandboxRunner(home string) func(context.Context, io.Reader, []string) cmdResult {
	return func(ctx context.Context, stdin io.Reader, argv []string) cmdResult {
		cmd := exec.CommandContext(ctx, "sh", "-c", argv[len(argv)-1])
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = stdin
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return cmdResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err}
	}
}

func oldSandboxFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte("preserve or expire"), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteCleanupRejectsHomeAndEscape(t *testing.T) {
	for _, name := range []string{".", "./.", "home-link", "outside-link", "nested/outside-link"} {
		t.Run(name, func(t *testing.T) {
			home, outside, state := t.TempDir(), t.TempDir(), t.TempDir()
			homeFile, outsideFile := filepath.Join(home, "user.txt"), filepath.Join(outside, "user.txt")
			oldSandboxFile(t, homeFile, 10*24*time.Hour)
			oldSandboxFile(t, outsideFile, 10*24*time.Hour)
			if err := os.Mkdir(filepath.Join(home, "nested"), 0700); err != nil {
				t.Fatal(err)
			}
			for link, destination := range map[string]string{"home-link": home, "outside-link": outside, "nested/outside-link": outside} {
				if err := os.Symlink(destination, filepath.Join(home, link)); err != nil {
					t.Fatal(err)
				}
			}
			u := NewUploader("fake", nil, state)
			u.remoteName, u.run = name, sandboxRunner(home)
			if err := u.ensure(context.Background()); err == nil {
				t.Fatal("accepted unsafe resolved directory")
			}
			for _, path := range []string{homeFile, outsideFile} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("cleanup touched user data %s: %v", path, err)
				}
			}
		})
	}
}

func TestRemoteCleanupStaysInUploadDirectory(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, ".moshdrop")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	homeFile, outsideFile := filepath.Join(home, "user.txt"), filepath.Join(outside, "user.txt")
	expired, orphan, fresh := filepath.Join(dir, "expired.txt"), filepath.Join(dir, ".part-old"), filepath.Join(dir, "fresh.txt")
	oldSandboxFile(t, homeFile, 10*24*time.Hour)
	oldSandboxFile(t, outsideFile, 10*24*time.Hour)
	oldSandboxFile(t, expired, 10*24*time.Hour)
	oldSandboxFile(t, orphan, 2*time.Hour)
	oldSandboxFile(t, fresh, 0)
	if err := os.Symlink(outside, filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	u := NewUploader("fake", nil, t.TempDir())
	u.run = sandboxRunner(home)
	if err := u.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{homeFile, outsideFile, fresh} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup removed retained file %s: %v", path, err)
		}
	}
	for _, path := range []string{expired, orphan} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup retained expired file %s: %v", path, err)
		}
	}
}

func TestUploadExactDestinationCollisions(t *testing.T) {
	for _, kind := range []string{"file", "directory", "directory-symlink", "file-symlink", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			dir, target := filepath.Join(home, ".moshdrop"), filepath.Join(home, ".moshdrop", "notes.txt")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "file":
				oldSandboxFile(t, target, 0)
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			default:
				existing := filepath.Join(home, "existing")
				if kind == "directory-symlink" {
					if err := os.Mkdir(existing, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "file-symlink" {
					oldSandboxFile(t, existing, 0)
				}
				if err := os.Symlink(existing, target); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			u := NewUploader("fake", nil, t.TempDir())
			u.run = sandboxRunner(home)
			paths, err := u.Upload(context.Background(), []string{tmpFile(t, "notes.txt", "actual contents")})
			if err != nil {
				t.Fatal(err)
			}
			physicalDir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(paths) != 1 || paths[0] != filepath.Join(physicalDir, "notes-1.txt") {
				t.Fatalf("wrong destination: %v", paths)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || string(data) != "actual contents" {
				t.Fatalf("wrong uploaded bytes: %q %v", data, err)
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("overwrote collision target")
			}
			err = filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if strings.HasPrefix(entry.Name(), ".part-") {
					t.Errorf("left hidden upload link: %s", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUploadConcurrentNamesDoNotOverwrite(t *testing.T) {
	home := t.TempDir()
	local := tmpFile(t, "shared.txt", "same bytes")
	const count = 6
	results := make(chan string, count)
	failures := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		u := NewUploader("fake", nil, t.TempDir())
		u.run = sandboxRunner(home)
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths, err := u.Upload(context.Background(), []string{local})
			if err != nil {
				failures <- err
				return
			}
			results <- paths[0]
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	seen := map[string]bool{}
	for path := range results {
		if seen[path] {
			t.Errorf("duplicate destination: %s", path)
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "same bytes" {
			t.Errorf("lost upload: %s: %q %v", path, data, err)
		}
	}
	if len(seen) != count {
		t.Fatalf("got %d uploads, want %d", len(seen), count)
	}
}

func TestUploadUsesCachedPhysicalDirectory(t *testing.T) {
	home := t.TempDir()
	first, second := filepath.Join(home, "first"), filepath.Join(home, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(home, "drops")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	u := NewUploader("fake", nil, t.TempDir())
	u.remoteName, u.run = "drops", sandboxRunner(home)
	if err := u.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	paths, err := u.Upload(context.Background(), []string{tmpFile(t, "file.txt", "data")})
	if err != nil {
		t.Fatal(err)
	}
	physicalFirst, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != filepath.Join(physicalFirst, "file.txt") {
		t.Fatalf("reported wrong physical destination: %v", paths)
	}
	if _, err := os.Stat(filepath.Join(second, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("followed changed symlink: %v", err)
	}
}

func TestUploadRejectsChangedPhysicalDirectory(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, ".moshdrop")
	u := NewUploader("fake", nil, t.TempDir())
	u.run = sandboxRunner(home)
	if err := u.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Upload(context.Background(), []string{tmpFile(t, "file.txt", "data")}); err == nil {
		t.Fatal("accepted escaped directory after ensure")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside HOME: %v %v", entries, err)
	}
}

func TestUploadMissingLinkFailsBeforeReceivingBytes(t *testing.T) {
	home := t.TempDir()
	u := NewUploader("fake", nil, t.TempDir())
	u.run = sandboxRunner(home)
	if err := u.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Keep only the shell and mkdir in PATH; omit link and data-copy tools.
	bin := t.TempDir()
	for _, name := range []string{"sh", "mkdir"} {
		executable, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(executable, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	u.run = func(ctx context.Context, stdin io.Reader, argv []string) cmdResult {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", argv[len(argv)-1])
		cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin)
		cmd.Stdin = stdin
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		return cmdResult{err: cmd.Run(), stderr: stderr.Bytes()}
	}
	start := time.Now()
	_, err := u.uploadOne(context.Background(), tmpFile(t, "file.txt", "data"))
	if err == nil || !strings.Contains(err.Error(), "requires the link utility") {
		t.Fatalf("missing actionable error: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("missing link did not fail promptly")
	}
	entries, err := os.ReadDir(u.remoteDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("received bytes before checking link: %v %v", entries, err)
	}
}
