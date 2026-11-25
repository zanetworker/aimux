package modelproxy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/ledger"
)

const callersLogin = "Bearer the-callers-login"

func TestChooseEngine(t *testing.T) {
	key, own, both := credentials{proxyKey: true}, credentials{own: true}, credentials{proxyKey: true, own: true}
	for _, tc := range []struct {
		name   string
		model  string
		c      credentials
		engine string
		err    error
	}{
		{"a codex model with the key", "codex:gpt-6.2-sol", key, engineCodex, nil},
		{"a codex model with both", "codex:gpt-6.2-sol", both, engineCodex, nil},
		{"a codex model with the login only", "codex:gpt-6.2-sol", own, "", errNeedsProxyKey},
		{"another model with the login", "claude-sonnet-5-5", own, engineAnthropic, nil},
		{"another model with both", "claude-sonnet-5-5", both, engineAnthropic, nil},
		{"another model with the key only, as before", "claude-sonnet-5-5", key, engineCodex, nil},
		{"no model with the key", "", key, engineCodex, nil},
		{"no model with the login", "", own, engineAnthropic, nil},
		{"nothing usable", "claude-sonnet-5-5", credentials{}, "", errNoCredential},
		{"a codex model and nothing", "codex:gpt-6.2-sol", credentials{}, "", errNoCredential},
	} {
		engine, err := chooseEngine(tc.model, tc.c)
		if engine != tc.engine || !errors.Is(err, tc.err) {
			t.Errorf("%s: chooseEngine = %q, %v; want %q, %v", tc.name, engine, err, tc.engine, tc.err)
		}
	}
}

func TestReadCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		header map[string]string
		want   credentials
		path   string
	}{
		{"the key as an API key", "/v1/messages", map[string]string{"X-Api-Key": testKey}, credentials{proxyKey: true}, "/v1/messages"},
		{"the key as a bearer token", "/v1/messages", map[string]string{"Authorization": "Bearer " + testKey}, credentials{proxyKey: true}, "/v1/messages"},
		{"the key in the path", "/k/" + testKey + "/v1/messages?beta=true", nil, credentials{proxyKey: true, viaPath: true}, "/v1/messages"},
		{"the key in the path, nothing after it", "/k/" + testKey, nil, credentials{proxyKey: true, viaPath: true}, "/"},
		{"the key in the path and a login", "/k/" + testKey + "/api/hello", map[string]string{"Authorization": callersLogin}, credentials{proxyKey: true, viaPath: true, own: true}, "/api/hello"},
		{"an escaped path keeps its escapes", "/k/" + testKey + "/v1/files/a%2Fb", nil, credentials{proxyKey: true, viaPath: true}, "/v1/files/a%2Fb"},
		{"a wrong key in the path", "/k/not-the-key/v1/messages", map[string]string{"Authorization": callersLogin}, credentials{own: true, badPath: true}, "/k/not-the-key/v1/messages"},
		{"an empty key in the path", "/k//v1/messages", nil, credentials{badPath: true}, "/k//v1/messages"},
		{"a login only", "/v1/messages", map[string]string{"Authorization": callersLogin}, credentials{own: true}, "/v1/messages"},
		{"an API key that is not the proxy's is not a login", "/v1/messages", map[string]string{"X-Api-Key": "someone-elses"}, credentials{}, "/v1/messages"},
		{"an empty bearer token", "/v1/messages", map[string]string{"Authorization": "Bearer "}, credentials{}, "/v1/messages"},
		{"nothing", "/v1/messages", nil, credentials{}, "/v1/messages"},
		{"a path that only starts like the prefix", "/keys", nil, credentials{}, "/keys"},
		{"the prefix with an encoded letter", "/%6B/" + testKey + "/v1/messages", map[string]string{"Authorization": callersLogin}, credentials{own: true, badPath: true}, "/%6B/" + testKey + "/v1/messages"},
		{"the prefix with an encoded slash", "/k%2F" + testKey + "/v1/messages", nil, credentials{badPath: true}, "/k%2F" + testKey + "/v1/messages"},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.target, nil)
		for name, value := range tc.header {
			req.Header.Set(name, value)
		}
		got, path := readCredentials(req, testKey)
		if got != tc.want || path != tc.path {
			t.Errorf("%s: readCredentials = %+v, %q; want %+v, %q", tc.name, got, path, tc.want, tc.path)
		}
	}
	if got, _ := readCredentials(httptest.NewRequest(http.MethodGet, "/k//v1/models", nil), ""); got.proxyKey {
		t.Error("an empty proxy key must never match")
	}
}

