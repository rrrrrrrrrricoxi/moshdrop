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
		return fmt.Errorf(msg("err.auth"), line)
	case strings.Contains(low, "permission denied"):
		return fmt.Errorf(msg("err.fsperm"), line)
	case strings.Contains(low, "no space left"):
		return fmt.Errorf(msg("err.nospace"), line)
	case strings.Contains(low, "could not resolve hostname"):
		return fmt.Errorf(msg("err.resolve"), line)
	case strings.Contains(low, "connection refused"),
		strings.Contains(low, "no route to host"),
		strings.Contains(low, "network is unreachable"),
		strings.Contains(low, "connection timed out"),
		strings.Contains(low, "timed out"),
		strings.Contains(low, "connection closed"),
		strings.Contains(low, "broken pipe"):
		return fmt.Errorf(msg("err.network"), line)
	case strings.Contains(low, "controlpath too long"):
		return fmt.Errorf(msg("err.ctlpath"), line)
	case strings.Contains(low, "host key verification failed"):
		return fmt.Errorf(msg("err.hostkey"), line)
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
