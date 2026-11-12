package profilerun

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

const (
	fakeAgentEnv  = "AIMUX_PROFILERUN_FAKE_AGENT"
	fakeRecordEnv = "AIMUX_PROFILERUN_FAKE_RECORD" // file: one JSON line per request
)

// TestMain lets the test binary act as an ACP agent, so the page engine
// tests see what really goes over the wire.
func TestMain(m *testing.M) {
	if os.Getenv(fakeAgentEnv) == "1" {
		a := &fakeAgent{models: map[acp.SessionId]string{}}
		a.conn = acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
		<-a.conn.Done()
		return
	}
	os.Exit(m.Run())
}

type fakeAgent struct {
	conn   *acp.AgentSideConnection
	mu     sync.Mutex
	models map[acp.SessionId]string
	n      int
}

func (a *fakeAgent) record(method string, meta map[string]any, value string) {
	path := os.Getenv(fakeRecordEnv)
	if path == "" {
		return
	}
	line, _ := json.Marshal(map[string]any{"method": method, "meta": meta, "value": value})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: path is a test temp file
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func (a *fakeAgent) options(id acp.SessionId) []acp.SessionConfigOption {
	opts := acp.SessionConfigSelectOptionsUngrouped{}
	for _, m := range []string{"default", "opus", "sonnet"} {
		opts = append(opts, acp.SessionConfigSelectOption{Name: m, Value: acp.SessionConfigValueId(m)})
	}
	cat := acp.SessionConfigOptionCategoryModel
	return []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{Id: "model", Name: "Model", Type: "select", Category: &cat,
		CurrentValue: acp.SessionConfigValueId(a.models[id]), Options: acp.SessionConfigSelectOptions{Ungrouped: &opts}}}}
}

func (a *fakeAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber, AgentCapabilities: acp.AgentCapabilities{LoadSession: true}}, nil
}
func (a *fakeAgent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.record("session/new", p.Meta, "")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	id := acp.SessionId("page-" + string(rune('0'+a.n))) //nolint:gosec // G115: a.n is a small test counter (< 10)
	a.models[id] = "opus"                                // like claude-agent-acp 0.101.0
	return acp.NewSessionResponse{SessionId: id, ConfigOptions: a.options(id)}, nil
}
func (a *fakeAgent) LoadSession(_ context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.record("session/load", p.Meta, "")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.models[p.SessionId] = "opus"
	return acp.LoadSessionResponse{ConfigOptions: a.options(p.SessionId)}, nil
}
func (a *fakeAgent) SetSessionConfigOption(_ context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	v := string(p.ValueId.Value)
	a.record("session/set_config_option", nil, v)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.models[p.ValueId.SessionId] = v
	return acp.SetSessionConfigOptionResponse{ConfigOptions: a.options(p.ValueId.SessionId)}, nil
}
func (a *fakeAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	text := ""
	if len(p.Prompt) > 0 && p.Prompt[0].Text != nil {
		text = p.Prompt[0].Text.Text
	}
	a.record("session/prompt", nil, text)
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText("answer")})
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}
func (a *fakeAgent) Cancel(context.Context, acp.CancelNotification) error { return nil }
func (a *fakeAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}
func (a *fakeAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}
func (a *fakeAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}
func (a *fakeAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}
func (a *fakeAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}
func (a *fakeAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

type recorded struct {
	Method string         `json:"method"`
	Meta   map[string]any `json:"meta"`
	Value  string         `json:"value"`
}

func readRecord(t *testing.T, path string) []recorded {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: path is a test temp or fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	var out []recorded
	dec := json.NewDecoder(bytesReader(b))
	for dec.More() {
		var r recorded
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}
