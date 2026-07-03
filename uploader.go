package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Uploader struct {
	target    string
	ctlPath   string
	remoteDir string // 远端 drop 目录绝对路径（Prewarm 填充）
	ready     chan struct{}
	err       error
}

func NewUploader(target, localStateDir string) *Uploader {
	ctlDir := localStateDir
	// macOS unix socket 路径上限约 104 字节；%C 会展开成 40 字符哈希。
	// 状态目录太深时回退到短路径，否则 ssh 直接报 "ControlPath too long"。
	if len(filepath.Join(ctlDir, "cm-"))+40 >= 100 {
		short := fmt.Sprintf("/tmp/moshdrop-%d", os.Getuid())
		if err := os.MkdirAll(short, 0o700); err == nil {
			ctlDir = short
		}
	}
	return &Uploader{
		target:  target,
		ctlPath: filepath.Join(ctlDir, "cm-%C"),
		ready:   make(chan struct{}),
	}
}

func (u *Uploader) sshOpts() []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + u.ctlPath,
		"-o", "ControlPersist=120",
	}
}

// Prewarm 建立 ssh 长连接、建远端目录并解析其绝对路径。启动时 goroutine 调用。
func (u *Uploader) Prewarm() {
	defer close(u.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := append(u.sshOpts(), u.target,
		`mkdir -p "$HOME/.moshdrop" && printf %s "$HOME/.moshdrop"`)
	out, err := exec.CommandContext(ctx, "ssh", args...).Output()
	if err != nil {
		u.err = fmt.Errorf("ssh 预热失败: %w", err)
		return
	}
	dir := strings.TrimSpace(string(out))
	if !strings.HasPrefix(dir, "/") {
		u.err = fmt.Errorf("解析远端目录失败: %q", dir)
		return
	}
	u.remoteDir = dir
}

// Upload 逐个上传并返回远端绝对路径（保序）。任何失败即整体报错（调用方 fail-open）。
func (u *Uploader) Upload(ctx context.Context, locals []string) ([]string, error) {
	select {
	case <-u.ready:
	case <-ctx.Done():
		return nil, fmt.Errorf("ssh 连接未就绪: %w", ctx.Err())
	}
	if u.err != nil {
		return nil, u.err
	}
	remotes := make([]string, len(locals))
	for i, lp := range locals {
		name, err := u.uploadOne(ctx, lp)
		if err != nil {
			return nil, err
		}
		remotes[i] = u.remoteDir + "/" + name
	}
	return remotes, nil
}

// uploadOne：单次 ssh 往返完成"挑选不冲突文件名 + cat 流式写入 + 回显最终名"。
// 用 cat 重定向而非 scp，彻底绕开 scp 的远端 shell 引号问题。
func (u *Uploader) uploadOne(ctx context.Context, localPath string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	name := sanitizeName(localPath)
	script := `d="$HOME/.moshdrop"; n=` + shellQuote(name) + `; c="$n"; i=1
case "$n" in *.*) b="${n%.*}"; e=".${n##*.}";; *) b="$n"; e="";; esac
while [ -e "$d/$c" ]; do c="${b}-${i}${e}"; i=$((i+1)); done
cat > "$d/$c" && printf %s "$c"`
	args := append(u.sshOpts(), u.target, script)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = f
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("上传 %s 失败: %w", filepath.Base(localPath), err)
	}
	final := strings.TrimSpace(string(out))
	if final == "" {
		return "", fmt.Errorf("远端未回显文件名")
	}
	return final, nil
}

// sanitizeName 取 basename 并剔除控制字符；空名兜底为 "file"。
func sanitizeName(p string) string {
	name := filepath.Base(p)
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" || out == "/" || out == "." {
		return "file"
	}
	return out
}

// shellQuote 单引号安全包裹：' → '\''
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
