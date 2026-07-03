package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type cmdResult struct {
	stdout []byte
	stderr []byte
	err    error
}

// Uploader 经与 mosh 相同的 ssh 通道把本地文件送到远端 ~/.moshdrop。
// 设计要点：失败绝不粘住（每次按需重试建连）；上传串行化（注入顺序=拖拽顺序）；
// run 可注入测试替身，全部状态机行为可离线单测。
type Uploader struct {
	target   string
	sshArgv  []string
	ctlPath  string
	disabled bool

	mu        sync.Mutex // 串行化 ensure 与上传
	remoteDir string     // 成功后缓存；失败不缓存 → 下次自动重试

	run func(ctx context.Context, stdin io.Reader, argv []string) cmdResult
}

func NewUploader(target string, sshArgv []string, localStateDir string) *Uploader {
	ctlDir := localStateDir
	// macOS unix socket 路径上限约 104 字节；%C 展开为 40 字符哈希。
	if len(filepath.Join(ctlDir, "cm-"))+40 >= 100 {
		short := fmt.Sprintf("/tmp/moshdrop-%d", os.Getuid())
		if err := os.MkdirAll(short, 0o700); err == nil {
			ctlDir = short
		}
	}
	if len(sshArgv) == 0 {
		sshArgv = []string{"ssh"}
	}
	return &Uploader{
		target:   target,
		sshArgv:  sshArgv,
		ctlPath:  filepath.Join(ctlDir, "cm-%C"),
		disabled: target == "",
		run:      realRun,
	}
}

func realRun(ctx context.Context, stdin io.Reader, argv []string) cmdResult {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return cmdResult{out.Bytes(), errb.Bytes(), err}
}

// Disabled: 无法确定 ssh 目标时为真——拦截逻辑必须完全让路（真·纯透传）。
func (u *Uploader) Disabled() bool { return u.disabled }

func (u *Uploader) sshCmd(script string) []string {
	argv := append([]string{}, u.sshArgv...)
	argv = append(argv,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath="+u.ctlPath,
		"-o", "ControlPersist=120",
		u.target, script)
	return argv
}

// ensure 建连并解析远端 drop 目录。成功缓存、失败不缓存。调用方须持有 u.mu。
// 远端命令统一包 sh -c：fish/csh 登录 shell 的远端同样可用。
func (u *Uploader) ensure(ctx context.Context) error {
	if u.disabled {
		return fmt.Errorf("未能从参数确定 ssh 目标，拖拽上传已停用")
	}
	if u.remoteDir != "" {
		return nil
	}
	res := u.run(ctx, nil, u.sshCmd(`sh -c 'mkdir -p "$HOME/.moshdrop" && printf %s "$HOME/.moshdrop"'`))
	if res.err != nil {
		return classifySSHError(res.err, res.stderr)
	}
	dir := strings.TrimSpace(string(res.stdout))
	if !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("解析远端目录失败: %q", dir)
	}
	u.remoteDir = dir
	return nil
}

// Prewarm 启动时异步预热（best-effort，失败无副作用——之后每次上传按需重试）。
func (u *Uploader) Prewarm() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u.mu.Lock()
	defer u.mu.Unlock()
	_ = u.ensure(ctx)
}

// Close 收掉 ControlMaster 长连接（退出不留残连）。
func (u *Uploader) Close() {
	if u.disabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	argv := append([]string{}, u.sshArgv...)
	argv = append(argv, "-o", "ControlPath="+u.ctlPath, "-O", "exit", u.target)
	_ = u.run(ctx, nil, argv)
}

// Upload 串行上传并返回远端绝对路径（保序）。单文件失败会重建连接静默重试一次。
func (u *Uploader) Upload(ctx context.Context, locals []string) ([]string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.ensure(ctx); err != nil {
		return nil, err
	}
	remotes := make([]string, len(locals))
	for i, lp := range locals {
		name, err := u.uploadOne(ctx, lp)
		if err != nil && ctx.Err() == nil {
			// 连接可能中途死了：重建后再试一次
			u.remoteDir = ""
			if err2 := u.ensure(ctx); err2 == nil {
				name, err = u.uploadOne(ctx, lp)
			}
		}
		if err != nil {
			return nil, err
		}
		remotes[i] = u.remoteDir + "/" + name
	}
	return remotes, nil
}

// uploadTimeout 按大小自适应：保守按 100KB/s 估算，下限 60s——大文件不再被一刀切判死。
func uploadTimeout(size int64) time.Duration {
	d := time.Duration(size/(100<<10)) * time.Second
	if d < 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

// uploadOne：单次 ssh 往返完成"挑名 + 写临时名 + mv 正式名 + 回显最终名"。
// 先写 .part-$$ 成功后才 mv：中途断网不会留下占用正式名的半截文件。
func (u *Uploader) uploadOne(ctx context.Context, localPath string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	tctx, cancel := context.WithTimeout(ctx, uploadTimeout(fi.Size()))
	defer cancel()

	name := sanitizeName(localPath)
	script := `d="$HOME/.moshdrop"; n=` + shellQuote(name) + `; c="$n"; i=1
case "$n" in *.*) b="${n%.*}"; e=".${n##*.}";; *) b="$n"; e="";; esac
while [ -e "$d/$c" ]; do c="${b}-${i}${e}"; i=$((i+1)); done
t="$d/.part-$$"
trap 'rm -f "$t"' HUP INT TERM
cat > "$t" && mv "$t" "$d/$c" && printf '%s\n' "$c"`
	res := u.run(tctx, f, u.sshCmd("sh -c "+shellQuote(script)))
	if res.err != nil {
		if tctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("上传超时（%s %.1f MB，网络太慢或已断开）",
				filepath.Base(localPath), float64(fi.Size())/(1<<20))
		}
		return "", classifySSHError(res.err, res.stderr)
	}
	// 只剥协议约定的那一个尾换行——文件名自身的首尾空白必须原样保留
	out := strings.TrimSuffix(string(res.stdout), "\n")
	if out == "" {
		return "", fmt.Errorf("远端未回显文件名")
	}
	return out, nil
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

// shellQuote 单引号安全包裹：' → '\''（该写法 POSIX sh/fish/csh 皆兼容）
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
