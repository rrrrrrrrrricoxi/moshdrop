package main

import (
	"strings"
	"testing"
)

func TestDeriveSSHTarget(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    string
		wantSSH string
		ok      bool
	}{
		{"plain host", []string{"ccc"}, "ccc", "ssh", true},
		{"user@host", []string{"rico@example.com"}, "rico@example.com", "ssh", true},
		{"with remote command", []string{"ccc", "--", "tmux", "new", "-A", "-s", "main"}, "ccc", "ssh", true},
		{"ssh inline", []string{"--ssh=ssh -p 2222", "ccc"}, "ccc", "ssh -p 2222", true},
		{"ssh separate", []string{"--ssh", "ssh -J jump", "ccc"}, "ccc", "ssh -J jump", true},
		{"predict inline", []string{"--predict=always", "ccc"}, "ccc", "ssh", true},
		{"predict separate (审计C2回归)", []string{"--predict", "always", "ccc"}, "ccc", "ssh", true},
		{"short value flag -p", []string{"-p", "60000", "ccc"}, "ccc", "ssh", true},
		{"short bool flag", []string{"-a", "ccc"}, "ccc", "ssh", true},
		{"port separate", []string{"--port", "60000", "ccc"}, "ccc", "ssh", true},
		{"no host", []string{"--predict=always"}, "", "", false},
		{"only separator", []string{"--", "cmd"}, "", "", false},
	}
	for _, c := range cases {
		got, sshArgv, err := DeriveSSHTarget(c.args)
		if c.ok {
			if err != nil || got != c.want {
				t.Errorf("%s: got (%q,%v), want %q", c.name, got, err, c.want)
			}
			if joined := strings.Join(sshArgv, " "); joined != c.wantSSH {
				t.Errorf("%s: sshArgv=%q want %q", c.name, joined, c.wantSSH)
			}
		} else if err == nil {
			t.Errorf("%s: expected error, got %q", c.name, got)
		}
	}
}
