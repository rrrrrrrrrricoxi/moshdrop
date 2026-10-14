package main

import (
	"encoding/json"
	"os/exec"
	"reflect"
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
		{"ssh inline after host", []string{"ccc", "--ssh=ssh -p 2222", "--", "tmux"}, "ccc", "ssh -p 2222", true},
		{"ssh separate after host", []string{"ccc", "--ssh", "ssh -J jump", "--", "tmux"}, "ccc", "ssh -J jump", true},
		{"last ssh option wins", []string{"--ssh=ssh -p 2222", "ccc", "--ssh=ssh -p 3333"}, "ccc", "ssh -p 3333", true},
		{"remote command opaque", []string{"ccc", "--", "tmux", "--ssh=ssh -p 2222"}, "ccc", "ssh", true},
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

func TestSSHWordsMatchesMoshPerlParser(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("Perl is needed only for the independent parser comparison")
	}
	commands := []string{
		`ssh -p 2222`,
		`"/path with spaces/ssh" -o 'ProxyCommand=ssh -W %h:%p jump'`,
		`ssh -i /path\ with\ spaces/key`,
		`ssh "a\q" "a\$b" "a\\b"`,
		`ssh 'a\b' 'a\'b' 'a\\b'`,
		`ssh "" pre"middle words"post`,
	}
	for _, command := range commands {
		out, err := exec.Command("perl", "-MText::ParseWords", "-MJSON::PP", "-e", `print encode_json([shellwords($ARGV[0])]);`, "--", command).Output()
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		if err := json.Unmarshal(out, &want); err != nil {
			t.Fatal(err)
		}
		got, err := splitSSHWords(command)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("SSH command %q: got %q/%v; Mosh shellwords gives %q", command, got, err, want)
		}
	}
}

func TestDeriveSSHQuotedWords(t *testing.T) {
	args := []string{"host", `--ssh="/path with spaces/ssh" -o 'ProxyCommand=ssh -W %h:%p jump' -i /path\ with\ spaces/key`, "--", "tmux"}
	target, argv, err := DeriveSSHTarget(args)
	want := []string{"/path with spaces/ssh", "-o", "ProxyCommand=ssh -W %h:%p jump", "-i", "/path with spaces/key"}
	if err != nil || target != "host" || !reflect.DeepEqual(argv, want) {
		t.Fatalf("route changed: %q %q %v", target, argv, err)
	}
	for _, command := range []string{"", `ssh 'unfinished`, `ssh trailing\`} {
		if _, _, err := DeriveSSHTarget([]string{"host", "--ssh=" + command}); err == nil {
			t.Fatalf("accepted invalid SSH command %q", command)
		}
	}
}
