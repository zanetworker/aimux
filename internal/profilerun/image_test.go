package profilerun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
)

var testImage = &acprun.Image{MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUg=="}

// A page turn with an image sends the framed text block, then the image
// block, over the real adapter path.
func TestPageEngine_SendsImageBlock(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "record.jsonl")
	t.Setenv(fakeAgentEnv, "1")
	t.Setenv(fakeRecordEnv, rec)
	mgr := NewPageManager(os.Args[0], DefaultPageModel)
	t.Cleanup(mgr.Close)
	e := NewPageEngine(mgr, filepath.Join(t.TempDir(), "sessions"), DefaultPageModel)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sid, err := e.New(ctx, Meta{Profile: "page", URL: "https://a.example", Title: "A"}, "PAGE")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Send(WithImage(ctx, testImage), sid, "what is this?", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Send(ctx, sid, "and now?", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	var prompts []recorded
	for _, r := range readRecord(t, rec) {
		if r.Method == "session/prompt" {
			prompts = append(prompts, r)
		}
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %+v", prompts)
	}
	want := []any{map[string]any{"type": "text"}, map[string]any{"type": "image", "mimeType": "image/png", "data": testImage.Data}}
	if got := prompts[0].Meta["blocks"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("image turn blocks = %v, want %v", got, want)
	}
	if !strings.Contains(prompts[0].Value, imageFraming) || !strings.HasSuffix(prompts[0].Value, "Question: what is this?") {
		t.Fatalf("image turn text %q", prompts[0].Value)
	}
	if got := prompts[1].Meta["blocks"]; !reflect.DeepEqual(got, []any{map[string]any{"type": "text"}}) {
		t.Fatalf("text turn blocks = %v", got)
	}
	if strings.Contains(prompts[1].Value, imageFraming) {
		t.Fatalf("text turn framed an image: %q", prompts[1].Value)
	}
}

// streamUser is the one stream-json user message a turn with an image
// writes to claude's stdin.
type streamUser struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Source *struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		} `json:"content"`
	} `json:"message"`
}

// A sandbox turn with an image runs claude with --input-format stream-json
// and writes, after the MCP config line, one JSON user message: the framed
// prompt as a text block, then the image block.
func TestSandboxEngine_StreamJSONWithImage(t *testing.T) {
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly", URL: "https://a.example", Title: "T"}, "PAGE")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Send(WithImage(context.Background(), testImage), sid, "what is this?", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	x := b.execs[0]
	if got := argAfter(x.args, "--input-format"); got != "stream-json" {
		t.Fatalf("--input-format = %q in %q", got, x.args)
	}
	line, rest, _ := strings.Cut(x.prompt, "\n")
	if rest != "" {
		t.Fatalf("stdin after the config line is not one line: %q", x.prompt)
	}
	var m streamUser
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("stdin line 2 is not JSON: %v: %q", err, line)
	}
	c := m.Message.Content
	if m.Type != "user" || m.Message.Role != "user" || len(c) != 2 {
		t.Fatalf("message = %+v", m)
	}
	if c[0].Type != "text" || !strings.Contains(c[0].Text, "PAGE") || !strings.Contains(c[0].Text, imageFraming) ||
		!strings.HasSuffix(c[0].Text, "Question: what is this?") || QuestionOf(c[0].Text) != "what is this?" {
		t.Fatalf("text block %q", c[0].Text)
	}
	if c[1].Type != "image" || c[1].Source == nil || c[1].Source.Type != "base64" ||
		c[1].Source.MediaType != "image/png" || c[1].Source.Data != testImage.Data {
		t.Fatalf("image block %+v", c[1])
	}
}

// Without an image the stdin and argv are today's: plain prompt text, no
// --input-format.
func TestSandboxEngine_TextOnlyUnchanged(t *testing.T) {
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Send(context.Background(), sid, "q", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	x := b.execs[0]
	if _, ok := flagIndex(x.args, "--input-format"); ok {
		t.Fatalf("--input-format without an image: %q", x.args)
	}
	if want := sandboxPrompt(nil, "q"); x.prompt != want {
		t.Fatalf("stdin prompt = %q, want %q", x.prompt, want)
	}
}

func flagIndex(args []string, flag string) (int, bool) {
	for i, a := range args {
		if a == flag {
			return i, true
		}
	}
	return 0, false
}

// The image is framed as untrusted data from the person's screen in the
// text block, under the same rules as page text, in both prompt builders;
// the question is still found, and a turn without an image has no line.
func TestImage_UntrustedFraming(t *testing.T) {
	for name, p := range map[string]string{
		"page":          Prompt(AskRequest{Image: true, Question: "Q"}),
		"page first":    Prompt(AskRequest{URL: "https://a.example", Page: "P", Image: true, Prior: priorQs, Question: "Q"}),
		"sandbox":       sandboxTurnPrompt(nil, nil, true, "Q"),
		"sandbox first": sandboxTurnPrompt(&AskRequest{URL: "https://a.example", Page: "P"}, priorQs, true, "Q"),
	} {
		i := strings.Index(p, imageFraming)
		if i < 0 {
			t.Errorf("%s: no image framing: %q", name, p)
			continue
		}
		if !strings.Contains(strings.ToLower(imageFraming), "untrusted") || !strings.Contains(imageFraming, "screen") {
			t.Fatalf("framing does not say untrusted screen data: %q", imageFraming)
		}
		if q := strings.LastIndex(p, "Question: Q"); q < i {
			t.Errorf("%s: framing after the question: %q", name, p)
		}
		if got := QuestionOf(p); got != "Q" {
			t.Errorf("%s: QuestionOf = %q", name, got)
		}
	}
	for name, p := range map[string]string{
		"page":    Prompt(AskRequest{Question: "Q"}),
		"sandbox": sandboxTurnPrompt(nil, nil, false, "Q"),
	} {
		if strings.Contains(p, imageFraming) {
			t.Errorf("%s: framing without an image: %q", name, p)
		}
	}
}

func TestImageContext(t *testing.T) {
	if ImageFrom(context.Background()) != nil {
		t.Fatal("empty ctx has an image")
	}
	if got := ImageFrom(WithImage(context.Background(), testImage)); got != testImage {
		t.Fatalf("image = %v", got)
	}
}
