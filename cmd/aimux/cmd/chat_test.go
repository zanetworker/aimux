package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/zanetworker/aimux/internal/acprun"
)

func ev(kind string, v any) acprun.Event {
	data, _ := json.Marshal(v)
	return acprun.Event{Seq: 1, Kind: kind, Data: data}
}

func TestRenderChatEvent(t *testing.T) {
	var out, info bytes.Buffer
	renderChatEvent(&out, &info, ev(acprun.KindUpdate, acp.UpdateAgentMessageText("hello ")), false)
	renderChatEvent(&out, &info, ev(acprun.KindUpdate, acp.UpdateAgentMessageText("world")), false)
	renderChatEvent(&out, &info, ev(acprun.KindUpdate, acp.StartToolCall("c1", "Read main.go")), false)
	renderChatEvent(&out, &info, ev(acprun.KindUpdate, acp.UpdateAgentThoughtText("hmm")), false)
	if out.String() != "hello world" {
		t.Fatalf("stdout = %q, want only the agent's reply text", out.String())
	}
	if !strings.Contains(info.String(), "Read main.go") || strings.Contains(info.String(), "hmm") {
		t.Fatalf("stderr = %q, want the tool call and no thoughts", info.String())
	}
}

func TestRenderChatEvent_JSON(t *testing.T) {
	var out, info bytes.Buffer
	renderChatEvent(&out, &info, ev(acprun.KindUpdate, acp.UpdateAgentMessageText("hi")), true)
	var got acprun.Event
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil || got.Kind != acprun.KindUpdate {
		t.Fatalf("--json line %q: %v", out.String(), err)
	}
	if info.Len() != 0 {
		t.Fatalf("--json wrote to stderr: %q", info.String())
	}
}

func permReq() acp.RequestPermissionRequest {
	return acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{ToolCallId: "c1", Title: acp.Ptr("git push fork docs-fix")},
		Options: []acp.PermissionOption{
			{OptionId: "rej", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
			{OptionId: "ok", Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
		},
	}
}

func TestPromptApprover(t *testing.T) {
	tests := []struct {
		answer string
		wantID string
		wantOK bool
	}{
		{"y\n", "ok", true},
		{"yes\n", "ok", true},
		{"n\n", "", false},
		{"\n", "", false},
		{"", "", false}, // EOF
	}
	for _, tt := range tests {
		var out bytes.Buffer
		approve := promptApprover(strings.NewReader(tt.answer), &out)
		id, ok := approve(context.Background(), permReq())
		if id != tt.wantID || ok != tt.wantOK {
			t.Errorf("answer %q: got %q,%v want %q,%v", tt.answer, id, ok, tt.wantID, tt.wantOK)
		}
		if !strings.Contains(out.String(), "git push fork docs-fix") {
			t.Errorf("prompt %q does not show what is being approved", out.String())
		}
	}
}

func TestPromptApprover_NoAllowOption(t *testing.T) {
	req := permReq()
	req.Options = req.Options[:1]
	if _, ok := promptApprover(strings.NewReader("y\n"), &bytes.Buffer{})(context.Background(), req); ok {
		t.Fatal("approved a request that offers no allow option")
	}
}

func TestParseAdapter(t *testing.T) {
	cmd, args, err := parseAdapter("npx -y @agentclientprotocol/claude-agent-acp")
	if err != nil || cmd != "npx" || len(args) != 2 || args[1] != "@agentclientprotocol/claude-agent-acp" {
		t.Fatalf("got %q %v %v", cmd, args, err)
	}
	if _, _, err := parseAdapter("   "); err == nil {
		t.Fatal("empty adapter command accepted")
	}
}

func TestIsTerminal_DevNullIsNot(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if isTerminal(f) {
		t.Fatal("/dev/null reported as a terminal; approvals would be asked with nobody to answer")
	}
}

func TestDenyNoTerminal(t *testing.T) {
	var out bytes.Buffer
	id, ok := denyNoTerminal(&out)(context.Background(), permReq())
	if ok || id != "" || !strings.Contains(out.String(), "denied") || !strings.Contains(out.String(), "git push fork docs-fix") {
		t.Fatalf("got %q,%v and note %q; want a denial that says what was denied", id, ok, out.String())
	}
}

// Parallel tool calls ask for permission concurrently. Each prompt must be
// answered by the line typed after it, never by another prompt's answer.
func TestPromptApprover_ConcurrentRequestsAreSerialized(t *testing.T) {
	pr, pw := io.Pipe()
	var out syncBuffer
	approve := promptApprover(pr, &out)
	reqFor := func(title string) acp.RequestPermissionRequest {
		r := permReq()
		r.ToolCall.Title = acp.Ptr(title)
		return r
	}
	results := make(chan string, 2)
	for _, title := range []string{"rm -rf build", "git push"} {
		go func(title string) {
			id, ok := approve(context.Background(), reqFor(title))
			results <- fmt.Sprintf("%s=%v/%s", title, ok, id)
		}(title)
	}
	// Answer each prompt only once it is on screen: y to whichever came first.
	answered := map[string]string{}
	for len(answered) < 2 {
		time.Sleep(20 * time.Millisecond)
		screen := out.String()
		for _, title := range []string{"rm -rf build", "git push"} {
			if _, done := answered[title]; done || !strings.Contains(screen, "Allow "+title+"?") {
				continue
			}
			ans := "n"
			if len(answered) == 0 {
				ans = "y"
			}
			answered[title] = ans
			_, _ = pw.Write([]byte(ans + "\n"))
		}
		if strings.Count(screen, "Allow ") > len(answered)+1 {
			t.Fatalf("a second prompt appeared before the first was answered:\n%s", screen)
		}
	}
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		r := <-results
		got[r] = true
	}
	for title, ans := range answered {
		want := title + "=false/"
		if ans == "y" {
			want = title + "=true/ok"
		}
		if !got[want] {
			t.Fatalf("answers %v: %q should have been decided by %q", got, title, ans)
		}
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// "Allow X? [y/N]" approves this one call. It must never grant a standing
// permission, even when that is all the agent offers.
func TestPromptApprover_NeverGrantsAllowAlways(t *testing.T) {
	req := permReq()
	req.Options = []acp.PermissionOption{
		{OptionId: "rej", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
		{OptionId: "always", Name: "Always allow", Kind: acp.PermissionOptionKindAllowAlways},
	}
	var out bytes.Buffer
	id, ok := promptApprover(strings.NewReader("y\n"), &out)(context.Background(), req)
	if ok || id == "always" {
		t.Fatalf("y to a one-off prompt granted %q", id)
	}
	if !strings.Contains(out.String(), "denied") {
		t.Fatalf("no explanation for the denial: %q", out.String())
	}
}
