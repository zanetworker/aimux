package decide

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/llm"
)

type fakeLLM struct {
	reply llm.Reply
	err   error
	req   llm.Request
}

func (f *fakeLLM) Complete(_ context.Context, req llm.Request) (llm.Reply, error) {
	f.req = req
	return f.reply, f.err
}

func TestChat_PromptAndParse(t *testing.T) {
	c := 0.002
	fake := &fakeLLM{reply: llm.Reply{Text: "```json\n{\"choice\": \"workspace\", \"confidence\": 0.8}\n```", Model: "claude-haiku-5-5", CostUSD: &c, InputTokens: 300, OutputTokens: 12}}
	recorded := 0
	client := &llm.Client{Primary: fake, Label: "other", Record: func(cost.Usage) error { recorded++; return nil }}
	b := NewChat(client, "haiku")
	a, err := b.Decide(context.Background(), Request{State: map[string]string{"question": "my jira tickets"}, Question: testQuestion})
	if err != nil {
		t.Fatal(err)
	}
	if a.Choice != "workspace" || a.Confidence != 0.8 || a.Probabilities != nil || a.Backend != "chat" || a.Model != "claude-haiku-5-5" {
		t.Fatalf("answer %+v", a)
	}
	if a.CostUSD == nil || *a.CostUSD != 0.002 || a.inputTokens != 300 {
		t.Fatalf("cost/tokens %+v", a)
	}
	if recorded != 0 {
		t.Fatalf("the client recorded %d usage lines; the Decider records the one line", recorded)
	}
	if fake.req.Model != llm.DefaultModel {
		t.Errorf("model %q", fake.req.Model)
	}
	for _, s := range []string{testQuestion.Instructions, testQuestion.Options["web"], `"choice"`, `"confidence"`} {
		if !strings.Contains(fake.req.System, s) {
			t.Errorf("system prompt lacks %q", s)
		}
	}
	if !strings.Contains(fake.req.Prompt, "my jira tickets") {
		t.Errorf("prompt lacks the state: %q", fake.req.Prompt)
	}
}

func TestChat_BadReplies(t *testing.T) {
	for _, text := range []string{"no json here", `{"choice":"nope","confidence":0.9}`, `{"choice":"web","confidence":7}`} {
		fake := &fakeLLM{reply: llm.Reply{Text: text}}
		b := NewChat(&llm.Client{Primary: fake, Record: func(cost.Usage) error { return nil }}, "haiku")
		if _, err := b.Decide(context.Background(), Request{State: "s", Question: testQuestion}); !errors.Is(err, ErrBadReply) {
			t.Errorf("%q: want ErrBadReply, got %v", text, err)
		}
	}
}

func TestChat_CallError(t *testing.T) {
	fake := &fakeLLM{err: llm.ErrUnauthorized}
	b := NewChat(&llm.Client{Primary: fake}, "haiku")
	if _, err := b.Decide(context.Background(), Request{State: "s", Question: testQuestion}); !errors.Is(err, llm.ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if _, err := NewChat(nil, "haiku").Decide(context.Background(), Request{State: "s", Question: testQuestion}); err == nil {
		t.Fatal("nil client must error")
	}
}
