package main

import (
	"os"
	"testing"
)

// TestMain: 整个测试二进制全局静音通知——
// 任何模拟故障的用例都绝不允许骚扰开发者/用户的真实通知中心。
func TestMain(m *testing.M) {
	os.Setenv("MOSHDROP_MUTE_NOTIFY", "1")
	os.Exit(m.Run())
}
