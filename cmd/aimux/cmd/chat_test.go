package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

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
