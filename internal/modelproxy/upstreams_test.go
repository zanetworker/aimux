package modelproxy

import (
	"bytes"
	"context"
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

const upstreamKey = "the-gateways-own-key"

func TestNamedModel(t *testing.T) {
	names := []string{"enmaas", "lab"}
	for _, tc := range []struct {
		model, name, rest string
		err               bool
	}{
		{"enmaas:claude-opus-5-5", "enmaas", "claude-opus-5-5", false},
		{"lab:rits/deepseek", "lab", "rits/deepseek", false},
		{"codex:gpt-6.2-sol", "", "", false},
		{"claude-sonnet-5-5", "", "", false},
		{"", "", "", false},
		{"other:x", "", "", true},
		{"enmaas:", "", "", true},
	} {
		name, rest, err := namedModel(tc.model, names)
		if name != tc.name || rest != tc.rest || (err != nil) != tc.err {
			t.Errorf("namedModel(%q) = %q, %q, %v; want %q, %q, error %v", tc.model, name, rest, err, tc.name, tc.rest, tc.err)
		}
		if tc.model == "other:x" && (err == nil || !strings.Contains(err.Error(), "enmaas") || !strings.Contains(err.Error(), "lab")) {
			t.Errorf("the error for an unknown prefix should name the ones there are: %v", err)
		}
	}
	// With no named upstream at all, a prefix nobody knows is still refused: it is never another engine's to answer.
	if name, _, err := namedModel("other:x", nil); name != "" || err == nil || !strings.Contains(err.Error(), "codex:") {
		t.Errorf("no upstreams: %q, %v", name, err)
	}
	for _, model := range []string{"codex:gpt-6.2-sol", "claude-sonnet-5-5", ""} {
		if name, _, err := namedModel(model, nil); name != "" || err != nil {
			t.Errorf("no upstreams, %q: %q, %v", model, name, err)
		}
	}
}

func TestRenameModel(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"first field", `{"model":"enmaas:m1","stream":true}`, `{"model":"m1","stream":true}`},
		{"spaced, last field", "{ \"stream\" : true ,\n  \"model\" :  \"enmaas:m1\" }", "{ \"stream\" : true ,\n  \"model\" :  \"m1\" }"},
		{"a nested model is left alone", `{"metadata":{"model":"enmaas:m1"},"model":"enmaas:m1"}`, `{"metadata":{"model":"enmaas:m1"},"model":"m1"}`},
		{"an escaped name", `{"model":"enmaas:m1"}`, `{"model":"m1"}`},
		{"text that looks like the field", `{"messages":[{"role":"user","content":"\"model\":\"enmaas:m1\""}],"model":"enmaas:m1"}`, `{"messages":[{"role":"user","content":"\"model\":\"enmaas:m1\""}],"model":"m1"}`},
	} {
		got, ok := renameModel([]byte(tc.body), "m1")
		if !ok || string(got) != tc.want {
			t.Errorf("%s: renameModel = %s, %v; want %s", tc.name, got, ok, tc.want)
		}
	}
	for _, body := range []string{``, `[]`, `{"stream":true}`, `{"model":5}`, `not json`} {
		if _, ok := renameModel([]byte(body), "m1"); ok {
			t.Errorf("renameModel(%q) reported a rename", body)
		}
	}
}

// gatewayProxy is a proxy with a named upstream "enmaas", sent to a stand-in, and with the Anthropic engine on.
type gatewayProxy struct {
	twoEngines
	gateway func() []seenRequest
	history string
}

func newGatewayProxy(t *testing.T, key func(context.Context) (string, error)) gatewayProxy {
	t.Helper()
	answer := anthropicFixture(t, "stream.sse")
	gateway, seenGateway := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sseType)
		_, _ = w.Write(answer)
	})
	anthropic, seenAnthropic := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
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
	history := filepath.Join(t.TempDir(), "requests.jsonl")
	requests, err := OpenRequestLog(history, RequestLogLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = requests.Close() })
	second, err := WithAnthropic(anthropic.URL)
	if err != nil {
		t.Fatal(err)
	}
	named, err := WithMessagesUpstream("enmaas", gateway.URL, key, "")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(backend, testKey, WithLedger(writer), WithRequestLog(requests), second, named)
	return gatewayProxy{twoEngines{ledgerProxy{handler, dir, writer}, seenAnthropic, codexLog}, seenGateway, history}
}

