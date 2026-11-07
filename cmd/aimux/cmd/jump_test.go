package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
)

func runJump(t *testing.T, agents []agent.Agent, f controller.Focuser, json bool, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	c := newJumpCmd(func() ([]agent.Agent, error) { return agents, nil }, f)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = json
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs(append([]string{"jump"}, args...))
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	err := rootCmd.Execute()
	return stdout.String(), err
}

func paneFocuser(where string) controller.Focuser {
	return controller.Focuser{
		TTYForPID: func(int) string { return "/dev/ttys031" },
		FocusTTY:  func(string) (string, bool, error) { return where, true, nil },
	}
}

func TestJumpCmd_FocusesByPrefix_JSON(t *testing.T) {
	agents := []agent.Agent{{PID: 4242, SessionID: "19c0ce61-aaaa", ProviderName: "claude"}}
	out, err := runJump(t, agents, paneFocuser("tab 2 · pane 1 of 1"), true, "19c0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var res controller.FocusResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !res.Focused || res.PID != 4242 || res.SessionID != "19c0ce61-aaaa" || res.Where != "tab 2 · pane 1 of 1" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestJumpCmd_Text(t *testing.T) {
	agents := []agent.Agent{{PID: 4242, SessionID: "19c0ce61-aaaa", ProviderName: "claude"}}
	out, err := runJump(t, agents, paneFocuser("tab 2 · pane 1 of 1"), false, "4242")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "tab 2 · pane 1 of 1") {
		t.Errorf("text output should say where, got %q", out)
	}
}

func TestJumpCmd_NotRunning(t *testing.T) {
	_, err := runJump(t, nil, paneFocuser(""), false, "19c0")
	if err == nil || !strings.Contains(err.Error(), "no running agent") {
		t.Fatalf("want not-running error, got %v", err)
	}
}

func TestJumpCmd_NoPane_TmuxHintInError(t *testing.T) {
	agents := []agent.Agent{{PID: 7, SessionID: "abc", TMuxSession: "aimux-codex"}}
	f := controller.Focuser{
		TTYForPID: func(int) string { return "" },
		FocusTTY:  func(string) (string, bool, error) { return "", false, nil },
	}
	_, err := runJump(t, agents, f, false, "abc")
	if err == nil || !strings.Contains(err.Error(), "tmux attach-session -t aimux-codex") {
		t.Fatalf("error should carry the tmux hint, got %v", err)
	}
}

func TestJumpCmd_RequiresOneArg(t *testing.T) {
	_, err := runJump(t, nil, paneFocuser(""), false)
	if err == nil {
		t.Fatal("jump without args should fail")
	}
}
