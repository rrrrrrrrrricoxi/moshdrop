package main

import (
	"fmt"
	"strings"
)

// classifySSHError 把 ssh 的原始报错翻译成用户可行动的人话，并保留首行原文便于排障。
func classifySSHError(err error, stderr []byte) error {
	s := strings.TrimSpace(string(stderr))
	low := strings.ToLower(s)
	line := firstLine(s)
	switch {
	case strings.Contains(low, "permission denied (publickey") ||
		strings.Contains(low, "permission denied (password") ||
		strings.Contains(low, "permission denied (keyboard"):
		return fmt.Errorf("远端拒绝了 ssh 登录（免密密钥失效？）— %s", line)
	case strings.Contains(low, "permission denied"):
		return fmt.Errorf("远端文件权限不足（目录不可写？）— %s", line)
	case strings.Contains(low, "no space left"):
		return fmt.Errorf("远端磁盘已满 — %s", line)
	case strings.Contains(low, "could not resolve hostname"):
		return fmt.Errorf("解析不了主机名 — %s", line)
	case strings.Contains(low, "connection refused"),
		strings.Contains(low, "no route to host"),
		strings.Contains(low, "network is unreachable"),
		strings.Contains(low, "connection timed out"),
		strings.Contains(low, "timed out"),
		strings.Contains(low, "connection closed"),
		strings.Contains(low, "broken pipe"):
		return fmt.Errorf("连不上远端（网络问题）— %s", line)
	case strings.Contains(low, "controlpath too long"):
		return fmt.Errorf("ControlPath 过长（请提 issue）— %s", line)
	case strings.Contains(low, "host key verification failed"):
		return fmt.Errorf("主机指纹校验失败（先手动 ssh 一次确认指纹）— %s", line)
	}
	if s != "" {
		return fmt.Errorf("%v — %s", err, line)
	}
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