func gatewayKey(context.Context) (string, error) { return upstreamKey, nil }

func TestWithMessagesUpstream_RefusesBadNamesAndAddresses(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"codex", "https://gw.example"}, {"anthropic", "https://gw.example"}, {"", "https://gw.example"},
		{"En Maas", "https://gw.example"}, {"a:b", "https://gw.example"}, {"enmaas", "gw.example"}, {"enmaas", "https://gw.example/v1"},
		{"enmaas", "http://gw.example"}, {"enmaas", "http://10.0.0.5:8080"},
	} {
		if _, err := WithMessagesUpstream(tc.name, tc.url, gatewayKey, ""); err == nil {
			t.Errorf("WithMessagesUpstream(%q, %q) = nil error", tc.name, tc.url)
		}
	}
}

func TestHandler_NamedUpstreamGetsItsOwnKeyAndNotTheCallers(t *testing.T) {
	p := newGatewayProxy(t, gatewayKey)
	const body = `{"stream":true, "model" : "enmaas:claude-opus-5-5","metadata":{"user_id":"{\"session_id\":\"0b61f1d5-gateway\"}"},"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/k/"+testKey+"/v1/messages?beta=true", strings.NewReader(body))
	req.Header.Set("Authorization", callersLogin)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Cookie", "c=1")
	res := httptest.NewRecorder()
	p.handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Body.String() != string(anthropicFixture(t, "stream.sse")) {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
	seen := p.gateway()
	if len(seen) != 1 || len(p.anthropic()) != 0 || p.codexStarted() {
		t.Fatalf("gateway got %d, Anthropic %d, Codex started %v", len(seen), len(p.anthropic()), p.codexStarted())
	}
	sent := seen[0]
	want := strings.Replace(body, "enmaas:claude-opus-5-5", "claude-opus-5-5", 1)
	if string(sent.Body) != want {
		t.Errorf("the gateway's body =\n%s\nwant every byte the same but the model's prefix:\n%s", sent.Body, want)
	}
	if sent.Path != "/v1/messages" || sent.Query != "beta=true" || sent.Header.Get("X-Api-Key") != upstreamKey || sent.Header.Get("Anthropic-Version") != "2023-06-01" {
		t.Errorf("the gateway got %s?%s, key set %v, version %q", sent.Path, sent.Query, sent.Header.Get("X-Api-Key") == upstreamKey, sent.Header.Get("Anthropic-Version"))
	}
	for _, mustNot := range []string{"Authorization", "Cookie"} {
		if sent.Header.Get(mustNot) != "" {
			t.Errorf("the gateway was sent the caller's %s", mustNot)
		}
	}
	for name, values := range sent.Header {
		if joined := strings.Join(values, " "); strings.Contains(joined, testKey) || strings.Contains(joined, "the-callers-login") {
			t.Errorf("the gateway was sent the proxy key or the caller's login in %s", name)
		}
	}
	lines := p.lines(t)
	if len(lines) != 1 || lines[0].Engine != "enmaas" || lines[0].RequestModel != "enmaas:claude-opus-5-5" || lines[0].ResponseModel != "claude-sonnet-5-5" ||
		lines[0].Session != "0b61f1d5-gateway" || lines[0].Turn != "1" || lines[0].OutputTokens != 40 || lines[0].Outcome != ledger.OutcomeOK {
		t.Errorf("ledger = %+v", lines)
	}
}

func TestHandler_NamedUpstreamNeedsTheProxyKey(t *testing.T) {
	p := newGatewayProxy(t, gatewayKey)
	res := p.send(t, "/v1/messages", "enmaas:claude-opus-5-5", "", "hello", map[string]string{"Authorization": callersLogin})
	if res.Code != http.StatusUnauthorized || len(p.gateway()) != 0 || len(p.anthropic()) != 0 {
		t.Fatalf("a login alone: HTTP %d, gateway %d, Anthropic %d; the gateway is paid for with the owner's key", res.Code, len(p.gateway()), len(p.anthropic()))
	}
	res = p.send(t, "/v1/messages", "other:model", "", "hello", map[string]string{"x-api-key": testKey})
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "enmaas") {
		t.Errorf("an unknown prefix: HTTP %d: %s", res.Code, res.Body)
	}
	// With the key in a header and no login, as a client set up for the gateway alone would send it.
	res = p.send(t, "/v1/messages", "enmaas:claude-opus-5-5", "", "hello", map[string]string{"x-api-key": testKey})
	if seen := p.gateway(); res.Code != http.StatusOK || len(seen) != 1 || seen[0].Header.Get("X-Api-Key") != upstreamKey {
		t.Errorf("the proxy key in a header: HTTP %d, gateway got %d", res.Code, len(seen))
	}
}

func TestHandler_NamedUpstreamKeyIsNeverLogged(t *testing.T) {
	p := newGatewayProxy(t, gatewayKey)
	res := p.send(t, "/v1/messages", "enmaas:claude-opus-5-5", "0b61f1d5-quiet", "hello", map[string]string{"x-api-key": testKey})
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP %d", res.Code)
	}
	_ = p.writer.Close()
	ledgerFile, _ := os.ReadFile(filepath.Join(p.dir, LedgerName(StablePort)+".jsonl")) // #nosec G304 -- the test's own folder
	history, _ := os.ReadFile(p.history)                                                // #nosec G304 -- the test's own file
	if len(ledgerFile) == 0 || len(history) == 0 {
		t.Fatalf("ledger %d bytes, history %d bytes; the test proves nothing without both", len(ledgerFile), len(history))
	}
	for name, text := range map[string][]byte{"the response": res.Body.Bytes(), "the ledger": ledgerFile, "the request history": history} {
		if bytes.Contains(text, []byte(upstreamKey)) {
			t.Errorf("%s holds the gateway's key", name)
		}
	}
	if !bytes.Contains(history, []byte(`"enmaas"`)) {
		t.Errorf("the request history does not name the upstream: %s", history)
	}
}

func TestHandler_MissingUpstreamKeyIsAClearError(t *testing.T) {
	p := newGatewayProxy(t, func(context.Context) (string, error) {
		return "", errors.New("secret store: item not found in /Users/someone/Library")
	})
	res := p.send(t, "/v1/messages", "enmaas:claude-opus-5-5", "0b61f1d5-nokey", "hello", map[string]string{"x-api-key": testKey})
	if res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "enmaas-key") || strings.Contains(res.Body.String(), "/Users/") {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
	if len(p.gateway()) != 0 || len(p.lines(t)) != 0 {
		t.Errorf("gateway got %d requests, ledger has %d lines", len(p.gateway()), len(p.lines(t)))
	}
}

func TestHandler_NamedUpstreamWithoutTheAnthropicEngine(t *testing.T) {
	gateway, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", jsonType)
		_, _ = w.Write(anthropicFixture(t, "message.json"))
	})
	named, err := WithMessagesUpstream("enmaas", gateway.URL, gatewayKey, "")
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := newFakeBackend(t)
	handler := NewHandler(backend, testKey, named)
	post := func(model string, headers map[string]string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"model": model, "messages": []conversationMessage{conversation("user", `"hello"`)}})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw))
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if res := post("enmaas:claude-opus-5-5", map[string]string{"x-api-key": testKey}); res.Code != http.StatusOK || len(seen()) != 1 {
		t.Fatalf("HTTP %d, gateway got %d", res.Code, len(seen()))
	}
	// Everything else is what it was: Codex answers the rest, and a login alone is refused.
	if res := post("claude-sonnet-5-5", map[string]string{"x-api-key": testKey}); res.Code != http.StatusOK || len(seen()) != 1 {
		t.Errorf("a Claude name with the key: HTTP %d, gateway got %d", res.Code, len(seen()))
	}
	if res := post("enmaas:claude-opus-5-5", map[string]string{"Authorization": callersLogin}); res.Code != http.StatusUnauthorized || len(seen()) != 1 {
		t.Errorf("a login alone: HTTP %d, gateway got %d", res.Code, len(seen()))
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var live Liveness
	if err := json.Unmarshal(res.Body.Bytes(), &live); err != nil || strings.Join(live.Engines, ",") != "codex,enmaas" {
		t.Errorf("healthz engines = %v (%v)", live.Engines, err)
	}
}

func TestWithMessagesUpstream_PlainHTTPOnlyOnThisMachine(t *testing.T) {
	for _, address := range []string{"https://gw.example", "http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080"} {
		if _, err := WithMessagesUpstream("enmaas", address, gatewayKey, ""); err != nil {
			t.Errorf("WithMessagesUpstream(%q) = %v", address, err)
		}
	}
}

// A gateway that refuses the key may repeat it in its answer. The caller is told what to check, not what it said.
func TestHandler_NamedUpstreamRefusalIsNotPassedOn(t *testing.T) {
	asked := 0
	gateway, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Header().Set("Content-Type", jsonType)
		w.Header().Set("X-Echo", r.Header.Get("X-Api-Key"))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key ` + r.Header.Get("X-Api-Key") + `"}`)) //nolint:gosec // G705: a stand-in that repeats the key on purpose
	})
	reads := 0
	named, err := WithMessagesUpstream("enmaas", gateway.URL, func(context.Context) (string, error) { reads++; return upstreamKey, nil }, "")
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := newFakeBackend(t)
	handler := NewHandler(backend, testKey, named)
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"enmaas:m1","messages":[]}`))
		req.Header.Set("x-api-key", testKey)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	res := post()
	if res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "enmaas-key") {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), upstreamKey) || strings.Contains(strings.Join(res.Header().Values("X-Echo"), ""), upstreamKey) {
		t.Errorf("the gateway's key came back to the caller: %s %v", res.Body, res.Header())
	}
	// The key is read again for the next request: it may have been rotated.
	post()
	if reads != 2 || asked != 2 {
		t.Errorf("key read %d times, gateway asked %d times; want 2 and 2", reads, asked)
	}
}

func TestHandler_NamedUpstreamAloneRefusesALoginBeforeReadingIt(t *testing.T) {
	gateway, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {})
	named, err := WithMessagesUpstream("enmaas", gateway.URL, gatewayKey, "")
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := newFakeBackend(t)
	body := &countingReader{Reader: strings.NewReader(`{"model":"enmaas:m1","messages":[]}`)}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("Authorization", callersLogin)
	res := httptest.NewRecorder()
	NewHandler(backend, testKey, named).ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized || body.read != 0 || len(seen()) != 0 {
		t.Fatalf("HTTP %d, %d body bytes read, gateway got %d", res.Code, body.read, len(seen()))
	}
}

type countingReader struct {
	io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.read += n
	return n, err
}

// Whatever a gateway answers, the owner's key does not come back to the caller, and the caller is not sent
// somewhere else.
func TestHandler_NamedUpstreamAnswersThatAreNotPassedOn(t *testing.T) {
	for name, tc := range map[string]struct {
		answer   http.HandlerFunc
		want     int
		wantBody string
	}{
		"an error that repeats the key": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", jsonType)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"malformed credential `+r.Header.Get("X-Api-Key")+`"}`) //nolint:gosec // G705: a stand-in that repeats the key on purpose
		}, http.StatusBadGateway, "could not be passed on"},
		"a header that repeats the key": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Debug-Key", r.Header.Get("X-Api-Key"))
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow down"}`)
		}, http.StatusTooManyRequests, "slow down"},
		"a redirect": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.invalid/v1/messages", http.StatusTemporaryRedirect)
		}, http.StatusBadGateway, "redirect"},
		"an ordinary error is passed on": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", jsonType)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"max_tokens is required"}`)
		}, http.StatusBadRequest, "max_tokens is required"},
	} {
		t.Run(name, func(t *testing.T) {
			gateway, _ := anthropicStandIn(t, tc.answer)
			named, err := WithMessagesUpstream("enmaas", gateway.URL, gatewayKey, "")
			if err != nil {
				t.Fatal(err)
			}
			backend, _ := newFakeBackend(t)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"enmaas:m1","messages":[]}`))
			req.Header.Set("x-api-key", testKey)
			res := httptest.NewRecorder()
			NewHandler(backend, testKey, named).ServeHTTP(res, req)
			if res.Code != tc.want || !strings.Contains(res.Body.String(), tc.wantBody) {
				t.Errorf("HTTP %d: %s; want %d holding %q", res.Code, res.Body, tc.want, tc.wantBody)
			}
			if res.Header().Get("Location") != "" {
				t.Errorf("a Location came back: %q", res.Header().Get("Location"))
			}
			dump := res.Body.String()
			for header, values := range res.Header() {
				dump += header + strings.Join(values, " ")
			}
			if strings.Contains(dump, upstreamKey) {
				t.Errorf("the gateway's key came back to the caller")
			}
		})
	}
}

// A name that is not an engine here, because it was never added or because it is switched off, is refused. It
// is not answered by Codex or passed to Anthropic as if it were a model of theirs.
func TestHandler_AnEngineNameNobodyKnowsIsRefused(t *testing.T) {
	both := map[string]string{"x-api-key": testKey, "Authorization": callersLogin}
	for name, tc := range map[string]struct {
		headers map[string]string
		model   string
		want    int
	}{
		"the key only":           {map[string]string{"x-api-key": testKey}, "lab:some-model", http.StatusBadRequest},
		"a login only":           {map[string]string{"Authorization": callersLogin}, "lab:some-model", http.StatusBadRequest},
		"both":                   {both, "lab:some-model", http.StatusBadRequest},
		"nothing at all":         {nil, "lab:some-model", http.StatusUnauthorized},
		"codex is still codex":   {both, "codex:gpt-6.2-sol", http.StatusOK},
		"a plain model is plain": {both, "claude-sonnet-5-5", http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			p := newTwoEngines(t) // no named upstream is configured
			res := p.send(t, "/v1/messages", tc.model, "0b61f1d5-unknown", "hello", tc.headers)
			if res.Code != tc.want {
				t.Fatalf("HTTP %d: %s", res.Code, res.Body)
			}
			if tc.want == http.StatusBadRequest {
				if len(p.anthropic()) != 0 || p.codexStarted() || len(p.lines(t)) != 0 {
					t.Errorf("a refused name reached an engine or the ledger: Anthropic %d, Codex started %v, lines %d", len(p.anthropic()), p.codexStarted(), len(p.lines(t)))
				}
				if !strings.Contains(res.Body.String(), "lab") || !strings.Contains(res.Body.String(), "codex:") {
					t.Errorf("the refusal should name what was asked and what there is: %s", res.Body)
				}
			}
		})
	}
	// Without the Anthropic engine too: the proxy as it has always run.
	plain := newLedgerProxy(t)
	if res := plain.call(t, "lab:some-model", "", "hello", nil); res.Code != http.StatusBadRequest {
		t.Errorf("the proxy with Codex alone: HTTP %d: %s", res.Code, res.Body)
	}
	if res := plain.call(t, "claude-sonnet-5-5", "", "hello", nil); res.Code != http.StatusOK {
		t.Errorf("a plain model on the proxy with Codex alone: HTTP %d", res.Code)
	}
}

func TestHandler_MissingUpstreamKeySaysWhereItIsKept(t *testing.T) {
	gateway, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {})
	named, err := WithMessagesUpstream("enmaas", gateway.URL, func(context.Context) (string, error) { return "", errors.New("not there") }, "the environment variable ENMAAS_KEY")
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := newFakeBackend(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"enmaas:m1","messages":[]}`))
	req.Header.Set("x-api-key", testKey)
	res := httptest.NewRecorder()
	NewHandler(backend, testKey, named).ServeHTTP(res, req)
	if res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "the environment variable ENMAAS_KEY") || len(seen()) != 0 {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
}
