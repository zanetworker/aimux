package history

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zanetworker/aimux/internal/cost"
)

// fakeMessagesAPI stands in for the Anthropic Messages API; it answers every
// request with reply and counts calls. The model asked for is kept.
func fakeMessagesAPI(t *testing.T, reply string) (calls *int32, model *atomic.Value) {
	t.Helper()
	calls = new(int32)
	model = &atomic.Value{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		model.Store(req.Model)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": reply}},
			"usage":   map[string]int{"input_tokens": 50, "output_tokens": 8},
		})
	}))
	t.Cleanup(srv.Close)
	old := anthropicMessagesURL
	anthropicMessagesURL = srv.URL
	t.Cleanup(func() { anthropicMessagesURL = old })
	return calls, model
}

func titlerSession(t *testing.T, dir, id string, meta *Meta) Session {
	t.Helper()
	p := filepath.Join(dir, id+".jsonl")
	line := `{"type":"user","message":{"role":"user","content":"Please fix the flaky login test"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if meta != nil {
		if err := UpdateMeta(p, func(m *Meta) { *m = *meta }); err != nil {
			t.Fatal(err)
		}
	}
	return Session{ID: id + "-0000-0000", FilePath: p, FirstPrompt: "Please fix the flaky login test", Title: func() string {
		if meta != nil {
			return meta.Title
		}
		return ""
	}()}
}

func TestGenerateTitles_SkipsOwnerTitlesAndMarksModelTitles(t *testing.T) {
	calls, model := fakeMessagesAPI(t, "Fix flaky login test")
	dir := t.TempDir()
	owner := titlerSession(t, dir, "owner123", &Meta{Title: "Mine", TitleBy: "owner"})
	plain := titlerSession(t, dir, "plain123", nil)

	n, err := GenerateTitles([]Session{owner, plain}, TitleConfig{APIKey: "test-key", Model: "haiku", Regenerate: true, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || atomic.LoadInt32(calls) != 1 {
		t.Fatalf("titled %d with %d calls, want 1 and 1", n, atomic.LoadInt32(calls))
	}
	if got := model.Load(); got != "claude-haiku-5-5" {
		t.Errorf("model = %v, want claude-haiku-5-5", got)
	}
	if m := LoadMeta(owner.FilePath); m.Title != "Mine" || m.TitleBy != "owner" {
		t.Errorf("owner meta changed: %+v", m)
	}
	if m := LoadMeta(plain.FilePath); m.Title != "Fix flaky login test" || m.TitleBy != "model" {
		t.Errorf("model meta = %+v", m)
	}
}

func TestGenerateTitles_StaleListKeepsOwnerTitle(t *testing.T) {
	fakeMessagesAPI(t, "Model title")
	dir := t.TempDir()
	s := titlerSession(t, dir, "race1234", nil)
	// the session list is stale: the owner named it after the list was read
	if err := UpdateMeta(s.FilePath, func(m *Meta) { m.Title, m.TitleBy = "Owner title", "owner" }); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateTitles([]Session{s}, TitleConfig{APIKey: "k", Output: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if m := LoadMeta(s.FilePath); m.Title != "Owner title" || m.TitleBy != "owner" {
		t.Errorf("meta = %+v, want the owner's title", m)
	}
}

func TestGenerateTitles_RecordsUsageUnderNaming(t *testing.T) {
	fakeMessagesAPI(t, "Fix flaky login test")
	var got []cost.Usage
	old := recordNamingUsage
	recordNamingUsage = func(u cost.Usage) error { got = append(got, u); return nil }
	t.Cleanup(func() { recordNamingUsage = old })
	s := titlerSession(t, t.TempDir(), "usage123", nil)
	if _, err := GenerateTitles([]Session{s}, TitleConfig{APIKey: "k", Output: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Label != "naming" || got[0].Model != "claude-haiku-5-5" || got[0].InputTokens != 50 || got[0].OutputTokens != 8 {
		t.Errorf("usage = %+v", got)
	}
}

func TestCallAnthropic_ErrorCarriesStatusAndTypeOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"echoed: Please fix the flaky login test"}}`)
	}))
	defer srv.Close()
	old := anthropicMessagesURL
	anthropicMessagesURL = srv.URL
	t.Cleanup(func() { anthropicMessagesURL = old })
	_, err := callAnthropic("Please fix the flaky login test", "claude-haiku-5-5", "k")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "flaky login") || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "invalid_request_error") {
		t.Errorf("err = %v, want status and error type only", err)
	}
}