// twoEngines is a proxy over the fake Codex app-server with the Anthropic engine on, sent to a stand-in.
type twoEngines struct {
	ledgerProxy
	anthropic func() []seenRequest
	codexLog  string
}

func newTwoEngines(t *testing.T) twoEngines {
	t.Helper()
	answer := anthropicFixture(t, "stream.sse")
	upstream, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.Header().Set("Content-Type", jsonType)
			_, _ = io.WriteString(w, `{"from":"anthropic"}`)
			return
		}
		w.Header().Set("Content-Type", sseType)
		_, _ = w.Write(answer)
	})
	backend, codexLog := newFakeBackend(t)
	dir := filepath.Join(t.TempDir(), "ledger")
	writer, err := ledger.OpenWriter(dir, LedgerName(StablePort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	engine, err := WithAnthropic(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(backend, testKey, WithLedger(writer), engine)
	return twoEngines{ledgerProxy{handler, dir, writer}, seen, codexLog}
}

func (p twoEngines) codexStarted() bool {
	info, err := os.Stat(p.codexLog)
	return err == nil && info.Size() > 0
}

// send posts one message to target with the given headers, as session when it is set.
func (p twoEngines) send(t *testing.T, target, model, session, text string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"model": model, "messages": []conversationMessage{conversation("user", `"`+text+`"`)}}
	if session != "" {
		id, _ := json.Marshal(map[string]string{"session_id": session})
		body["metadata"] = map[string]string{"user_id": string(id)}
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(string(raw)))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res := httptest.NewRecorder()
	p.handler.ServeHTTP(res, req)
	return res
}

func TestHandler_ProxyKeyOnlyBehavesAsBefore(t *testing.T) {
	p := newTwoEngines(t)
	res := p.send(t, "/v1/messages", "claude-sonnet-5-5", "0b61f1d5-key-only", "hello", map[string]string{"x-api-key": testKey})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"type":"message"`) {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
	if got := p.anthropic(); len(got) != 0 {
		t.Fatalf("Anthropic received %d requests for a request that carried only the proxy key", len(got))
	}
	lines := p.lines(t)
	if len(lines) != 1 || lines[0].Engine != engineCodex {
		t.Fatalf("ledger = %+v", lines)
	}
	// A path nothing serves is still not found, and still not forwarded.
	req := httptest.NewRequest(http.MethodHead, "/api/hello", nil)
	req.Header.Set("x-api-key", testKey)
	res = httptest.NewRecorder()
	p.handler.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound || len(p.anthropic()) != 0 {
		t.Errorf("HEAD /api/hello with the key only: HTTP %d, Anthropic got %d", res.Code, len(p.anthropic()))
	}
}

func TestHandler_OwnLoginGoesToAnthropic(t *testing.T) {
	p := newTwoEngines(t)
	res := p.send(t, "/v1/messages?beta=true", "claude-sonnet-5-5", "0b61f1d5-own-login", "hello",
		map[string]string{"Authorization": callersLogin, "User-Agent": "claude-cli/2.1.332 (external, cli)"})
	if res.Code != http.StatusOK || res.Body.String() != string(anthropicFixture(t, "stream.sse")) {
		t.Fatalf("HTTP %d; the client should get Anthropic's answer unchanged, got %q", res.Code, res.Body)
	}
	if p.codexStarted() {
		t.Error("Codex was started for a request Anthropic answered")
	}
	seen := p.anthropic()
	if len(seen) != 1 || seen[0].Path != "/v1/messages" || seen[0].Query != "beta=true" || seen[0].Header.Get("Authorization") != callersLogin {
		t.Fatalf("Anthropic got %+v", seen)
	}
	lines := p.lines(t)
	if len(lines) != 1 {
		t.Fatalf("ledger has %d lines, want 1", len(lines))
	}
	line := lines[0]
	want := ledger.Line{
		Kind: ledger.KindModelCall, Session: "0b61f1d5-own-login", Turn: "1", Harness: "claude-code",
		RequestModel: "claude-sonnet-5-5", ResponseModel: "claude-sonnet-5-5", Engine: engineAnthropic,
		InputTokens: 1200 + 90000 + 300, CacheRead: 90000, CacheWrite: 300, OutputTokens: 40, Outcome: ledger.OutcomeOK,
	}
	got := line
	got.Schema, got.SemConv, got.At, got.Writer, got.Seq, got.Call, got.TTFBMillis, got.DurationMs = "", "", want.At, "", 0, "", 0, 0
	if got != want {
		t.Errorf("ledger line =\n%+v\nwant\n%+v", got, want)
	}
	if !strings.HasPrefix(line.Call, "mc_") || line.Estimated || line.UsageUnknown {
		t.Errorf("call %q, estimated %v, usage unknown %v", line.Call, line.Estimated, line.UsageUnknown)
	}
	raw, _ := os.ReadFile(filepath.Join(p.dir, LedgerName(StablePort)+".jsonl")) // #nosec G304 -- the test's own folder
	for _, leak := range []string{"the-callers-login", "hello", "SECRET-ANSWER"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the ledger holds %q", leak)
		}
	}
}

func TestHandler_AnthropicAnswerWithoutUsage(t *testing.T) {
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", jsonType)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(anthropicFixture(t, "error.json"))
	})
	backend, _ := newFakeBackend(t)
	dir := filepath.Join(t.TempDir(), "ledger")
	writer, err := ledger.OpenWriter(dir, LedgerName(StablePort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	engine, err := WithAnthropic(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := twoEngines{ledgerProxy: ledgerProxy{NewHandler(backend, testKey, WithLedger(writer), engine), dir, writer}}
	res := p.send(t, "/v1/messages", "claude-sonnet-5-5", "0b61f1d5-limited", "hello", map[string]string{"Authorization": callersLogin})
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("HTTP %d", res.Code)
	}
	lines := p.lines(t)
	if len(lines) != 1 || !lines[0].UsageUnknown || lines[0].InputTokens != 0 || lines[0].Outcome != "upstream_error" || lines[0].ResponseModel != "" {
		t.Fatalf("ledger = %+v", lines)
	}
}

func TestHandler_CodexModelWithoutTheKeyIsRefused(t *testing.T) {
	p := newTwoEngines(t)
	res := p.send(t, "/v1/messages", "codex:gpt-6.2-sol", "0b61f1d5-no-key", "hello", map[string]string{"Authorization": callersLogin})
	if res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "followed by /k/ and the proxy key") {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "the-callers-login") {
		t.Error("the refusal echoes the login")
	}
	if len(p.anthropic()) != 0 || p.codexStarted() || len(p.lines(t)) != 0 {
		t.Errorf("a refused request reached an engine or the ledger: Anthropic %d, Codex started %v, lines %d", len(p.anthropic()), p.codexStarted(), len(p.lines(t)))
	}
}

func TestHandler_BothCredentialsCanUseBothEngines(t *testing.T) {
	p := newTwoEngines(t)
	base := "/k/" + testKey
	res := p.send(t, base+"/v1/messages?beta=true", "claude-sonnet-5-5", "0b61f1d5-both", "one", map[string]string{"Authorization": callersLogin})
	if res.Code != http.StatusOK {
		t.Fatalf("Anthropic turn: HTTP %d: %s", res.Code, res.Body)
	}
	seen := p.anthropic()
	if len(seen) != 1 || seen[0].Path != "/v1/messages" || seen[0].Query != "beta=true" || seen[0].Header.Get("Authorization") != callersLogin {
		t.Fatalf("Anthropic got %+v", seen)
	}
	for name, values := range seen[0].Header {
		if strings.Contains(strings.Join(values, " "), testKey) {
			t.Errorf("Anthropic was sent the proxy key in %s", name)
		}
	}
	res = p.send(t, base+"/v1/messages?beta=true", "codex:gpt-6.2-sol", "0b61f1d5-both", "two", map[string]string{"Authorization": callersLogin})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"model":"codex:gpt-6.2-sol"`) {
		t.Fatalf("Codex turn: HTTP %d: %s", res.Code, res.Body)
	}
	if len(p.anthropic()) != 1 {
		t.Errorf("Anthropic received the Codex turn")
	}
	// Another path with the login is Anthropic's to answer; the prefix never reaches it.
	req := httptest.NewRequest(http.MethodHead, base+"/api/hello", nil)
	req.Header.Set("Authorization", callersLogin)
	res = httptest.NewRecorder()
	p.handler.ServeHTTP(res, req)
	if seen = p.anthropic(); res.Code != http.StatusOK || len(seen) != 2 || seen[1].Path != "/api/hello" {
		t.Errorf("HEAD /api/hello: HTTP %d, Anthropic got %+v", res.Code, seen)
	}
}

