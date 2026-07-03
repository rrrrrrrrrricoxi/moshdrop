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
	fmt.Println("moshdrop doctor")

	// 1) mosh 本体
	moshBin := os.Getenv("MOSHDROP_CMD")
	if moshBin == "" {
		moshBin = "mosh"
	}
	if p, err := exec.LookPath(moshBin); err == nil {
		out, _ := exec.Command(p, "--version").CombinedOutput()
		step("mosh 可执行", true, p+"  "+firstLine(string(out)))
	} else {
		step("mosh 可执行", false, "找不到 "+moshBin+"（brew install mosh）")
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
		step("ssh 目标", false, "用法: moshdrop doctor <host>")
		summary(fail)
		return 1
	}
	step("ssh 目标", true, fmt.Sprintf("%s（通道: %s）", target, strings.Join(sshArgv, " ")))

	stateDir := stateDirPath()
	_ = os.MkdirAll(stateDir, 0o700)
	u := NewUploader(target, sshArgv, stateDir)

	// 3) ControlPath 长度（unix socket 上限 104）
	cp := strings.Replace(u.ctlPath, "%C", strings.Repeat("x", 40), 1)
	step("ControlPath 长度", len(cp) < 104, fmt.Sprintf("%d/104  %s", len(cp), u.ctlPath))

	// 4) 免密连通 + 远端目录
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t0 := time.Now()
	u.mu.Lock()
	err := u.ensure(ctx)
	u.mu.Unlock()
	if err != nil {
		step("ssh 免密连通", false, err.Error())
		fmt.Println("   提示: 先手动跑一次  ssh " + target + " true  确认免密可用")
		summary(fail)
		return 1
	}
	step("ssh 免密连通", true, fmt.Sprintf("%d ms，远端目录 %s", time.Since(t0).Milliseconds(), u.remoteDir))

	// 5) 探针文件端到端（真上传 + sha256 校验 + 清理）
	probe := filepath.Join(os.TempDir(), fmt.Sprintf("moshdrop-probe-%d.txt", os.Getpid()))
	content := []byte("moshdrop-probe " + time.Now().Format(time.RFC3339Nano))
	_ = os.WriteFile(probe, content, 0o600)
	defer os.Remove(probe)
	remotes, err := u.Upload(ctx, []string{probe})
	if err != nil {
		step("探针上传", false, err.Error())
		summary(fail)
		return 1
	}
	sum := sha256.Sum256(content)
	res := u.run(ctx, nil, u.sshCmd(`sh -c 'shasum -a 256 `+shellQuote(remotes[0])+` 2>/dev/null || sha256sum `+shellQuote(remotes[0])+`'`))
	remoteSum := ""
	if f := strings.Fields(string(res.stdout)); len(f) > 0 {
		remoteSum = f[0]
	}
	step("探针端到端校验", remoteSum == hex.EncodeToString(sum[:]), remotes[0])
	u.run(ctx, nil, u.sshCmd(`sh -c 'rm -f `+shellQuote(remotes[0])+`'`))
	u.Close()

	// 6) 最近一次失败留痕
	if hint := lastFailureHint(stateDir); hint != "" {
		fmt.Println("   最近一次失败: " + hint)
	}
	summary(fail)
	if fail > 0 {
		return 1
	}
	return 0
}

func summary(fail int) {
	if fail == 0 {
		fmt.Println("全部通过 — 拖拽链路健康。")
	} else {
		fmt.Printf("%d 项未通过。\n", fail)
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
