package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestModelProxyServeCommand(t *testing.T) {
	t.Run("help", func(t *testing.T) {
		cmd := newModelCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"proxy", "serve", "--help"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"--port", "7385", "--codex-command", "127.0.0.1"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("help missing %q: %s", want, &out)
			}
		}
	})
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"invalid port", []string{"--port", "-1"}, "listen on 127.0.0.1:-1"},
		{"unexpected argument", []string{"unused"}, "unknown command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newModelCmd()
			cmd.SetArgs(append([]string{"proxy", "serve"}, tc.args...))
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