func TestHandler_WrongKeyInThePathIsRefused(t *testing.T) {
	p := newTwoEngines(t)
	res := p.send(t, "/k/not-the-key/v1/messages", "claude-sonnet-5-5", "", "hello", map[string]string{"Authorization": callersLogin})
	if res.Code != http.StatusUnauthorized || len(p.anthropic()) != 0 {
		t.Fatalf("HTTP %d, Anthropic got %d; a wrong key in the path is refused even with a login", res.Code, len(p.anthropic()))
	}
	if strings.Contains(res.Body.String(), "not-the-key") {
		t.Error("the refusal echoes what was sent as a key")
	}
}

func TestLedger_SwitchLineWhenASessionChangesEngine(t *testing.T) {
	p := newTwoEngines(t)
	const session = "0b61f1d5-switching"
	both := map[string]string{"Authorization": callersLogin, "x-api-key": testKey}
	subagent := map[string]string{"Authorization": callersLogin, "x-api-key": testKey, agentHeader: "agent-7"}
	for _, step := range []struct {
		model, text string
		headers     map[string]string
	}{
		{"claude-sonnet-5-5", "one", both},
		{"codex:gpt-6.2-sol", "two", both},
		{"claude-sonnet-5-5", "a subagent's task", subagent},
		{"claude-sonnet-5-5", "three", both},
	} {
		if res := p.send(t, "/v1/messages", step.model, session, step.text, step.headers); res.Code != http.StatusOK {
			t.Fatalf("%s %q: HTTP %d: %s", step.model, step.text, res.Code, res.Body)
		}
	}
	type row struct {
		kind         ledger.Kind
		turn, engine string
		from, to     string
		side         bool
	}
	var got []row
	for _, line := range p.lines(t) {
		if line.Session != session {
			t.Errorf("a line of another session: %+v", line)
		}
		got = append(got, row{line.Kind, line.Turn, line.Engine, line.FromEngine, line.ToEngine, line.Side})
	}
	want := []row{
		{ledger.KindModelCall, "1", engineAnthropic, "", "", false},
		{ledger.KindSwitch, "2", "", engineAnthropic, engineCodex, false},
		{ledger.KindModelCall, "2", engineCodex, "", "", false},
		{ledger.KindModelCall, "2", engineAnthropic, "", "", true},
		{ledger.KindSwitch, "3", "", engineCodex, engineAnthropic, false},
		{ledger.KindModelCall, "3", engineAnthropic, "", "", false},
	}
	if len(got) != len(want) {
		t.Fatalf("ledger rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestTurns_SeededEngineStillSwitches(t *testing.T) {
	table := &turns{}
	table.seed([]ledger.Line{
		{Kind: ledger.KindModelCall, Session: "s", Turn: "4", Engine: engineCodex},
		{Kind: ledger.KindModelCall, Session: "s", Turn: "4", Engine: engineAnthropic, Side: true},
	})
	if turn, from := table.nextOn("s", false, true, 5, 1, engineAnthropic); turn != "5" || from != engineCodex {
		t.Errorf("after a restart: turn %q, switched from %q; want 5, codex", turn, from)
	}
	if _, from := table.nextOn("s", false, true, 6, 2, engineAnthropic); from != "" {
		t.Errorf("the same engine again reported a switch from %q", from)
	}
	if _, from := table.nextOn("new", false, true, 1, 3, engineCodex); from != "" {
		t.Errorf("a session's first request reported a switch from %q", from)
	}
}

func TestHandler_WithoutTheOptionNothingChanges(t *testing.T) {
	p := newLedgerProxy(t)
	for _, model := range []string{"claude-sonnet-5-5", "codex:gpt-6.2-sol", ""} {
		res := p.call(t, model, "", "hello", map[string]string{"x-api-key": "", "Authorization": callersLogin})
		if res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "invalid or missing proxy key") {
			t.Errorf("model %q with a login only: HTTP %d: %s", model, res.Code, res.Body)
		}
	}
	// The path prefix is a way to present the key whether or not the Anthropic engine is on.
	raw := `{"model":"codex:gpt-6.2-sol","messages":[{"role":"user","content":"hello"}]}`
	res := httptest.NewRecorder()
	p.handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/k/"+testKey+"/v1/messages", strings.NewReader(raw)))
	if res.Code != http.StatusOK {
		t.Errorf("the key in the path: HTTP %d: %s", res.Code, res.Body)
	}
	res = httptest.NewRecorder()
	p.handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/k/wrong/v1/messages", strings.NewReader(raw)))
	if res.Code != http.StatusUnauthorized {
		t.Errorf("a wrong key in the path: HTTP %d", res.Code)
	}
}

func TestWithAnthropic_RefusesABadUpstream(t *testing.T) {
	if _, err := WithAnthropic("https://api.anthropic.com/v1"); err == nil {
		t.Error("WithAnthropic accepted an upstream with a path")
	}
}

func TestHandler_TheKeyNeverReachesAnthropicInAPath(t *testing.T) {
	p := newTwoEngines(t)
	for _, target := range []string{
		"/%6B/" + testKey + "/v1/messages",
		"/k%2F" + testKey + "/v1/messages",
		"/v1/files/" + testKey,
		"/v1/files?after=" + testKey,
	} {
		res := p.send(t, target, "claude-sonnet-5-5", "", "hello", map[string]string{"Authorization": callersLogin})
		if res.Code < 400 || strings.Contains(res.Body.String(), testKey) {
			t.Errorf("%s: HTTP %d %s; want a refusal that does not repeat the key", strings.ReplaceAll(target, testKey, "<key>"), res.Code, res.Body)
		}
	}
	if got := p.anthropic(); len(got) != 0 {
		t.Fatalf("Anthropic received %d requests whose address held the proxy key", len(got))
	}
}

func TestHandler_AnEncodedMessagesPathIsStillRouted(t *testing.T) {
	p := newTwoEngines(t)
	res := p.send(t, "/v1/%6Dessages", "codex:gpt-6.2-sol", "0b61f1d5-encoded", "hello", map[string]string{"Authorization": callersLogin, "x-api-key": testKey})
	if res.Code != http.StatusOK || len(p.anthropic()) != 0 {
		t.Fatalf("HTTP %d, Anthropic got %d; a codex: model goes to Codex however the path is spelled", res.Code, len(p.anthropic()))
	}
	if lines := p.lines(t); len(lines) != 1 || lines[0].Engine != engineCodex {
		t.Errorf("ledger = %+v", lines)
	}
}

func TestHandler_ABodyTooLargeIsNotForwardedInPart(t *testing.T) {
	old := maxRequestBytes
	maxRequestBytes = 64
	t.Cleanup(func() { maxRequestBytes = old })
	p := newTwoEngines(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(strings.Repeat("x", 200)))
	req.Header.Set("Authorization", callersLogin)
	res := httptest.NewRecorder()
	p.handler.ServeHTTP(res, req)
	if res.Code != http.StatusRequestEntityTooLarge || len(p.anthropic()) != 0 {
		t.Fatalf("HTTP %d, Anthropic got %d requests", res.Code, len(p.anthropic()))
	}
}

func TestHandler_EachHandlerRemovesItsOwnKey(t *testing.T) {
	upstream, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {})
	engine, err := WithAnthropic(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := newFakeBackend(t)
	first := NewHandler(backend, "the-first-key", engine)
	_ = NewHandler(backend, "the-second-key", engine)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5-5"}`))
	req.Header.Set("Authorization", callersLogin)
	req.Header.Set("x-api-key", "the-first-key")
	first.ServeHTTP(httptest.NewRecorder(), req)
	got := seen()
	if len(got) != 1 || got[0].Header.Get("X-Api-Key") != "" {
		t.Fatalf("Anthropic got %d requests, with the first handler's key: %v", len(got), len(got) == 1 && got[0].Header.Get("X-Api-Key") != "")
	}
}

func TestHandler_HealthStaysLocal(t *testing.T) {
	p := newTwoEngines(t)
	for name, tc := range map[string]struct {
		headers map[string]string
		want    int
	}{
		"the key and a login": {map[string]string{"x-api-key": testKey, "Authorization": callersLogin}, http.StatusOK},
		"a login only":        {map[string]string{"Authorization": callersLogin}, http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		for header, value := range tc.headers {
			req.Header.Set(header, value)
		}
		res := httptest.NewRecorder()
		p.handler.ServeHTTP(res, req)
		// The proxy's own health answer is 200, or 503 when it is degraded: either way it is the proxy's.
		local := res.Code == http.StatusOK || res.Code == http.StatusServiceUnavailable
		if (tc.want == http.StatusOK) != local || (tc.want != http.StatusOK && res.Code != tc.want) {
			t.Errorf("%s: HTTP %d, want %d", name, res.Code, tc.want)
		}
	}
	if got := p.anthropic(); len(got) != 0 {
		t.Errorf("Anthropic received %d health requests", len(got))
	}
}

func TestLedger_ARefusedAddressLeavesNoTrace(t *testing.T) {
	p := newTwoEngines(t)
	both := map[string]string{"Authorization": callersLogin, "x-api-key": testKey}
	const session = "0b61f1d5-refused"
	if res := p.send(t, "/v1/messages", "codex:gpt-6.2-sol", session, "one", both); res.Code != http.StatusOK {
		t.Fatalf("HTTP %d", res.Code)
	}
	if res := p.send(t, "/v1/messages?x="+testKey, "claude-sonnet-5-5", session, "two", both); res.Code != http.StatusBadRequest {
		t.Fatalf("a key in the query: HTTP %d", res.Code)
	}
	if res := p.send(t, "/v1/messages", "codex:gpt-6.2-sol", session, "three", both); res.Code != http.StatusOK {
		t.Fatalf("HTTP %d", res.Code)
	}
	lines := p.lines(t)
	if len(lines) != 2 || lines[0].Kind != ledger.KindModelCall || lines[1].Kind != ledger.KindModelCall || lines[1].Turn != "2" {
		t.Fatalf("ledger = %+v; want two Codex calls, turns 1 and 2, and no switch", lines)
	}
}
