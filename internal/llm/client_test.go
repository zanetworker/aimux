package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
)

// privateText stands for session text: no error or log may contain it.
const privateText = "zebra lantern marmalade"

// fakeBackend answers with answer and keeps the requests.
type fakeBackend struct {
	reqs   []Request
	answer func(Request) (Reply, error)
}

func (f *fakeBackend) Complete(_ context.Context, req Request) (Reply, error) {
	f.reqs = append(f.reqs, req)
	return f.answer(req)
}

func TestResolveModel(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  string
		known bool
	}{
		{"", "claude-haiku-5-5", true},
		{"haiku", "claude-haiku-5-5", true},
		{" Sonnet ", "claude-sonnet-5-5", true},
		{"opus", "claude-opus-5-5", true},
		{"claude-opus-5-5", "claude-opus-5-5", true},
		{"gpt-9", "claude-haiku-5-5", false},
	} {
		got, known := ResolveModel(tc.in)
		if got != tc.want || known != tc.known {
			t.Errorf("ResolveModel(%q) = %q, %v; want %q, %v", tc.in, got, known, tc.want, tc.known)
		}
	}
}

func TestAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if APIKey("") != "" {
		t.Error("no key: want empty")
	}
	if APIKey("cfg") != "cfg" {
		t.Error("the configured key wins")
	}
	t.Setenv("ANTHROPIC_API_KEY", "env")
	if APIKey("") != "env" || APIKey("cfg") != "cfg" {
		t.Error("environment key is the fallback only")
	}
}

func TestClient_RecordsUsageUnderItsLabel(t *testing.T) {
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local)
	var got []cost.Usage
	primary := &fakeBackend{answer: func(Request) (Reply, error) {
		return Reply{Text: "ok", InputTokens: 300, OutputTokens: 40}, nil
	}}
	c := &Client{Primary: primary, Label: "insight", Now: func() time.Time { return at },
		Record: func(u cost.Usage) error { got = append(got, u); return nil }}
	reply, err := c.Complete(context.Background(), Request{Model: "sonnet", Prompt: "p", MaxTokens: 10})
	if err != nil || reply.Text != "ok" {
		t.Fatalf("reply %+v err %v", reply, err)
	}
	if len(got) != 1 {
		t.Fatalf("usage = %+v, want one line", got)
	}
	u := got[0]
	if u.Label != "insight" || u.Model != "claude-sonnet-5-5" || u.InputTokens != 300 || u.OutputTokens != 40 || !u.At.Equal(at.UTC()) || u.At.Location() != time.UTC {
		t.Errorf("usage = %+v", u)
	}
}

