package profilerun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// A page session started through the profile Engine runs the real adapter
// path (PageSpec) and must send the lockdown, the page once, and the model.
func TestPageEngine_NoToolsMeta(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "record.jsonl")
	t.Setenv(fakeAgentEnv, "1")
	t.Setenv(fakeRecordEnv, rec)
	mgr := NewPageManager(os.Args[0], DefaultPageModel)
	t.Cleanup(mgr.Close)
	var e Engine = NewPageEngine(mgr, filepath.Join(t.TempDir(), "sessions"), DefaultPageModel)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sid, err := e.New(ctx, Meta{Profile: "page", URL: "https://a.example", Title: "A page"}, "PAGE BODY")
	if err != nil || sid == "" {
		t.Fatalf("New = %q %v", sid, err)
	}
	var text strings.Builder
	stop, err := e.Send(ctx, sid, "first question", func(ev acprun.Event) {
		if s, ok := deltaText(ev); ok {
			text.WriteString(s)
		}
	})
	if err != nil || stop != "end_turn" || text.String() != "answer" {
		t.Fatalf("Send = %q %v, text %q", stop, err, text.String())
	}
	if _, err := e.Send(ctx, sid, "second question", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}

	var prompts []string
	var news int
	for _, r := range readRecord(t, rec) {
		switch r.Method {
		case "session/new", "session/load":
			news++
			opts, _ := r.Meta["claudeCode"].(map[string]any)["options"].(map[string]any)
			want := map[string]any{"tools": []any{}, "settingSources": []any{}, "mcpServers": map[string]any{},
				"strictMcpConfig": true, "model": DefaultPageModel}
			if !reflect.DeepEqual(opts, want) {
				t.Fatalf("%s _meta.claudeCode.options = %v, want %v", r.Method, opts, want)
			}
		case "session/prompt":
			prompts = append(prompts, r.Value)
		}
	}
	if news == 0 || len(prompts) != 2 {
		t.Fatalf("saw %d session starts and prompts %q", news, prompts)
	}
	if !strings.Contains(prompts[0], "PAGE BODY") || !strings.Contains(prompts[0], "URL: https://a.example") ||
		!strings.HasSuffix(prompts[0], "Question: first question") {
		t.Fatalf("first prompt does not frame the page: %q", prompts[0])
	}
	if strings.Contains(prompts[1], "PAGE BODY") || !strings.HasSuffix(prompts[1], "Question: second question") ||
		!strings.Contains(prompts[1], "untrusted") {
		t.Fatalf("follow-up prompt %q", prompts[1])
	}
}

// A turn's model choice (WithModel) switches the page session with
// session/set_config_option before its prompt; no choice is the default.
func TestPageEngine_SendUsesTurnModel(t *testing.T) {
	a := &pgAdapter{}
	e := newPageEngine(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sid, err := e.New(ctx, Meta{Profile: "page", URL: "u"}, "P")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Send(WithModel(ctx, OpusPageModel), sid, "q1", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	r := a.runner(sid)
	if r.Model() != OpusPageModel {
		t.Fatalf("model after an opus turn = %q", r.Model())
	}
	if _, err := e.Send(ctx, sid, "q2", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	if sets, _ := r.history(); r.Model() != DefaultPageModel || sets[len(sets)-1] != DefaultPageModel {
		t.Fatalf("no choice: model %q, switches %v", r.Model(), sets)
	}
}

// A session's profile model (SessionModel, a user profile extending
// builtin:page with model.name opus) is the default for a turn with no
// model choice; a turn's choice still wins, and a profile model other than
// sonnet or opus is refused.
func TestPageEngine_SendDefaultsToTheProfileModel(t *testing.T) {
	a := &pgAdapter{}
	e := newPageEngine(t, a)
	profModel := OpusPageModel
	e.SessionModel = func(string) string { return profModel }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sid, err := e.New(ctx, Meta{Profile: "my-page", URL: "u"}, "P")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Send(ctx, sid, "q1", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	r := a.runner(sid)
	if r.Model() != OpusPageModel {
		t.Fatalf("no choice under an opus profile: model %q, want opus", r.Model())
	}
	if _, err := e.Send(WithModel(ctx, DefaultPageModel), sid, "q2", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	if r.Model() != DefaultPageModel {
		t.Fatalf("turn choice sonnet: model %q", r.Model())
	}
	profModel = "claude-opus-4-6"
	if _, err := e.Send(ctx, sid, "q3", func(acprun.Event) {}); err == nil || !strings.Contains(err.Error(), "claude-opus-4-6") {
		t.Fatalf("unsupported profile model: %v", err)
	}
}
