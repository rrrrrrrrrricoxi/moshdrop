package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runDoctor 用与真实拖拽完全相同的代码链路做逐项体检。
func runDoctor(args []string) int {
	fail := 0
	step := func(name string, ok bool, detail string) {
		mark := "✓"
		if !ok {
			mark = "✗"
			fail++
		}
		fmt.Printf(" %s %-22s %s\n", mark, name, detail)
	}
	fmt.Println(msg("d.title"))

	// 1) mosh 本体
	moshBin := os.Getenv("MOSHDROP_CMD")
	if moshBin == "" {
		moshBin = "mosh"
	}
	if p, err := exec.LookPath(moshBin); err == nil {
		out, _ := exec.Command(p, "--version").CombinedOutput()
		step(msg("d.mosh"), true, p+"  "+firstLine(string(out)))
	} else {
		step(msg("d.mosh"), false, fmt.Sprintf(msg("d.moshmiss"), moshBin))
	}

	// 2) 目标推导
	target := os.Getenv("MOSHDROP_TARGET")
	sshArgv := []string{"ssh"}
	if t, sa, err := DeriveSSHTarget(args); err == nil {
		if target == "" {
			target = t
		}
		sshArgv = sa
	}
	if target == "" {
		step(msg("d.target"), false, msg("d.usage"))
		summary(fail)
		return 1
	}
	step(msg("d.target"), true, fmt.Sprintf(msg("d.via"), target, strings.Join(sshArgv, " ")))

	stateDir := stateDirPath()
	_ = os.MkdirAll(stateDir, 0o700)
	cfg := LoadConfig(stateDir, target)
	setLang(cfg.Lang)
	u := NewUploader(target, sshArgv, stateDir)
	u.ApplyConfig(cfg)
	defer u.Close() // 任何 early-return 都不留 ControlMaster 残连

	// 3) ControlPath 长度（unix socket 上限 104）
	cp := strings.Replace(u.ctlPath, "%C", strings.Repeat("x", 40), 1)
	step(msg("d.ctl"), len(cp) < 104, fmt.Sprintf("%d/104  %s", len(cp), u.ctlPath))

	// 4) 免密连通 + 远端目录
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t0 := time.Now()
	u.mu.Lock()
	err := u.ensure(ctx)
	u.mu.Unlock()
	if err != nil {
		step(msg("d.conn"), false, err.Error())
		fmt.Println(fmt.Sprintf(msg("d.hint"), target))
		summary(fail)
		return 1
	}
	step(msg("d.conn"), true, fmt.Sprintf(msg("d.connok"), time.Since(t0).Milliseconds(), u.remoteDir))

	// 5) 探针文件端到端（真上传 + sha256 校验 + 清理）
	probe := filepath.Join(os.TempDir(), fmt.Sprintf("moshdrop-probe-%d.txt", os.Getpid()))
	content := []byte("moshdrop-probe " + time.Now().Format(time.RFC3339Nano))
	_ = os.WriteFile(probe, content, 0o600)
	defer os.Remove(probe)
	remotes, err := u.Upload(ctx, []string{probe})
	if err != nil {
		step(msg("d.probe"), false, err.Error())
		summary(fail)
		return 1
	}
	sum := sha256.Sum256(content)
	// 路径引用嵌套：内层脚本单独构造后整体 shellQuote，含空格路径也正确
	hashScript := `shasum -a 256 ` + shellQuote(remotes[0]) + ` 2>/dev/null || sha256sum ` + shellQuote(remotes[0])
	res := u.run(ctx, nil, u.sshCmd("sh -c "+shellQuote(hashScript)))
	remoteSum := ""
	if f := strings.Fields(string(res.stdout)); len(f) > 0 {
		remoteSum = f[0]
	}
	step(msg("d.e2e"), remoteSum == hex.EncodeToString(sum[:]), remotes[0])
	u.run(ctx, nil, u.sshCmd("sh -c "+shellQuote("rm -f "+shellQuote(remotes[0]))))

	// 6) 最近一次失败留痕
	if hint := lastFailureHint(stateDir); hint != "" {
		fmt.Println(msg("d.lastfail") + hint)
	}
	summary(fail)
	if fail > 0 {
		return 1
	}
	return 0
}

func summary(fail int) {
	if fail == 0 {
		fmt.Println(msg("d.allpass"))
	} else {
		fmt.Printf(msg("d.nfail"), fail)
	}
}

// lastFailureHint 从事件日志里翻出最近一次失败，给排障线索。
func lastFailureHint(stateDir string) string {
	b, err := os.ReadFile(filepath.Join(stateDir, "events.log"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var ev dropEvent
		if json.Unmarshal([]byte(lines[i]), &ev) == nil && !ev.Ok {
			return fmt.Sprintf("%s  %v  %s", ev.Ts, ev.Files, ev.Err)
		}
	}
	return ""
}
