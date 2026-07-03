package main

import (
	"fmt"
	"strings"
)

// mosh 中取"独立值参数"的短旗标（如 mosh -p 60000 ccc）。
var moshValueFlags = map[string]bool{
	"-p": true,
}

// DeriveSSHTarget 从 mosh 风格参数中提取 [user@]host：
// 遇 "--" 停止；跳过旗标（--x / --x=y / -x）及已知取值旗标的值；
// 第一个裸词即目标。
func DeriveSSHTarget(args []string) (string, error) {
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			if moshValueFlags[a] {
				skipNext = true
			}
			continue
		}
		return a, nil
	}
	return "", fmt.Errorf("在参数中找不到目标主机: %v", args)
}
