package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const pendingVersion = 1
const pendingMaxRecords = 100
const pendingRetention = 7 * 24 * time.Hour
const pendingMaxBytes = 8 << 20

// PendingResult contains result metadata, not uploaded file contents. Clipboard
// text uses the same escaping as ordinary terminal insertion.
type PendingResult struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Target    string    `json:"target"`
	Source    string    `json:"source,omitempty"`
	Files     []string  `json:"files"`
	Paths     []string  `json:"paths,omitempty"`
	Clipboard string    `json:"clipboard,omitempty"`
	Created   time.Time `json:"created"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
}

func validPendingID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func privatePendingDir(stateDir string, create bool) (string, error) {
	root, err := filepath.Abs(stateDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "pending")
	if create {
		if err := os.MkdirAll(root, 0700); err != nil {
			return "", err
		}
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || fi.Mode().Perm() != 0700 || !ok || int(st.Uid) != os.Getuid() {
		return "", errors.New("pending directory must be an owned, private directory (0700), not a symlink")
	}
	return dir, nil
}

func savePending(stateDir string, r PendingResult) (PendingResult, error) {
	dir, err := privatePendingDir(stateDir, true)
	if err != nil {
		return r, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return r, err
	}
	r.ID = hex.EncodeToString(token[:])
	r.Version = pendingVersion
	r.Created = time.Now().UTC()
	if r.OK {
		r.Clipboard = FormatInjection(r.Paths)
	} else {
		r.Paths = nil
		r.Clipboard = ""
	}
	data, err := json.Marshal(r)
	if err != nil {
		return r, err
	}
	if len(data) > pendingMaxBytes {
		return r, errors.New("upload result is too large for pending history")
	}
	tmp, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return r, err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return r, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return r, err
	}
	if err := tmp.Close(); err != nil {
		return r, err
	}
	if err := os.Rename(name, filepath.Join(dir, r.ID+".json")); err != nil {
		return r, err
	}
	if err := prunePending(stateDir); err != nil {
		return r, fmt.Errorf("saved result %s, but history cleanup failed: %w", r.ID, err)
	}
	return r, nil
}

func readPendingFile(dir, id string) (PendingResult, error) {
	var r PendingResult
	if !validPendingID(id) {
		return r, errors.New("invalid upload ID")
	}
	path := filepath.Join(dir, id+".json")
	fi, err := os.Lstat(path)
	if err != nil {
		return r, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0600 || fi.Size() > pendingMaxBytes || !ok || int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		return r, errors.New("result must be an owned, private regular file (0600), not a link")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return r, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return r, err
	}
	if !os.SameFile(fi, opened) {
		return r, errors.New("result changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(f, pendingMaxBytes+1))
	if err != nil {
		return r, err
	}
	if len(data) > pendingMaxBytes {
		return r, errors.New("result exceeds size limit")
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Version != pendingVersion || r.ID != id || r.Created.IsZero() {
		return r, errors.New("invalid result schema or ID")
	}
	if r.OK && (len(r.Paths) == 0 || r.Clipboard != FormatInjection(r.Paths)) {
		return r, errors.New("invalid successful result")
	}
	return r, nil
}

func loadPending(stateDir, id string) (PendingResult, error) {
	dir, err := privatePendingDir(stateDir, false)
	if err != nil {
		return PendingResult{}, err
	}
	r, err := readPendingFile(dir, id)
	if err != nil {
		return r, err
	}
	if time.Since(r.Created) > pendingRetention {
		return r, errors.New("upload result has expired from local history")
	}
	return r, nil
}

func listPending(stateDir string) ([]PendingResult, error) {
	dir, err := privatePendingDir(stateDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var results []PendingResult
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !validPendingID(id) {
			continue
		}
		r, err := readPendingFile(dir, id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // Another local session may have pruned it.
		if err != nil {
			return nil, fmt.Errorf("result %s: %w", id, err)
		}
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Created.Equal(results[j].Created) {
			return results[i].ID > results[j].ID
		}
		return results[i].Created.After(results[j].Created)
	})
	return results, nil
}

func prunePending(stateDir string) error {
	results, err := listPending(stateDir)
	if err != nil {
		return err
	}
	dir, err := privatePendingDir(stateDir, false)
	if err != nil {
		return err
	}
	for i, r := range results {
		if i >= pendingMaxRecords || time.Since(r.Created) > pendingRetention {
			if err := os.Remove(filepath.Join(dir, r.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func runPending(args []string) int {
	state := stateDirPath()
	fail := func(err error) int { fmt.Fprintln(os.Stderr, "moshdrop pending:", err); return 1 }
	if len(args) > 0 && args[0] == "--copy" {
		if len(args) != 2 {
			return fail(errors.New("usage: moshdrop pending --copy <upload-id>"))
		}
		r, err := loadPending(state, args[1])
		if err != nil {
			return fail(err)
		}
		if !r.OK {
			return fail(errors.New("upload failed; no remote paths to copy: " + r.Error))
		}
		if err := copyToClipboard(r.Clipboard); err != nil {
			return fail(fmt.Errorf("could not copy paths: %w", err))
		}
		fmt.Println(msg("pending.copied"))
		return 0
	}
	if len(args) > 1 || (len(args) == 1 && strings.HasPrefix(args[0], "-")) {
		return fail(errors.New("usage: moshdrop pending [host] | moshdrop pending --copy <upload-id>"))
	}
	if _, err := privatePendingDir(state, false); err == nil {
		if err := prunePending(state); err != nil {
			return fail(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	results, err := listPending(state)
	if err != nil {
		return fail(err)
	}
	host := ""
	if len(args) == 1 {
		host = args[0]
	}
	found := false
	for _, r := range results {
		if host != "" && r.Target != host {
			continue
		}
		found = true
		status := msg("pending.ready")
		if !r.OK {
			status = msg("pending.failed")
		}
		// Quote metadata so filenames/hosts cannot inject terminal control sequences.
		fmt.Printf("%s  %s  %s  host=%q files=%q\n", r.ID, r.Created.Format(time.RFC3339), status, r.Target, r.Files)
		if r.OK {
			fmt.Printf("  moshdrop pending --copy %s\n", r.ID)
		} else {
			fmt.Printf("  error=%q\n", r.Error)
		}
	}
	if !found {
		fmt.Println(msg("pending.empty"))
	}
	return 0
}