func TestClient_RecordsTheRepliedModelAndKeepsUnknownIDs(t *testing.T) {
	var got []cost.Usage
	c := &Client{Label: "x", Record: func(u cost.Usage) error { got = append(got, u); return nil },
		Primary: &fakeBackend{answer: func(Request) (Reply, error) { return Reply{Text: "a", Model: "claude-haiku-4-5-20251001"}, nil }}}
	if _, err := c.Complete(context.Background(), Request{Model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	cost5 := 0.5
	c.Primary = &fakeBackend{answer: func(Request) (Reply, error) { return Reply{Text: "a", CostUSD: &cost5, Estimated: true}, nil }}
	if _, err := c.Complete(context.Background(), Request{Model: "claude-x-1"}); err != nil {
		t.Fatal(err)
	}
	if got[0].Model != "claude-haiku-4-5-20251001" {
		t.Errorf("replied model: %q", got[0].Model)
	}
	if got[1].Model != "claude-x-1" || got[1].CostUSD == nil || *got[1].CostUSD != 0.5 || !got[1].Estimated {
		t.Errorf("unknown id kept with cost and estimate: %+v", got[1])
	}
}

func TestClient_DefaultRecordWritesUsageFile(t *testing.T) {
	c := &Client{Label: "insight", Primary: &fakeBackend{answer: func(Request) (Reply, error) {
		return Reply{Text: "a", InputTokens: 1, OutputTokens: 2}, nil
	}}}
	if _, err := c.Complete(context.Background(), Request{Model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".aimux", "data", "usage.jsonl")) // #nosec G304 -- test home
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"label":"insight"`) || !strings.Contains(string(data), `"model":"claude-haiku-5-5"`) {
		t.Errorf("usage file = %s", data)
	}
}

func TestClient_FailureRecordsNothing(t *testing.T) {
	var got []cost.Usage
	c := &Client{Label: "naming", Record: func(u cost.Usage) error { got = append(got, u); return nil },
		Primary: &fakeBackend{answer: func(Request) (Reply, error) { return Reply{}, fmt.Errorf("x: %w", ErrModelCall) }}}
	if _, err := c.Complete(context.Background(), Request{Model: "haiku"}); !errors.Is(err, ErrModelCall) {
		t.Errorf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("usage on failure: %+v", got)
	}
}

func TestClient_NoBackend(t *testing.T) {
	if _, err := (&Client{Label: "x"}).Complete(context.Background(), Request{}); !errors.Is(err, ErrNoBackend) {
		t.Errorf("err = %v", err)
	}
}

func TestClient_FallbackOnceWithHaikuWhenModelNotFound(t *testing.T) {
	var logs []string
	var got []cost.Usage
	primary := &fakeBackend{answer: func(Request) (Reply, error) { return Reply{}, fmt.Errorf("404: %w", ErrModelNotFound) }}
	fallback := &fakeBackend{answer: func(Request) (Reply, error) { return Reply{Text: "ok", InputTokens: 9}, nil }}
	c := &Client{Primary: primary, Fallback: fallback, Label: "naming",
		Record: func(u cost.Usage) error { got = append(got, u); return nil },
		Logf:   func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
	if _, err := c.Complete(context.Background(), Request{Model: "claude-haiku-5-5", Prompt: privateText}); err != nil {
		t.Fatal(err)
	}
	if len(fallback.reqs) != 1 || fallback.reqs[0].Model != "haiku" {
		t.Errorf("fallback requests = %+v", fallback.reqs)
	}
	if len(logs) != 1 || logs[0] != "naming: model claude-haiku-5-5 not found, retrying once with the fallback, model haiku" {
		t.Errorf("logs = %q", logs)
	}
	if len(got) != 1 || got[0].Model != "claude-haiku-5-5" {
		t.Errorf("usage = %+v, want haiku recorded as its id", got)
	}
	// without a fallback the error comes back
	c.Fallback = nil
	if _, err := c.Complete(context.Background(), Request{Model: "m"}); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("no fallback: err = %v", err)
	}
}

func TestClient_RecordFailureIsLoggedNotReturned(t *testing.T) {
	var logs []string
	c := &Client{Label: "titles", Record: func(cost.Usage) error { return errors.New("disk full") },
		Logf:    func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
		Primary: &fakeBackend{answer: func(Request) (Reply, error) { return Reply{Text: "a"}, nil }}}
	if _, err := c.Complete(context.Background(), Request{Model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0] != "titles: disk full" {
		t.Errorf("logs = %q", logs)
	}
}

func TestBackends(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	p, fb := Backends("", MessagesURL)
	if c, ok := p.(*CLI); !ok || fb != nil || c.Bin != "claude" || c.Timeout != 60*time.Second {
		t.Errorf("no key: primary %T fallback %v, want claude -p only", p, fb)
	}
	p, fb = Backends("cfg-key", "http://fake")
	if a, ok := p.(*API); !ok || a.Key != "cfg-key" || a.URL != "http://fake" || a.Client.Timeout != 60*time.Second {
		t.Errorf("config key: primary %T", p)
	}
	if _, ok := fb.(*CLI); !ok {
		t.Errorf("fallback %T, want claude -p", fb)
	}
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	if p, _ := Backends("", MessagesURL); p.(*API).Key != "env-key" {
		t.Error("environment key not used")
	}
}

func TestClient_FallbackModelIsConfigurable(t *testing.T) {
	primary := &fakeBackend{answer: func(Request) (Reply, error) { return Reply{}, ErrModelNotFound }}
	fallback := &fakeBackend{answer: func(Request) (Reply, error) { return Reply{Text: "ok"}, nil }}
	var logs []string
	c := &Client{Primary: primary, Fallback: fallback, FallbackModel: "sonnet", Label: "x",
		Record: func(cost.Usage) error { return nil },
		Logf:   func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
	if _, err := c.Complete(context.Background(), Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if fallback.reqs[0].Model != "sonnet" {
		t.Errorf("fallback model = %q, want sonnet", fallback.reqs[0].Model)
	}
	if len(logs) != 1 || logs[0] != "x: model m not found, retrying once with the fallback, model sonnet" {
		t.Errorf("logs = %q", logs)
	}
}
