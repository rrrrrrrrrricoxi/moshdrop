package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "doctor" {
		fmt.Println("moshdrop doctor：v0.2 计划中")
		return
	}

	target := os.Getenv("MOSHDROP_TARGET")
	if target == "" {
		if t, err := DeriveSSHTarget(args); err == nil {
			target = t
		} else {
			fmt.Fprintln(os.Stderr, "moshdrop: 无法确定 ssh 目标，降级为纯透传:", err)
		}
	}

	stateDir := filepath.Join(os.Getenv("HOME"), ".moshdrop")
	_ = os.MkdirAll(stateDir, 0o700)

	up := NewUploader(target, stateDir)
	if target != "" {
		go up.Prewarm()
	}

	moshBin := os.Getenv("MOSHDROP_CMD")
	if moshBin == "" {
		moshBin = "mosh"
	}
	code, err := RunProxy(exec.Command(moshBin, args...), up)
	if err != nil {
		fmt.Fprintln(os.Stderr, "moshdrop:", err)
	}
	os.Exit(code)
}
