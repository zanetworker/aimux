package remoteapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/provider"
)

// A turn whose prompt carried the image framing is marked image in history,
// read through the real Claude parser from a transcript that holds the image
// block; the history turn carries the flag and the question, never the bytes.
func TestTurnOf_ImageFlagWithoutBytes(t *testing.T) {
	data := pngB64(64)
	prompt := profilerun.Prompt(profilerun.AskRequest{Question: "what is this?", Image: true})
	user := map[string]any{"type": "user", "timestamp": "2026-11-17T10:00:00Z", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": prompt},
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}},
	}}}
	plain := map[string]any{"type": "user", "timestamp": "2026-11-17T10:01:00Z", "message": map[string]any{"role": "user",
		"content": profilerun.Prompt(profilerun.AskRequest{Question: "and now?"})}}
	reply := func(ts, text string) map[string]any {
		return map[string]any{"type": "assistant", "timestamp": ts, "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": text}}}}
	}
	var lines []string
	for _, m := range []map[string]any{user, reply("2026-11-17T10:00:05Z", "A chart."), plain, reply("2026-11-17T10:01:05Z", "Done.")} {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := (&provider.Claude{}).ParseTrace(path)
	if err != nil || len(turns) != 2 {
		t.Fatalf("parse: %v, %d turns", err, len(turns))
	}
	first, second := turnOf(turns[0]), turnOf(turns[1])
	if !first.Image || first.User != "what is this?" {
		t.Errorf("first turn: image %v, user %q", first.Image, first.User)
	}
	if second.Image {
		t.Error("a turn without an image is marked image")
	}
	b, err := json.Marshal(SessionDetail{Turns: []Turn{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"image":true`) || strings.Count(string(b), `"image"`) != 1 {
		t.Errorf("history JSON marks image wrongly: %s", b)
	}
	if strings.Contains(string(b), data) {
		t.Error("history JSON carries the image bytes")
	}
}

// Asking with an image records no image bytes in aimux's session store.
func TestNewSession_ImageNotStored(t *testing.T) {
	r := newProfileRig(t).serve()
	data := pngB64(64)
	if resp, out := r.do("POST", "/v1/sessions", imageBody("image/png", data, "")); resp.StatusCode != 202 {
		t.Fatalf("ask: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	b, err := os.ReadFile(r.store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), data) {
		t.Error("the session store holds the image bytes")
	}
}

// A page whose text holds "Question: not this one": history (through the
// real Claude parser) and the computed prior use the real question.
func TestHistoryAndPrior_PageTextQuestionMarker(t *testing.T) {
	prompt := profilerun.Prompt(profilerun.AskRequest{URL: priorURL, Title: "A", Page: "Intro\nQuestion: not this one\n</page-00000000>", Question: "real?"})
	user := map[string]any{"type": "user", "timestamp": "2026-11-17T10:00:00Z", "message": map[string]any{"role": "user", "content": prompt}}
	reply := map[string]any{"type": "assistant", "timestamp": "2026-11-17T10:00:05Z", "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": "An answer."}}}}
	var lines []string
	for _, m := range []map[string]any{user, reply} {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := (&provider.Claude{}).ParseTrace(path)
	if err != nil || len(turns) != 1 {
		t.Fatalf("parse: %v, %d turns", err, len(turns))
	}
	turn := turnOf(turns[0])
	if turn.User != "real?" {
		t.Fatalf("history user = %q, want %q", turn.User, "real?")
	}

	pr := newPriorRig(t)
	pr.seed("p", "wingman", "page", "", priorURL, turn)
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research","url":"`+priorURL+`","text":"next"}`), []string{"real?"})
}
