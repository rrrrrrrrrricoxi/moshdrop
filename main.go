package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const version = "0.2.0"

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "doctor":
			os.Exit(runDoctor(args[1:]))
		case "--version", "-V":
			fmt.Println("moshdrop", version)
			return
		case "--help", "-h":
			printHelp()
			return
		}
	}

	target := os.Getenv("MOSHDROP_TARGET")
	sshArgv := []string{"ssh"}
	if t, sa, err := DeriveSSHTarget(args); err == nil {
		if target == "" {
			target = t
		}
		sshArgv = sa
	} else if target == "" {
		fmt.Fprintln(os.Stderr, "moshdrop: 未能确定 ssh 目标，拖拽上传停用（mosh 本身不受影响）:", err)
	}

	stateDir := stateDirPath()
	_ = os.MkdirAll(stateDir, 0o700)

	up := NewUploader(target, sshArgv, stateDir)
	if !up.Disabled() {
		go up.Prewarm()
	}

	moshBin := os.Getenv("MOSHDROP_CMD")
	if moshBin == "" {
		moshBin = "mosh"
	}
	code, err := RunProxy(exec.Command(moshBin, args...), up)
	up.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "moshdrop:", err)
	}
	os.Exit(code)
}

func stateDirPath() string {
	return filepath.Join(os.Getenv("HOME"), ".moshdrop")
}

func printHelp() {
	fmt.Print(`moshdrop — mosh 的透明包装器：拖文件进终端即自动上传到远端

用法:
  moshdrop <mosh 的任意参数>     照常连接（例: moshdrop ccc -- tmux new -A -s main）
  moshdrop doctor <host>         全链路体检
  moshdrop --version | --help

工作方式: 拖进终端的本地文件会被自动上传到远端 ~/.moshdrop/，
输入流里出现的是远端可用路径。其余输入逐字节透传。
失败时原样放行你的输入并弹系统通知（详情见 ~/.moshdrop/events.log）。
`)
}
