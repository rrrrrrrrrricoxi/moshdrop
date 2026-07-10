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
	if cfg.TTLDays != 7 || !cfg.Intercept || cfg.RemoteDir != ".moshdrop" || cfg.Lang != "" || cfg.MaxInterceptMB != 50 {
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

// 行内注释：value 后跟「空白 + #」注释必须被剥除（README 示例正是这种写法）。
func TestLoadConfigInlineComments(t *testing.T) {
	dir := writeConfig(t, "ttl_days = 30 # keep a month\nremote_dir = drops   # per-host style\nmax_intercept_mb = 100 # cap\nlang = zh # chinese\n")
	cfg := LoadConfig(dir, "x")
	if cfg.TTLDays != 30 {
		t.Fatalf("行内注释未剥除，ttl_days=%d", cfg.TTLDays)
	}
	if cfg.RemoteDir != "drops" {
		t.Fatalf("行内注释未剥除，remote_dir=%q", cfg.RemoteDir)
	}
	if cfg.MaxInterceptMB != 100 {
		t.Fatalf("行内注释未剥除，max_intercept_mb=%d", cfg.MaxInterceptMB)
	}
	if cfg.Lang != "zh" {
		t.Fatalf("行内注释未剥除，lang=%q", cfg.Lang)
	}
	// value 内部不带空白的 # 不算注释（不误伤）。
	dir2 := writeConfig(t, "remote_dir = a#b\n")
	if cfg := LoadConfig(dir2, "x"); cfg.RemoteDir != "a#b" {
		t.Fatalf("非注释的 # 被误剥，remote_dir=%q", cfg.RemoteDir)
	}
}

// max_intercept_mb：默认 50、全局解析、per-host 覆盖、0=不限制。
func TestLoadConfigMaxInterceptMB(t *testing.T) {
	if cfg := LoadConfig(t.TempDir(), "x"); cfg.MaxInterceptMB != 50 {
		t.Fatalf("默认 max_intercept_mb 应为 50: %+v", cfg)
	}
	dir := writeConfig(t, "max_intercept_mb = 200\nhost.ccc.max_intercept_mb = 0\n")
	if cfg := LoadConfig(dir, "other"); cfg.MaxInterceptMB != 200 {
		t.Fatalf("全局 max_intercept_mb 解析错误: %+v", cfg)
	}
	if cfg := LoadConfig(dir, "ccc"); cfg.MaxInterceptMB != 0 {
		t.Fatalf("host 覆盖 max_intercept_mb=0 错误: %+v", cfg)
	}
}

func TestLoadConfigPasteKey(t *testing.T) {
	if cfg := LoadConfig(t.TempDir(), "x"); !cfg.PasteKey {
		t.Fatalf("默认 paste_key 应为 on: %+v", cfg)
	}
	dir := writeConfig(t, "paste_key = off\nhost.ccc.paste_key = on\n")
	if cfg := LoadConfig(dir, "other"); cfg.PasteKey {
		t.Fatalf("全局 paste_key=off 解析错误: %+v", cfg)
	}
	if cfg := LoadConfig(dir, "ccc"); !cfg.PasteKey {
		t.Fatalf("host 覆盖 paste_key=on 错误: %+v", cfg)
	}
}
