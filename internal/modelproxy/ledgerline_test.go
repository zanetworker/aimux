package modelproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/ledger"
)

// ledgerProxy is a handler over the fake Codex app-server that writes its ledger into a temp folder.
type ledgerProxy struct {
	handler http.Handler
	dir     string
	writer  *ledger.Writer
}

func newLedgerProxy(t *testing.T, options ...HandlerOption) ledgerProxy {
	t.Helper()
	backend, _ := newFakeBackend(t)
	dir := filepath.Join(t.TempDir(), "ledger")
	writer, err := ledger.OpenWriter(dir, LedgerName(StablePort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	return ledgerProxy{NewHandler(backend, testKey, append([]HandlerOption{WithLedger(writer)}, options...)...), dir, writer}
}

// call posts one message. session, when set, goes in the metadata as Claude Code sends it; headers are extra.
func (p ledgerProxy) call(t *testing.T, model, session, text string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"model": model, "messages": []conversationMessage{conversation("user", `"`+text+`"`)}}
	if session != "" {
		id, _ := json.Marshal(map[string]string{"session_id": session})
		body["metadata"] = map[string]string{"user_id": string(id)}
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(raw)))
	req.Header.Set("x-api-key", testKey)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res := httptest.NewRecorder()
	p.handler.ServeHTTP(res, req)
	return res
}

func (p ledgerProxy) lines(t *testing.T) []ledger.Line {
	t.Helper()
	lines, report, err := ledger.ReadDir(p.dir)
	if err != nil || report.Torn != 0 || report.Skipped != 0 {
		t.Fatalf("read the ledger: %v, report %+v", err, report)
	}
	return lines
}

func TestLedger_OneLinePerAnsweredRequest(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "requests.jsonl")
	requests, err := OpenRequestLog(historyPath, RequestLogLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = requests.Close() })
	p := newLedgerProxy(t, WithRequestLog(requests))

	res := p.call(t, "claude-sonnet-5-5", "0b61f1d5-full-session-id", "hello", map[string]string{"User-Agent": "claude-cli/2.1.0 (external, cli)"})
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
	lines := p.lines(t)
	if len(lines) != 1 {
		t.Fatalf("%d ledger lines, want 1", len(lines))
	}
	got := lines[0]
	want := ledger.Line{
		Schema: ledger.Schema, SemConv: ledger.SemConv, Writer: "model-proxy", Seq: 1, At: got.At, Kind: ledger.KindModelCall,
		Session: "0b61f1d5-full-session-id", Turn: "1", Call: got.Call, Harness: "claude-code", Role: RoleMain,
		RequestModel: "claude-sonnet-5-5", ResponseModel: "codex:" + defaultModel, Engine: "codex", Effort: got.Effort,
		InputTokens: 17, OutputTokens: 3, TTFBMillis: got.TTFBMillis, DurationMs: got.DurationMs, Outcome: ledger.OutcomeOK,
	}
	if got != want || !strings.HasPrefix(got.Call, "mc_") || len(got.Call) < 10 || got.TTFBMillis < 0 || got.At.IsZero() {
		t.Fatalf("line\n%+v\nwant\n%+v", got, want)
	}
	// The request history is still written, as before.
	if history := protocolLog(t, historyPath); strings.Count(history, "\n") != 1 || !strings.Contains(history, `"outcome"`) {
		t.Fatalf("request history: %q, want its one line", history)
	}
	if p.writer.Stats() != (ledger.WriterStats{Seq: 1}) {
		t.Fatalf("writer stats %+v", p.writer.Stats())
	}
}

