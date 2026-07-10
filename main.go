package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// version 由 goreleaser 在发布构建时注入（-X main.version）
var version = "1.0.0"

func main() {
	// 语言尽早解析（--help 等子命令也要吃到 config 的 lang）
	setLang(LoadConfig(stateDirPath(), "").Lang)
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "doctor":
			os.Exit(runDoctor(args[1:]))
		case "paste":
			os.Exit(runPaste(args[1:]))
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
		fmt.Fprintln(os.Stderr, msg("m.notarget"), err)
	}

	stateDir := stateDirPath()
	_ = os.MkdirAll(stateDir, 0o700)
	cfg := LoadConfig(stateDir, target)
	setLang(cfg.Lang)

	up := NewUploader(target, sshArgv, stateDir)
	up.ApplyConfig(cfg)
	if !cfg.Intercept {
		up.disabled.Store(true) // 配置关拦截 = 真·纯透传
	}
	pasteKeyEnabled = cfg.PasteKey // 会话内 Ctrl+V 贴图（uploader 停用时门上还有一道 Disabled 闸）
	go sweepStaleClipTemps()       // 上次中途死亡残留的截图临时目录，启动时兜底清扫
	if !up.Disabled() {
		go up.Prewarm()
	}

	moshBin := os.Getenv("MOSHDROP_CMD")
	if moshBin == "" {
		moshBin = "mosh"
	}
	code, err := RunProxy(exec.Command(moshBin, args...), up)
	trace("RunProxy 返回 code=%d err=%v, 开始 up.Close", code, err)
	up.Close()
	trace("up.Close 完成, 即将 os.Exit(%d)", code)
	if err != nil {
		fmt.Fprintln(os.Stderr, "moshdrop:", err)
	}
	os.Exit(code)
}

func stateDirPath() string {
	if d := os.Getenv("MOSHDROP_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("HOME"), ".moshdrop")
}

func curLangIsZH() bool {
	return effectiveLang() == "zh"
}

func printHelp() {
	if curLangIsZH() {
		fmt.Print(`moshdrop — mosh 的透明包装器：拖文件进终端即自动上传到远端

用法:
  moshdrop <mosh 的任意参数>     照常连接（例: moshdrop ccc -- tmux new -A -s main）
  moshdrop paste [host]          把剪贴板里的图片上传，远端路径回填剪贴板
  moshdrop doctor <host>         全链路体检
  moshdrop --version | --help

工作方式: 拖进终端的本地文件自动上传到远端（默认 ~/.moshdrop/），
输入流里出现的是远端可用路径；剪贴板里有图时按 Ctrl+V 同样直接上传；
其余输入逐字节透传。失败时原样放行你的输入并弹系统通知（剪贴板贴图无可放行物，
只通知；详情见 ~/.moshdrop/events.log）。
配置: ~/.moshdrop/config（ttl_days / intercept / lang / remote_dir / max_intercept_mb / paste_key，支持 host.<别名>.键 覆盖）
`)
		return
	}
	fmt.Print(`moshdrop — transparent mosh wrapper: drag files into your terminal, they land on the remote

Usage:
  moshdrop <any mosh args>       connect as usual (e.g. moshdrop myhost -- tmux new -A -s main)
  moshdrop paste [host]          upload the clipboard image; remote path is copied back to your clipboard
  moshdrop doctor <host>         end-to-end health check
  moshdrop --version | --help

How it works: local files dragged into the terminal are uploaded to the remote
(default ~/.moshdrop/) and your input stream receives a usable remote path.
Press Ctrl+V with an image in the clipboard to upload it the same way.
Everything else passes through byte-for-byte. On failure your original paste is
passed through untouched (clipboard-image path: notification only), plus a
system notification (see ~/.moshdrop/events.log).
Config: ~/.moshdrop/config (ttl_days / intercept / lang / remote_dir / max_intercept_mb / paste_key; host.<alias>.key overrides)
`)
}
