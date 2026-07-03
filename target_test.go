package main

import "testing"

func TestDeriveSSHTarget(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{"plain host", []string{"ccc"}, "ccc", true},
		{"user@host", []string{"rico@example.com"}, "rico@example.com", true},
		{"with remote command", []string{"ccc", "--", "tmux", "new", "-A", "-s", "main"}, "ccc", true},
		{"long flag with =", []string{"--ssh=ssh -p 2222", "ccc"}, "ccc", true},
		{"predict flag", []string{"--predict=always", "ccc"}, "ccc", true},
		{"short value flag -p", []string{"-p", "60000", "ccc"}, "ccc", true},
		{"short bool flag", []string{"-a", "ccc"}, "ccc", true},
		{"no host", []string{"--predict=always"}, "", false},
		{"only separator", []string{"--", "cmd"}, "", false},
	}
	for _, c := range cases {
		got, err := DeriveSSHTarget(c.args)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("%s: got (%q,%v), want %q", c.name, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error, got %q", c.name, got)
		}
	}
}