// A second prompt of the same session is the next turn, and each model call has its own id.
func TestLedger_TurnsAndCallIDs(t *testing.T) {
	p := newLedgerProxy(t)
	p.call(t, "codex:"+defaultModel, "s1", "one", nil)
	body, _ := json.Marshal(map[string]any{
		"model":    "codex:" + defaultModel,
		"metadata": map[string]string{"user_id": `{"session_id":"s1"}`},
		"messages": []conversationMessage{conversation("user", `"one"`), conversation("assistant", `"one"`), conversation("user", `"two"`)},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	req.Header.Set("x-api-key", testKey)
	p.handler.ServeHTTP(httptest.NewRecorder(), req)

	lines := p.lines(t)
	if len(lines) != 2 || lines[0].Turn != "1" || lines[1].Turn != "2" || lines[0].Call == lines[1].Call {
		t.Fatalf("lines %+v; want turns 1 and 2 with different call ids", lines)
	}
}

// A subagent's call carries its parent's session, its own agent id, and the parent's turn.
func TestLedger_SubagentCallIsASideCallOfItsParentsTurn(t *testing.T) {
	p := newLedgerProxy(t)
	p.call(t, "codex:"+defaultModel, "s1", "parent", nil)
	p.call(t, "codex:"+defaultModel, "s1", "task for the subagent", map[string]string{"X-Claude-Code-Agent-Id": "agent-7"})

	lines := p.lines(t)
	if len(lines) != 2 {
		t.Fatalf("%d lines", len(lines))
	}
	if sub := lines[1]; !sub.Side || sub.Agent != "agent-7" || sub.Session != "s1" || sub.Turn != "1" {
		t.Fatalf("subagent line %+v; want a side call of session s1, turn 1, agent agent-7", sub)
	}
	if lines[0].Side || lines[0].Agent != "" {
		t.Fatalf("parent line %+v is marked as a side call", lines[0])
	}
}

// A background call has no session in its body. Its line is still written, with the header's session when there
// is one, and without a session otherwise.
func TestLedger_BackgroundRequestIsStillWritten(t *testing.T) {
	p := newLedgerProxy(t)
	p.call(t, "claude-haiku-5-5", "", "background", map[string]string{"X-Claude-Code-Session-Id": "from-the-header"})
	p.call(t, "claude-haiku-5-5", "", "background", nil)

	lines := p.lines(t)
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2", len(lines))
	}
	if l := lines[0]; l.Session != "from-the-header" || !l.Side || l.Turn != "" || l.RequestModel != "claude-haiku-5-5" {
		t.Fatalf("with the header: %+v", l)
	}
	if l := lines[1]; l.Session != "" || !l.Side || l.Turn != "" {
		t.Fatalf("without a session: %+v", l)
	}
}

func TestLedger_FailedTurnIsWrittenWithItsKind(t *testing.T) {
	p := newLedgerProxy(t)
	res := p.call(t, "codex:"+defaultModel, "s1", "crash", nil)
	if res.Code != http.StatusBadGateway {
		t.Fatalf("HTTP %d, want 502", res.Code)
	}
	lines := p.lines(t)
	if len(lines) != 1 || lines[0].Outcome != "app_server_exited" || lines[0].Session != "s1" {
		t.Fatalf("lines %+v; want one line with outcome app_server_exited", lines)
	}
}

// A request the proxy rejects before Codex is not a model call.
func TestLedger_RejectedRequestWritesNoLine(t *testing.T) {
	p := newLedgerProxy(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"codex:x","messages":"not a list"}`))
	req.Header.Set("x-api-key", testKey)
	res := httptest.NewRecorder()
	p.handler.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400: %s", res.Code, res.Body)
	}
	if lines := p.lines(t); len(lines) != 0 {
		t.Fatalf("a rejected request wrote %+v", lines)
	}
}

// A ledger that cannot be written changes nothing for the client, and health says a line was lost.
func TestLedger_AFailingLedgerDoesNotChangeTheAnswer(t *testing.T) {
	backend, _ := newFakeBackend(t)
	plain := NewHandler(backend, testKey)
	post := func(h http.Handler) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"codex:`+defaultModel+`","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("x-api-key", testKey)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		var message messageResponse
		_ = json.Unmarshal(res.Body.Bytes(), &message)
		text := ""
		if len(message.Content) > 0 && message.Content[0].Text != nil {
			text = *message.Content[0].Text
		}
		return res.Code, text
	}
	wantCode, wantText := post(plain)

	p := newLedgerProxy(t)
	_ = p.writer.Close() // removing an open file would not stop writes to it; a closed one fails every write
	gotCode, gotText := post(p.handler)
	if gotCode != wantCode || gotText != wantText || gotCode != http.StatusOK {
		t.Fatalf("with a failing ledger: %d %q; without: %d %q", gotCode, gotText, wantCode, wantText)
	}
	if live := readLiveness(t, p.handler); live.LedgerFailed != 1 || live.LedgerSeq != 1 || live.LedgerOff {
		t.Fatalf("liveness %+v; want one failed line", live)
	}
}

func TestLedger_TwoProxiesWriteTwoFiles(t *testing.T) {
	if LedgerName(StablePort) != "model-proxy" || LedgerName(7386) != "model-proxy-7386" {
		t.Fatalf("names %q and %q", LedgerName(StablePort), LedgerName(7386))
	}
	dir := t.TempDir()
	for _, port := range []int{StablePort, 7386} {
		backend, _ := newFakeBackend(t)
		writer, err := ledger.OpenWriter(dir, LedgerName(port))
		if err != nil {
			t.Fatal(err)
		}
		p := ledgerProxy{NewHandler(backend, testKey, WithLedger(writer)), dir, writer}
		p.call(t, "codex:"+defaultModel, "s1", "hello", nil)
		_ = writer.Close()
	}
	lines, report, err := ledger.ReadDir(dir)
	if err != nil || report.Files != 2 || len(lines) != 2 || lines[0].Seq != 1 || lines[1].Seq != 1 || lines[0].Writer == lines[1].Writer {
		t.Fatalf("lines %+v, report %+v, err %v; want one line numbered 1 in each of two files", lines, report, err)
	}
}

func TestLiveness_ReportsTheLedger(t *testing.T) {
	p := newLedgerProxy(t)
	p.call(t, "codex:"+defaultModel, "s1", "one", nil)
	p.call(t, "codex:"+defaultModel, "s2", "two", nil)
	res := httptest.NewRecorder()
	p.handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	raw, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(raw), `"ledger_seq":2`) || strings.Contains(string(raw), "ledger_failed") || strings.Contains(string(raw), "ledger_off") {
		t.Fatalf("healthz %s; want ledger_seq 2 and nothing else about the ledger", raw)
	}

	backend, _ := newFakeBackend(t)
	if live := readLiveness(t, NewHandler(backend, testKey, WithLedgerOff())); !live.LedgerOff {
		t.Fatalf("liveness %+v; want ledger_off", live)
	}
	if live := readLiveness(t, NewHandler(backend, testKey)); live.LedgerOff || live.LedgerSeq != 0 {
		t.Fatalf("a proxy that was given no ledger: %+v", live)
	}
}

// The turn table goes on from lines already in the ledger.
func TestLedger_SeededFromEarlierLines(t *testing.T) {
	backend, _ := newFakeBackend(t)
	dir := t.TempDir()
	writer, err := ledger.OpenWriter(dir, "model-proxy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	earlier := []ledger.Line{{Kind: ledger.KindModelCall, Session: "s1", Turn: "6"}}
	p := ledgerProxy{NewHandler(backend, testKey, WithLedger(writer, earlier...)), dir, writer}
	p.call(t, "codex:"+defaultModel, "s1", "after a restart", nil)
	if lines := p.lines(t); len(lines) != 1 || lines[0].Turn != "7" {
		t.Fatalf("lines %+v; want turn 7", lines)
	}
}

// Only something that looks like an id reaches the ledger from a request's headers or metadata.
func TestLedger_IdsThatAreNotIdsAreLeftOut(t *testing.T) {
	p := newLedgerProxy(t)
	p.call(t, "codex:"+defaultModel, "", "hello", map[string]string{
		"X-Claude-Code-Session-Id": "a sentence, not an id",
		"X-Claude-Code-Agent-Id":   strings.Repeat("a", 200),
	})
	lines := p.lines(t)
	if len(lines) != 1 || lines[0].Session != "" || lines[0].Agent != "" {
		t.Fatalf("lines %+v; want no session and no agent", lines)
	}
	for id, want := range map[string]string{"0b61f1d5-f79b-4c16-8a6d-3acd4c843b5f": "0b61f1d5-f79b-4c16-8a6d-3acd4c843b5f", "a77fab1e5a": "a77fab1e5a", "": "", "has space": "", "semi;colon": ""} {
		if got := ledgerID(id); got != want {
			t.Errorf("ledgerID(%q) = %q, want %q", id, got, want)
		}
	}
}

// A model name that is not a name is left out; one with a provider prefix is kept.
func TestLedgerModel(t *testing.T) {
	for name, want := range map[string]string{"claude-sonnet-5-5": "claude-sonnet-5-5", "codex:gpt-6.2-sol": "codex:gpt-6.2-sol",
		"anthropic/claude-opus-5-5": "anthropic/claude-opus-5-5", "ignore previous instructions": "", "": ""} {
		if got := ledgerModel(name); got != want {
			t.Errorf("ledgerModel(%q) = %q, want %q", name, got, want)
		}
	}
}
