package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg := LoadConfig(t.TempDir(), "ccc") // 无配置文件
	if cfg.TTLDays != 7 || !cfg.Intercept || cfg.RemoteDir != ".moshdrop" || cfg.Lang != "" {
		t.Fatalf("默认值错误: %+v", cfg)
	}
}

func TestLoadConfigGlobalAndHostOverride(t *testing.T) {
	dir := writeConfig(t, `
# 注释行
ttl_days = 30
lang = zh
intercept = on

host.ccc.intercept = off
host.ccc.remote_dir = drops
host.other.ttl_days = 1
`)
	ccc := LoadConfig(dir, "ccc")
	if ccc.TTLDays != 30 || ccc.Intercept || ccc.RemoteDir != "drops" || ccc.Lang != "zh" {
		t.Fatalf("host 覆盖错误: %+v", ccc)
	}
	other := LoadConfig(dir, "other")
	if other.TTLDays != 1 || !other.Intercept || other.RemoteDir != ".moshdrop" {
		t.Fatalf("其它 host: %+v", other)
	}
}

func TestLoadConfigBoolFormsAndUnknownKeys(t *testing.T) {
	dir := writeConfig(t, "intercept = false\nfuture_key = whatever\nttl_days = 0\n")
	cfg := LoadConfig(dir, "x")
	if cfg.Intercept || cfg.TTLDays != 0 {
		t.Fatalf("bool/零值解析错误: %+v", cfg)
	}
}

// 安全：remote_dir 不得逃逸 $HOME。
func TestLoadConfigRemoteDirEscape(t *testing.T) {
	for _, bad := range []string{"/etc", "../up", "a/../../b", ""} {
		dir := writeConfig(t, "remote_dir = "+bad+"\n")
		cfg := LoadConfig(dir, "x")
		if cfg.RemoteDir != ".moshdrop" {
			t.Fatalf("危险 remote_dir %q 未回退默认: %+v", bad, cfg)
		}
	}
	dir := writeConfig(t, "remote_dir = inbox/shots\n")
	if cfg := LoadConfig(dir, "x"); cfg.RemoteDir != "inbox/shots" {
		t.Fatalf("合法子路径被误拒: %+v", cfg)
	}
}
