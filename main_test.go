package main

import "testing"

func TestCommandNeedsRuntime(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "root help", args: []string{"--help"}},
		{name: "default session", want: true},
		{name: "version", args: []string{"version"}},
		{name: "run help", args: []string{"run", "--help"}},
		{name: "run message named version", args: []string{"run", "version"}, want: true},
		{name: "daemon", args: []string{"daemon"}, want: true},
		{name: "daemon help", args: []string{"daemon", "--help"}},
		{name: "session", args: []string{"session"}, want: true},
		{name: "session list", args: []string{"session", "list"}},
		{name: "session resume", args: []string{"session", "session-a"}, want: true},
		{name: "config", args: []string{"config", "show"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := commandNeedsRuntime(test.args); got != test.want {
				t.Fatalf("commandNeedsRuntime(%v) = %v, want %v", test.args, got, test.want)
			}
		})
	}
}
