//go:build integration

package acprun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// TestClaudeACP_ContinuesARealSession needs Claude Code, npx and model
// access. To run it, set the AIMUX_ACP_INTEGRATION environment variable to 1
// and run go test with -tags integration on this package.
func TestClaudeACP_ContinuesARealSession(t *testing.T) {
	if os.Getenv("AIMUX_ACP_INTEGRATION") != "1" {
		t.Skip("set AIMUX_ACP_INTEGRATION=1 to run against the real claude-agent-acp")
	}
	dir := t.TempDir()
	env := withoutClaudeCode(os.Environ())

	// 1. A normal Claude Code session, started the way a user would.
	first := exec.Command("claude", "-p", "--output-format", "json")
	first.Dir, first.Env = dir, env
	first.Stdin = strings.NewReader("Remember the codeword ZEBRA-42. Reply with only OK.")
	out, err := first.Output()
	if err != nil {
		t.Fatalf("claude -p: %v", err)
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.SessionID == "" {
		t.Fatalf("no session id in %s", out)
	}
	sessionFile := findSessionFile(t, res.SessionID)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(sessionFile)) })
	before := countLines(t, sessionFile)

	// 2. Continue it through ACP.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	r, err := Start(ctx, Spec{Command: "npx", Args: []string{"-y", "@agentclientprotocol/claude-agent-acp"}, Cwd: dir, Env: []string{"CLAUDECODE="}},
		res.SessionID, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Close() }()
	var events []Event
	stop, err := r.Prompt(ctx, "What was the codeword? Reply with only the codeword.", func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	reply := messageText(t, events)
	t.Logf("stop=%s reply=%q events=%d", stop, reply, len(events))
	if !strings.Contains(reply, "ZEBRA-42") {
		t.Fatalf("reply %q does not contain the codeword: context was not loaded", reply)
	}
	_ = r.Close()

	// 3. The same session file grew.
	if after := countLines(t, sessionFile); after <= before {
		t.Fatalf("session file did not grow (%d -> %d lines)", before, after)
	}

	// 4. Plain Claude Code continues the conversation, including the ACP turn.
	resume := exec.Command("claude", "-p", "--resume", res.SessionID)
	resume.Dir, resume.Env = dir, env
	resume.Stdin = strings.NewReader("Quote the exact question I asked you just before this one.")
	quoted, err := resume.Output()
	if err != nil {
		t.Fatalf("claude --resume: %v", err)
	}
	t.Logf("resume reply: %s", bytes.TrimSpace(quoted))
	if !strings.Contains(strings.ToLower(string(quoted)), "codeword") {
		t.Fatalf("claude --resume did not see the ACP turn: %s", quoted)
	}
}

func withoutClaudeCode(env []string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CLAUDECODE=") {
			out = append(out, kv)
		}
	}
	return out
}

func findSessionFile(t *testing.T, id string) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", id+".jsonl"))
	if len(matches) != 1 {
		t.Fatalf("found %d session files for %s", len(matches), id)
	}
	return matches[0]
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- test reads its own session file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	n := 0
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 64<<20)
	for s.Scan() {
		n++
	}
	return n
}

// TestClaudeACP_ApprovalDecidesToolUse checks the permission relay against
// the real adapter: an allowed shell command runs, a denied one does not.
func TestClaudeACP_ApprovalDecidesToolUse(t *testing.T) {
	if os.Getenv("AIMUX_ACP_INTEGRATION") != "1" {
		t.Skip("set AIMUX_ACP_INTEGRATION=1 to run against the real claude-agent-acp")
	}
	dir := t.TempDir()
	first := exec.Command("claude", "-p", "--output-format", "json")
	first.Dir, first.Env = dir, withoutClaudeCode(os.Environ())
	first.Stdin = strings.NewReader("Reply with only OK.")
	out, err := first.Output()
	if err != nil {
		t.Fatalf("claude -p: %v", err)
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.SessionID == "" {
		t.Fatalf("no session id in %s", out)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(findSessionFile(t, res.SessionID))) })

	turn := func(allow bool, file string) int {
		t.Helper()
		asked := 0
		approve := func(ctx context.Context, req acp.RequestPermissionRequest) (string, bool) {
			asked++
			if !allow {
				return "", false
			}
			for _, o := range req.Options {
				if o.Kind == acp.PermissionOptionKindAllowOnce {
					return string(o.OptionId), true
				}
			}
			return "", false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		r, err := Start(ctx, Spec{Command: "npx", Args: []string{"-y", "@agentclientprotocol/claude-agent-acp"}, Cwd: dir}, res.SessionID, approve)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer func() { _ = r.Close() }()
		if _, err := r.Prompt(ctx, "Run exactly this shell command and nothing else: touch "+file, func(Event) {}); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		return asked
	}

	if asked := turn(true, "allowed.txt"); asked == 0 {
		t.Fatal("the approver was never asked; the command may have been allowed by a settings rule")
	}
	if _, err := os.Stat(filepath.Join(dir, "allowed.txt")); err != nil {
		t.Fatalf("allowed command did not run: %v", err)
	}
	if asked := turn(false, "denied.txt"); asked == 0 {
		t.Fatal("the approver was never asked for the denied command")
	}
	if _, err := os.Stat(filepath.Join(dir, "denied.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied command ran anyway (stat err %v)", err)
	}
}
