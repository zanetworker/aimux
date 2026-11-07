package insight

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/llm"
)

// fakeAPI answers every request with reply and keeps the request body and
// headers.
func fakeAPI(t *testing.T, status int, reply string) (body *map[string]any, headers *http.Header) {
	t.Helper()
	body, headers = &map[string]any{}, &http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*headers = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	old := messagesURL
	messagesURL = srv.URL
	t.Cleanup(func() { messagesURL = old })
	return body, headers
}

// TestGenerate_RequestShape pins insight's request: haiku's id as llm
// means (llm.ResolveModel), max_tokens 2048, one user message, no system prompt.
func TestGenerate_RequestShape(t *testing.T) {
	body, headers := fakeAPI(t, 200, `{"model":"claude-haiku-5-5","content":[{"type":"text","text":"  {\"a\":\"b\"}\n"}],"usage":{"input_tokens":900,"output_tokens":40}}`)
	got, err := Generate(Config{Model: "haiku", APIKey: "test-key"}, "the prompt")
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"a":"b"}` {
		t.Errorf("text = %q", got)
	}
	b := *body
	if len(b) != 3 || b["model"] != "claude-haiku-5-5" || b["max_tokens"].(float64) != 2048 {
		t.Errorf("request = %v", b)
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "the prompt" || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("messages = %v", msgs)
	}
	if headers.Get("x-api-key") != "test-key" || headers.Get("anthropic-version") != "2023-06-01" {
		t.Error("auth headers missing")
	}
}

func TestGenerate_ModelNames(t *testing.T) {
	for in, want := range map[string]string{
		"sonnet":          "claude-sonnet-5-5",
		"opus":            "claude-opus-5-5",
		"claude-opus-5-5": "claude-opus-5-5",
		"gpt-9":           "claude-haiku-5-5", // unknown names fall back to haiku, as everywhere
	} {
		body, _ := fakeAPI(t, 200, `{"content":[{"type":"text","text":"ok"}]}`)
		if _, err := Generate(Config{Model: in, APIKey: "k"}, "p"); err != nil {
			t.Fatal(err)
		}
		if (*body)["model"] != want {
			t.Errorf("%s: model = %v, want %s", in, (*body)["model"], want)
		}
	}
}

func TestGenerate_Errors(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := Generate(Config{}, "p"); err == nil || err.Error() != "no API key configured" {
		t.Errorf("no key: err = %v", err)
	}
	fakeAPI(t, 200, `{"content":[]}`)
	if _, err := Generate(Config{APIKey: "k"}, "p"); err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Errorf("empty: err = %v", err)
	}
	fakeAPI(t, 529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	if _, err := Generate(Config{APIKey: "k"}, "p"); err == nil || !strings.Contains(err.Error(), "529") {
		t.Errorf("529: err = %v", err)
	}
}

// TestGenerate_RecordsUsageUnderInsight: each successful call writes one
// usage line labelled "insight" with the model insight sent.
func TestGenerate_RecordsUsageUnderInsight(t *testing.T) {
	fakeAPI(t, 200, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":900,"output_tokens":40}}`)
	var got []cost.Usage
	rec := func(u cost.Usage) error { got = append(got, u); return nil }
	if _, err := Generate(Config{Model: "haiku", APIKey: "k", RecordUsage: rec}, "p"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Label != "insight" || got[0].Model != "claude-haiku-5-5" || got[0].InputTokens != 900 || got[0].OutputTokens != 40 {
		t.Errorf("usage = %+v", got)
	}
	fakeAPI(t, 529, `{"type":"error","error":{"type":"overloaded_error"}}`)
	_, _ = Generate(Config{APIKey: "k", RecordUsage: rec}, "p")
	if len(got) != 1 {
		t.Errorf("a failed call recorded usage: %+v", got)
	}
}

// TestGenerate_DefaultsToHaikuAndTheUsageFile: an empty model is haiku, and
// without RecordUsage the line goes to ~/.aimux/data/usage.jsonl.
func TestGenerate_DefaultsToHaikuAndTheUsageFile(t *testing.T) {
	body, _ := fakeAPI(t, 200, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, err := Generate(Config{APIKey: "k"}, "p"); err != nil {
		t.Fatal(err)
	}
	if (*body)["model"] != "claude-haiku-5-5" {
		t.Errorf("model = %v, want haiku", (*body)["model"])
	}
	data, err := os.ReadFile(cost.DefaultUsagePath())
	if err != nil || !strings.Contains(string(data), `"label":"insight"`) {
		t.Errorf("usage file = %s (%v)", data, err)
	}
}

// TestNewClient_Shape pins insight's client: the Messages API only, 30 s,
// label insight.
func TestNewClient_Shape(t *testing.T) {
	c := newClient(Config{}, "k")
	a, ok := c.Primary.(*llm.API)
	if !ok || a.Client.Timeout != 30*time.Second || a.Key != "k" || a.URL != messagesURL {
		t.Errorf("primary = %#v", c.Primary)
	}
	if c.Fallback != nil || c.Label != "insight" {
		t.Errorf("fallback %v label %q", c.Fallback, c.Label)
	}
}

// TestGenerate_ErrorCarriesNoResponseBody: the API's message may echo the
// prompt; the error keeps the status and error type only.
func TestGenerate_ErrorCarriesNoResponseBody(t *testing.T) {
	fakeAPI(t, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"echoed: zebra lantern marmalade"}}`)
	_, err := Generate(Config{APIKey: "k"}, "zebra lantern marmalade")
	if err == nil || strings.Contains(err.Error(), "zebra") || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "invalid_request_error") {
		t.Errorf("err = %v, want status and error type only", err)
	}
}

// TestGenerate_UnknownModelLoggedOnce: an unknown model name falls back to
// haiku and is logged once, not on every call.
func TestGenerate_UnknownModelLoggedOnce(t *testing.T) {
	var logs []string
	old := logf
	logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { logf = old })
	for i := 0; i < 2; i++ {
		body, _ := fakeAPI(t, 200, `{"content":[{"type":"text","text":"ok"}]}`)
		if _, err := Generate(Config{Model: "gpt-once-1", APIKey: "k", RecordUsage: func(cost.Usage) error { return nil }}, "p"); err != nil {
			t.Fatal(err)
		}
		if (*body)["model"] != "claude-haiku-5-5" {
			t.Errorf("model = %v", (*body)["model"])
		}
	}
	if len(logs) != 1 || logs[0] != `insight: unknown model "gpt-once-1", using claude-haiku-5-5` {
		t.Errorf("logs = %q", logs)
	}
	if _, err := Generate(Config{Model: "sonnet", APIKey: "k", RecordUsage: func(cost.Usage) error { return nil }}, "p"); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Errorf("a known model was logged: %q", logs)
	}
}
