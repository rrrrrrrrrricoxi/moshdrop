package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 是 moshdrop 的全部可配置项（~/.moshdrop/config，简单 key = value 格式）。
type Config struct {
	TTLDays   int    // 远端文件保质期（天），0=不清理
	Intercept bool   // 拖拽拦截总开关；off = 真·纯透传
	Lang      string // 用户可见文案语言：""/en | zh
	RemoteDir string // 远端落点，$HOME 下相对路径

	MaxInterceptMB int // 拖拽自动上传的大小上限（MB），超过则放行本地路径+通知；0=不限制
}

func defaultConfig() Config {
	return Config{TTLDays: 7, Intercept: true, RemoteDir: ".moshdrop", MaxInterceptMB: 50}
}

// LoadConfig 读 stateDir/config：全局键 + host.<name>.<key> 覆盖。
// 未知键忽略（向后兼容）；文件不存在返回默认值。
func LoadConfig(stateDir, host string) Config {
	cfg := defaultConfig()
	b, err := os.ReadFile(filepath.Join(stateDir, "config"))
	if err != nil {
		return cfg
	}
	apply := func(key, val string) {
		switch key {
		case "ttl_days":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				cfg.TTLDays = n
			}
		case "intercept":
			cfg.Intercept = parseBool(val, cfg.Intercept)
		case "lang":
			cfg.Lang = val
		case "remote_dir":
			if safeRemoteDir(val) {
				cfg.RemoteDir = val
			}
		case "max_intercept_mb":
			// 上界防 int64(n)<<20 溢出成负数（会被误判为「不限制」）；1<<33 MB(≈8 PB) 远超任何真实文件。
			if n, err := strconv.Atoi(val); err == nil && n >= 0 && n < 1<<33 {
				cfg.MaxInterceptMB = n
			}
		}
	}
	hostPrefix := "host." + host + "."
	// 两轮：先全局，再 host 覆盖（顺序无关）
	for round := range 2 {
		for line := range strings.SplitSeq(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			eq := strings.IndexByte(line, '=')
			if eq < 0 {
				continue
			}
			key := strings.TrimSpace(line[:eq])
			val := strings.TrimSpace(stripInlineComment(line[eq+1:]))
			isHost := strings.HasPrefix(key, "host.")
			if round == 0 && !isHost {
				apply(key, val)
			}
			if round == 1 && isHost && strings.HasPrefix(key, hostPrefix) {
				apply(strings.TrimPrefix(key, hostPrefix), val)
			}
		}
	}
	return cfg
}

func parseBool(s string, def bool) bool {
	switch strings.ToLower(s) {
	case "on", "true", "1", "yes":
		return true
	case "off", "false", "0", "no":
		return false
	}
	return def
}

// stripInlineComment 剥除「空白 + #」起的行内注释（整行 # 注释在上面已跳过）。
// 让 README 里 `key = val   # 说明` 这种写法真正生效，而不是把注释并进值里。
func stripInlineComment(s string) string {
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			return s[:i]
		}
	}
	return s
}

// safeRemoteDir: 只允许 $HOME 下的相对路径，杜绝逃逸。
func safeRemoteDir(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == ".." || seg == "" {
			return false
		}
	}
	return true
}
