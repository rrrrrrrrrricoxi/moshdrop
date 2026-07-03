package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestClassifySSHError(t *testing.T) {
	base := fmt.Errorf("exit status 255")
	cases := []struct {
		stderr string
		want   string
	}{
		{"user@h: Permission denied (publickey).", "密钥"},
		{"scp: /x: No space left on device", "磁盘已满"},
		{"ssh: Could not resolve hostname ccx", "主机名"},
		{"ssh: connect to host ccc port 22: Connection timed out", "网络"},
		{"ssh: connect to host ccc port 22: Connection refused", "网络"},
		{"Host key verification failed.", "指纹"},
	}
	for _, c := range cases {
		got := classifySSHError(base, []byte(c.stderr)).Error()
		if !strings.Contains(got, c.want) {
			t.Errorf("stderr=%q → %q，未含 %q", c.stderr, got, c.want)
		}
		if !strings.Contains(got, firstLine(c.stderr)) {
			t.Errorf("必须保留原文首行便于排障: %q", got)
		}
	}
	// 空 stderr 原样返回
	if classifySSHError(base, nil) != base {
		t.Error("空 stderr 应返回原 error")
	}
}
