package decide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/keyring"
)

var testQuestion = Question{
	Name:         "mode",
	Instructions: "Pick the one mode this question needs.",
	Options: map[string]string{
		"page":      "Answer only from the web page.",
		"web":       "Needs a public web search.",
		"workspace": "Needs the owner's private sources.",
	},
}

func staticKey(k string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return k, nil }
}

const okReply = `{"model":"cloudflare/clef-flash","answers":{"mode":{"type":"choice","choice":"web","probabilities":{"page":0.1,"web":0.7,"workspace":0.2},"confidence":0.57}},"usage":{"cost":0.0000146,"input_tokens":386}}`

func TestOpenRouter_RequestShape(t *testing.T) {
	var body map[string]any
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		_, _ = io.WriteString(w, okReply)
	}))
	defer srv.Close()

	b := NewOpenRouter(staticKey("sk-test"), "cloudflare/clef-flash", true)
	b.URL = srv.URL + "/api/alpha/decisions"
	state := map[string]any{"facts": map[string]bool{"page_readable": true}, "question": "what's new in Go 1.27?"}
	if _, err := b.Decide(context.Background(), Request{State: state, Question: testQuestion}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/alpha/decisions" {
		t.Errorf("path %q", path)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("auth %q", auth)
	}
	if body["model"] != "cloudflare/clef-flash" {
		t.Errorf("model %v", body["model"])
	}
	if p, _ := body["provider"].(map[string]any); p["zdr"] != true {
		t.Errorf("provider %v", body["provider"])
	}
	wantState, _ := json.Marshal(state)
	if body["state"] != string(wantState) {
		t.Errorf("state %v, want the JSON string %s", body["state"], wantState)
	}
	q, _ := body["questions"].(map[string]any)["mode"].(map[string]any)
	if q["type"] != "choice" || q["instructions"] != testQuestion.Instructions {
		t.Errorf("question %v", q)
	}
	crit, _ := q["criteria"].(map[string]any)
	for k, v := range testQuestion.Options {
		if crit[k] != v {
			t.Errorf("criteria[%s] = %v, want %q", k, crit[k], v)
		}
	}
	if len(crit) != len(testQuestion.Options) {
		t.Errorf("criteria %v", crit)
	}
}

func TestOpenRouter_ZDRFalseOmitsNothing(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		_, _ = io.WriteString(w, okReply)
	}))
	defer srv.Close()
	b := NewOpenRouter(staticKey("k"), "m", false)
	b.URL = srv.URL
	if _, err := b.Decide(context.Background(), Request{State: 1, Question: testQuestion}); err != nil {
		t.Fatal(err)
	}
	if p, _ := body["provider"].(map[string]any); p["zdr"] != false {
		t.Errorf("provider %v", body["provider"])
	}
}

func TestOpenRouter_ParsesAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, okReply)
	}))
	defer srv.Close()
	b := NewOpenRouter(staticKey("k"), "cloudflare/clef-flash", true)
	b.URL = srv.URL
	a, err := b.Decide(context.Background(), Request{State: "s", Question: testQuestion})
	if err != nil {
		t.Fatal(err)
	}
	if a.Choice != "web" || a.Confidence != 0.57 || a.Probabilities["web"] != 0.7 || a.Probabilities["workspace"] != 0.2 {
		t.Fatalf("answer %+v", a)
	}
	if a.Backend != "openrouter" || a.Model != "cloudflare/clef-flash" {
		t.Fatalf("backend/model %q %q", a.Backend, a.Model)
	}
	if a.CostUSD == nil || *a.CostUSD != 0.0000146 {
		t.Fatalf("cost %v", a.CostUSD)
	}
}

func TestOpenRouter_NoKey(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	for _, key := range []func(context.Context) (string, error){
		func(context.Context) (string, error) { return "", fmt.Errorf("keychain x: %w", keyring.ErrNotFound) },
		staticKey(""),
		nil,
	} {
		b := NewOpenRouter(key, "m", true)
		b.URL = srv.URL
		a, err := b.Decide(context.Background(), Request{State: "s", Question: testQuestion})
		if !errors.Is(err, ErrNoKey) || a.Undecided != UndecidedNoKey {
			t.Fatalf("got %+v, %v", a, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("made %d HTTP calls without a key", hits.Load())
	}
}

func TestOpenRouter_KeyReadError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	b := NewOpenRouter(func(context.Context) (string, error) { return "", errors.New("keychain svc: exit status 36") }, "m", true)
	b.URL = srv.URL
	a, err := b.Decide(context.Background(), Request{State: "s", Question: testQuestion})
	if !errors.Is(err, ErrKeyRead) || errors.Is(err, ErrNoKey) || a.Undecided != UndecidedKeyError || hits.Load() != 0 {
		t.Fatalf("got %+v, %v, %d calls", a, err, hits.Load())
	}
}

func TestOpenRouter_KeyLookupGetsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var got context.Context
	b := NewOpenRouter(func(c context.Context) (string, error) { got = c; return "", keyring.ErrNotFound }, "m", true)
	_, _ = b.Decide(ctx, Request{State: "s", Question: testQuestion})
	if got == nil {
		t.Fatal("key func not called")
	}
	if _, ok := got.Deadline(); !ok {
		t.Fatal("key lookup must get the Ask context with its deadline")
	}
}

func TestOpenRouter_ErrorsCarryNoBody(t *testing.T) {
	const echo = "UNIQUE-STATE-ECHO"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad state `+echo+`"}}`)
	}))
	defer srv.Close()
	b := NewOpenRouter(staticKey("sk-secret-key"), "m", true)
	b.URL = srv.URL
	_, err := b.Decide(context.Background(), Request{State: echo, Question: testQuestion})
	if err == nil || strings.Contains(err.Error(), echo) || strings.Contains(err.Error(), "sk-secret-key") {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("err should name the status: %v", err)
	}
}

func TestOpenRouter_MissingAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"answers":{"other":{"choice":"web"}}}`)
	}))
	defer srv.Close()
	b := NewOpenRouter(staticKey("k"), "m", true)
	b.URL = srv.URL
	if _, err := b.Decide(context.Background(), Request{State: "s", Question: testQuestion}); !errors.Is(err, ErrBadReply) {
		t.Fatalf("want ErrBadReply, got %v", err)
	}
}
