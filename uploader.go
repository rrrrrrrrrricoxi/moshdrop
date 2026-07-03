package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type cmdResult struct {
	stdout []byte
	stderr []byte
	err    error
}

// errUploadTimeout: 每文件超时是"必败重试"，绝不静默重试第二次。
var errUploadTimeout = errors.New("上传超时")

// minUploadTimeout 可在测试中调小。
var minUploadTimeout = 60 * time.Second

// Uploader 经与 mosh 相同的 ssh 通道把本地文件送到远端 ~/.moshdrop。
// 设计要点：失败绝不粘住（按需重试建连）；上传串行（注入顺序=拖拽顺序）；
// run 可注入测试替身，全部失败路径可离线单测。
type Uploader struct {
	target   string
	sshArgv  []string
	ctlPath  string
	disabled atomic.Bool

	mu        sync.Mutex // 串行化 ensure 与上传
	remoteDir string     // 成功后缓存；失败不缓存 → 下次自动重试

	run func(ctx context.Context, stdin io.Reader, argv []string) cmdResult
}

func NewUploader(target string, sshArgv []string, localStateDir string) *Uploader {
	if len(sshArgv) == 0 {
		sshArgv = []string{"ssh"}
	}
	u := &Uploader{
		target:  target,
		sshArgv: sshArgv,
		ctlPath: filepath.Join(ctlSocketDir(localStateDir), "cm-%C"),
		run:     realRun,
	}
	u.disabled.Store(target == "")
	return u
}

// ctlSocketDir 选 ControlMaster socket 目录：
// 状态目录太深（unix socket 路径上限约 104 字节）时回退 /tmp/moshdrop-<uid>，
// 回退目录必须属主是自己且 0700——否则可能被预建目录劫持 socket。
func ctlSocketDir(localStateDir string) string {
	if len(filepath.Join(localStateDir, "cm-"))+40 < 100 {
		return localStateDir
	}
	short := fmt.Sprintf("/tmp/moshdrop-%d", os.Getuid())
	if err := os.MkdirAll(short, 0o700); err == nil {
		if fi, err := os.Stat(short); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok &&
				int(st.Uid) == os.Getuid() && fi.Mode().Perm() == 0o700 {
				return short
			}
		}
	}
	// 属主/权限不对（疑似劫持）：用随机目录，宁可牺牲连接复用
	if d, err := os.MkdirTemp("/tmp", "moshdrop-"); err == nil {
		return d
	}
	return localStateDir
}

func realRun(ctx context.Context, stdin io.Reader, argv []string) cmdResult {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return cmdResult{out.Bytes(), errb.Bytes(), err}
}

// Disabled: 无法确定 ssh 目标（或已 Close）时为真——拦截逻辑必须完全让路。
func (u *Uploader) Disabled() bool { return u.disabled.Load() }

func (u *Uploader) sshCmd(script string) []string {
	argv := append([]string{}, u.sshArgv...)
	argv = append(argv,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", // 死链 ~60s 内必被发现，大文件不再挂数小时
		"-o", "ServerAliveCountMax=4",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath="+u.ctlPath,
		"-o", "ControlPersist=120",
		u.target, script)
	return argv
}

// ensure 建连并解析远端 drop 目录。成功缓存、失败不缓存。调用方须持有 u.mu。
// 远端命令统一包 sh -c 且单行（fish/csh 登录 shell 均可解析）。
// 顺带清理超过 1 小时的孤儿临时文件（SIGKILL 等极端路径的残留）。
func (u *Uploader) ensure(ctx context.Context) error {
	if u.Disabled() {
		return fmt.Errorf("未能从参数确定 ssh 目标，拖拽上传已停用")
	}
	if u.remoteDir != "" {
		return nil
	}
	script := `sh -c 'mkdir -p "$HOME/.moshdrop" && { find "$HOME/.moshdrop" -name ".part-*" -mmin +60 -delete 2>/dev/null || true; } && printf %s "$HOME/.moshdrop"'`
	res := u.run(ctx, nil, u.sshCmd(script))
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

// Prewarm 启动时异步预热（best-effort，失败无副作用）。
func (u *Uploader) Prewarm() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u.mu.Lock()
	defer u.mu.Unlock()
	_ = u.ensure(ctx)
}

// Close 停用上传并收掉 ControlMaster（先停用：在途重试不会复活连接）。
func (u *Uploader) Close() {
	if u.Disabled() {
		return
	}
	u.disabled.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	argv := append([]string{}, u.sshArgv...)
	argv = append(argv, "-o", "ControlPath="+u.ctlPath, "-O", "exit", u.target)
	_ = u.run(ctx, nil, argv)
}

// Upload 串行上传并返回远端绝对路径（保序）。
// 连接类失败重建连接静默重试一次；超时类失败绝不重试（必败且加倍等待）。
func (u *Uploader) Upload(ctx context.Context, locals []string) ([]string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.ensure(ctx); err != nil {
		return nil, err
	}
	remotes := make([]string, len(locals))
	for i, lp := range locals {
		name, err := u.uploadOne(ctx, lp)
		if err != nil && ctx.Err() == nil && !errors.Is(err, errUploadTimeout) {
			u.remoteDir = "" // 连接可能中途死了：重建后再试一次
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

// uploadTimeout 按大小自适应：保守按 100KB/s 估算（死链由 ServerAlive 提前掐断）。
func uploadTimeout(size int64) time.Duration {
	d := time.Duration(size/(100<<10)) * time.Second
	if d < minUploadTimeout {
		d = minUploadTimeout
	}
	return d
}

// uploadOne：单次 ssh 往返完成"写临时名 → 字节数校验 → ln 原子落名 → 回显最终名"。
// 关键防线：
//   - wc -c 对账：连接中途死亡时 cat 只会收到 EOF，字节数不符即拒绝落名——半截文件绝不占用正式名；
//   - ln 原子占名：并发会话同名上传不可能互相覆盖（TOCTOU 免疫），失败自动换后缀重试；
//   - 一切失败路径 rm 临时文件。脚本为单行（csh 登录 shell 也能解析外层）。
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
	script := `d="$HOME/.moshdrop"; n=` + shellQuote(name) + `; t="$d/.part-$$"; ` +
		`trap 'rm -f "$t"' HUP INT TERM; ` +
		`cat > "$t" || { rm -f "$t"; exit 1; }; ` +
		fmt.Sprintf(`[ "$(wc -c < "$t")" -eq %d ] || { rm -f "$t"; exit 1; }; `, fi.Size()) +
		`c="$n"; i=1; case "$n" in *.*) b="${n%.*}"; e=".${n##*.}";; *) b="$n"; e="";; esac; ` +
		`while ! ln "$t" "$d/$c" 2>/dev/null; do c="${b}-${i}${e}"; i=$((i+1)); ` +
		`[ "$i" -gt 999 ] && { rm -f "$t"; exit 1; }; done; ` +
		`rm -f "$t"; printf '%s\n' "$c"`
	res := u.run(tctx, f, u.sshCmd("sh -c "+shellQuote(script)))
	if res.err != nil {
		if tctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%w（%s %.1f MB，网络太慢或已断开）",
				errUploadTimeout, filepath.Base(localPath), float64(fi.Size())/(1<<20))
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

// shellQuote 单引号安全包裹：' → '\''（POSIX sh/fish/csh 皆兼容）
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
