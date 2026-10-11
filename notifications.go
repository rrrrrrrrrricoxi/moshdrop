package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var notifyPending = realNotifyPending
var runNotifierCommand = (*exec.Cmd).CombinedOutput // Injectable OS boundary, like the upload runner.

func notifierApp() (string, error) {
	if supplied := os.Getenv("MOSHDROP_NOTIFIER_APP"); supplied != "" {
		path, err := filepath.Abs(supplied)
		if err != nil {
			return "", err
		}
		if !usableNotifier(path) {
			return "", errors.New("MOSHDROP_NOTIFIER_APP does not contain the notification helper")
		}
		return path, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	for _, path := range []string{
		filepath.Join(filepath.Dir(exe), "MoshdropNotifier.app"),
		filepath.Join(filepath.Dir(exe), "..", "libexec", "MoshdropNotifier.app"),
	} {
		if usableNotifier(path) {
			return filepath.Clean(path), nil
		}
	}
	return "", errors.New("notification helper unavailable; use moshdrop pending")
}

func usableNotifier(path string) bool {
	fi, err := os.Stat(filepath.Join(path, "Contents", "MacOS", "MoshdropNotifier"))
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0111 != 0
}

func realNotifyPending(stateDir string, r PendingResult) error {
	if os.Getenv("MOSHDROP_MUTE_NOTIFY") != "" {
		return errors.New("notifications muted")
	}
	if !r.OK || !validPendingID(r.ID) {
		return errors.New("not a successful saved upload")
	}
	app, err := notifierApp()
	if err != nil {
		return err
	}
	state, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	dir, err := privatePendingDir(state, false)
	if err != nil {
		return err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	receiptID := hex.EncodeToString(token[:])
	receipt := filepath.Join(dir, ".receipt-"+receiptID+".json")
	file, err := os.OpenFile(receipt, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(receipt)
	if err := file.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// The helper posts and exits. It does not wait for a click or request
	// permission. A later Copy path action may relaunch it through Launch Services.
	cmd := exec.CommandContext(ctx, "open", "-W", "-g", "-n", app, "--args", "--notify", state, r.ID, receiptID)
	if output, err := runNotifierCommand(cmd); err != nil {
		return fmt.Errorf("notification helper: %w (%s)", err, output)
	}
	response, err := os.Open(receipt)
	if err != nil {
		return fmt.Errorf("notification receipt unavailable: %w", err)
	}
	defer response.Close()
	data, err := io.ReadAll(io.LimitReader(response, 4097))
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return errors.New("notification receipt exceeds size limit")
	}
	var reply struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("notification not posted: %s", reply.Error)
	}
	return nil
}

// Permission setup is explicit. Ordinary sessions never request it.
func runNotifications(args []string) int {
	if len(args) != 1 || args[0] != "setup" {
		fmt.Fprintln(os.Stderr, "usage: moshdrop notifications setup")
		return 1
	}
	app, err := notifierApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := exec.Command("open", "-g", "-n", app, "--args", "--authorize").Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(msg("pending.permission"))
	return 0
}
