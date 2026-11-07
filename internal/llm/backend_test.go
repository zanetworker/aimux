package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAPIBackend_SendsRequestAndReadsUsage(t *testing.T) {
	var got map[string]any
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = io.WriteString(w, `{"model":"claude-haiku-5-5","content":[{"type":"text","text":"{\"sessions\":[]}"}],"usage":{"input_tokens":640,"output_tokens":95}}`)
	}))
	defer srv.Close()
	b := &API{Key: "test-key", URL: srv.URL, Client: srv.Client()}
	reply, err := b.Complete(context.Background(), Request{Model: "claude-haiku-5-5", System: "sys", Prompt: "p", MaxTokens: 120})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != `{"sessions":[]}` || reply.InputTokens != 640 || reply.OutputTokens != 95 || reply.Estimated {
		t.Errorf("reply = %+v", reply)
	}
	if got["model"] != "claude-haiku-5-5" || got["system"] != "sys" || got["max_tokens"].(float64) != 120 {
		t.Errorf("request = %v", got)
	}
	if headers.Get("x-api-key") != "test-key" || headers.Get("anthropic-version") == "" {
		t.Error("auth headers missing")
	}
}

func TestAPIBackend_Errors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{404, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-haiku-5-5 ` + privateText + `"}}`, ErrModelNotFound},
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, ErrUnauthorized},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"` + privateText + `"}}`, ErrModelCall},
		{200, `not json ` + privateText, ErrBadReply},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		b := &API{Key: "k", URL: srv.URL, Client: srv.Client()}
		_, err := b.Complete(context.Background(), Request{Model: "m", Prompt: "p", MaxTokens: 60})
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), privateText) {
			t.Errorf("status %d: response text in error: %v", tc.status, err)
		}
	}
}

func TestCLIBackend_CommandIsIsolated(t *testing.T) {
	b := &CLI{Bin: "claude"}
	dir := t.TempDir()
	req := Request{Model: "claude-haiku-5-5", System: testSystem, Prompt: "Session 1 (title):\n" + privateText}
	cmd := b.command(context.Background(), dir, req)
	want := []string{"claude", "-p", "--safe-mode", "--model", "claude-haiku-5-5", "--output-format", "json",
		"--no-session-persistence", "--tools", "", "--strict-mcp-config",
		"--settings", `{"disableAllHooks":true}`, "--disable-slash-commands",
		"--system-prompt", testSystem}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("args =\n%q\nwant\n%q", cmd.Args, want)
	}
	if cmd.Dir != dir {
		t.Errorf("dir = %q, want the empty folder", cmd.Dir)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, privateText) {
			t.Error("session text on the command line (visible in ps); it goes on stdin")
		}
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if string(in) != req.Prompt {
		t.Errorf("stdin = %q", in)
	}
}

// fakeClaude writes a script standing in for the claude binary (never the
// real one): it reports how many files its working folder holds and prints
// out as its JSON result.
func fakeClaude(t *testing.T, out string, exit int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-claude")
	script := "#!/bin/sh\ncat >/dev/null\nn=$(ls -A | wc -l | tr -d ' ')\nprintf '%s' \"$(echo '" + out + "' | sed \"s/FILES/$n/\")\"\nexit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil { // #nosec G306 -- an executable test script
		t.Fatal(err)
	}
	return p
}

func TestCLIBackend_RunsInAnEmptyFolderAndReadsUsage(t *testing.T) {
	bin := fakeClaude(t, `{"type":"result","is_error":false,"result":"files=FILES","usage":{"input_tokens":12,"cache_creation_input_tokens":100,"cache_read_input_tokens":3,"output_tokens":7},"total_cost_usd":0.002}`, 0)
	b := &CLI{Bin: bin, Timeout: cliTestTimeout}
	reply, err := b.Complete(context.Background(), Request{Model: "haiku", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "files=0" {
		t.Errorf("text = %q, want the run in an empty folder", reply.Text)
	}
	if reply.InputTokens != 115 || reply.OutputTokens != 7 || reply.CostUSD == nil || *reply.CostUSD != 0.002 || reply.Estimated {
		t.Errorf("reply = %+v", reply)
	}
}

func TestCLIBackend_EstimatesWithoutUsage(t *testing.T) {
	bin := fakeClaude(t, `{"type":"result","result":"abcdefghijklmnop"}`, 0)
	b := &CLI{Bin: bin, Timeout: cliTestTimeout}
	reply, err := b.Complete(context.Background(), Request{Model: "haiku", System: strings.Repeat("s", 40), Prompt: strings.Repeat("p", 360)})
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Estimated || reply.InputTokens != 100 || reply.OutputTokens != 4 {
		t.Errorf("reply = %+v, want estimated 100 in, 4 out", reply)
	}
}

func TestCLIBackend_Failures(t *testing.T) {
	bin := fakeClaude(t, `{"type":"result","is_error":true,"result":"`+privateText+`"}`, 0)
	_, err := (&CLI{Bin: bin, Timeout: cliTestTimeout}).Complete(context.Background(), Request{Prompt: "p"})
	if !errors.Is(err, ErrModelCall) || strings.Contains(err.Error(), privateText) {
		t.Errorf("is_error: err = %v", err)
	}
	bin = fakeClaude(t, privateText, 3)
	_, err = (&CLI{Bin: bin, Timeout: cliTestTimeout}).Complete(context.Background(), Request{Prompt: "p"})
	if err == nil || strings.Contains(err.Error(), privateText) {
		t.Errorf("exit 3: err = %v", err)
	}
	_, err = (&CLI{Bin: "aimux-no-such-claude-binary", Timeout: time.Second}).Complete(context.Background(), Request{Prompt: "p"})
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("missing binary: err = %v, want exec.ErrNotFound", err)
	}
}

func TestCLIBackend_RemovesItsFolder(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	bin := fakeClaude(t, `{"result":"x"}`, 0)
	if _, err := (&CLI{Bin: bin, Timeout: cliTestTimeout}).Complete(context.Background(), Request{Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(tmp)
	if len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestCLIBackend_TimeoutKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := filepath.Join(dir, "fake-claude-hang")
	script := "#!/bin/sh\ncat >/dev/null\nsleep 30 &\necho $! > " + pidFile + "\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- an executable test script
		t.Fatal(err)
	}
	start := time.Now()
	_, err := (&CLI{Bin: bin, Timeout: 1500 * time.Millisecond}).Complete(context.Background(), Request{Prompt: "p"})
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("Complete took %v: a child holding stdout kept it waiting", elapsed)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
	data, rerr := os.ReadFile(pidFile) // #nosec G304 -- a test temp file
	if rerr != nil {
		t.Fatal(rerr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Error("the child survived the timeout")
	}
}

// testSystem stands for a caller's system prompt.
const testSystem = "You name coding sessions."

// cliTestTimeout bounds each fake claude run. A new script's first run can be
// slow on a busy Mac (macOS checks a new executable before running it), so the
// tests allow more than the real 60 s; the timeout tests use their own value.
const cliTestTimeout = 30 * time.Second

// TestAPI_NoSystemFieldWithoutSystemPrompt: callers without a system prompt
// (titles, insight) send model, max_tokens and messages only.
func TestAPI_NoSystemFieldWithoutSystemPrompt(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"a"},{"type":"tool_use"},{"type":"text","text":"b"}]}`)
	}))
	defer srv.Close()
	reply, err := (&API{Key: "k", URL: srv.URL, Client: srv.Client()}).Complete(context.Background(), Request{Model: "m", Prompt: "p", MaxTokens: 30})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["system"]; ok || len(got) != 3 {
		t.Errorf("request = %v, want no system field", got)
	}
	if reply.Text != "ab" || reply.Model != "" {
		t.Errorf("reply = %+v, want the text blocks joined", reply)
	}
}

func TestNewAPI(t *testing.T) {
	a := NewAPI("k", "http://u", 30*time.Second)
	if a.Key != "k" || a.URL != "http://u" || a.Client.Timeout != 30*time.Second {
		t.Errorf("NewAPI = %+v", a)
	}
}

// TestCLI_TempFolderPrefix: each run's empty folder is named aimux-llm-*.
func TestCLI_TempFolderPrefix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fake-claude-pwd")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '{\"result\":\"%s\"}' \"$(basename \"$(pwd)\")\"\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil { // #nosec G306 -- an executable test script
		t.Fatal(err)
	}
	reply, err := (&CLI{Bin: p, Timeout: cliTestTimeout}).Complete(context.Background(), Request{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reply.Text, "aimux-llm-") {
		t.Errorf("folder = %q, want aimux-llm-*", reply.Text)
	}
}
