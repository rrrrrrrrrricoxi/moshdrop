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
	setSSH := func(v string) error {
		fields, parseErr := splitSSHWords(v)
		if parseErr != nil || len(fields) == 0 || fields[0] == "" {
			return fmt.Errorf("invalid --ssh command: %q", v)
		}
		sshArgv = fields
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				if a[:eq] == "--ssh" {
					if err = setSSH(a[eq+1:]); err != nil {
						return "", sshArgv, err
					}
				}
				continue
			}
			if moshValueFlags[a] && i+1 < len(args) {
				if a == "--ssh" {
					if err = setSSH(args[i+1]); err != nil {
						return "", sshArgv, err
					}
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
		if target == "" {
			target = a
		}
	}
	if target != "" {
		return target, sshArgv, nil
	}
	return "", sshArgv, fmt.Errorf("在参数中找不到目标主机: %v", args)
}

// 与 mosh 的 shellwords 一样，保留引号内的空格并处理反斜线；不执行 shell。
func splitSSHWords(command string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	started, escaped := false, false
	for _, r := range command {
		if escaped {
			if quote == '\'' {
				word.WriteRune('\\')
			}
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote, started = r, true
		case ' ', '\t', '\n', '\r', '\v', '\f':
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unfinished quote or escape")
	}
	if started {
		words = append(words, word.String())
	}
	return words, nil
}
