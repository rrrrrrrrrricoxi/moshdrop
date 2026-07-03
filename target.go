package main

import (
	"fmt"
	"strings"
)

// 取独立值参数的旗标（两段式 `--opt value` 与 `-p value`）。
// mosh 的长选项同时支持 `--opt=value` 与 `--opt value` 两种写法（getopt_long）。
var moshValueFlags = map[string]bool{
	"-p":                       true,
	"--port":                   true,
	"--ssh":                    true,
	"--predict":                true,
	"--family":                 true,
	"--client":                 true,
	"--server":                 true,
	"--bind-server":            true,
	"--experimental-remote-ip": true,
}

// DeriveSSHTarget 从 mosh 风格参数提取 [user@]host 与上传通道的 ssh 命令基座。
// 基座默认 ["ssh"]；若用户给了 --ssh="ssh -p 2222"，上传通道沿用同一命令，
// 保证与 mosh 登录走完全相同的端口/跳板配置（否则可能传错机器）。
func DeriveSSHTarget(args []string) (target string, sshArgv []string, err error) {
	sshArgv = []string{"ssh"}
	setSSH := func(v string) {
		if fields := strings.Fields(v); len(fields) > 0 {
			sshArgv = fields
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				if a[:eq] == "--ssh" {
					setSSH(a[eq+1:])
				}
				continue
			}
			if moshValueFlags[a] && i+1 < len(args) {
				if a == "--ssh" {
					setSSH(args[i+1])
				}
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			if moshValueFlags[a] && i+1 < len(args) {
				i++
			}
			continue
		}
		return a, sshArgv, nil
	}
	return "", sshArgv, fmt.Errorf("在参数中找不到目标主机: %v", args)
}
