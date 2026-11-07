package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
)

// titleFixture writes three sessions under dir, one per step of the title
// rule: a meta title, an ai-title only, and only a first message. It returns
// their file paths in that order.
func titleFixture(t *testing.T, dir string) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	user := func(text string) string {
		return `{"type":"user","message":{"role":"user","content":"` + text + `"},"timestamp":"2026-10-01T10:00:00Z"}`
	}
	reply := `{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-5","content":"ok","usage":{"input_tokens":2000,"output_tokens":100}},"timestamp":"2026-10-01T10:01:00Z"}`
	write := func(id string, lines ...string) string {
		p := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	meta := write("11111111-meta", user("meta first message"), `{"type":"ai-title","aiTitle":"Meta ai name"}`, reply)
	if err := history.UpdateMeta(meta, func(m *history.Meta) { m.Title = "Meta title"; m.TitleBy = "owner" }); err != nil {
		t.Fatal(err)
	}
	return []string{meta,
		write("22222222-ai", user("ai first message"), `{"type":"ai-title","aiTitle":"AI only name"}`, reply),
		write("33333333-first", user("plain first message"), reply)}
}

var wantTitles = []string{"Meta title", "AI only name", "plain first message"}

func TestSSE_TitlesFollowTheTitleRule(t *testing.T) {
	files := titleFixture(t, t.TempDir())
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		var out []agent.Agent
		for i, f := range files {
			out = append(out, agent.Agent{PID: 100 + i, Name: "a", ProviderName: "claude", Status: agent.StatusActive,
				TokensIn: 5000, SessionID: filepath.Base(f), SessionFile: f})
		}
		return out, nil
	})
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload struct {
			Agents []struct {
				PID   int    `json:"PID"`
				Title string `json:"Title"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Agents) != 3 {
			t.Fatalf("want 3 agents, got %d", len(payload.Agents))
		}
		for _, a := range payload.Agents {
			if want := wantTitles[a.PID-100]; a.Title != want {
				t.Errorf("agent %d Title = %q, want %q", a.PID, a.Title, want)
			}
		}
		return
	}
	t.Fatal("no agents event")
}

// fakeHome points the history scan at a temp ~/.claude/projects holding the
// title fixture, so the test never reads the real one.
func fakeHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	titleFixture(t, filepath.Join(home, ".claude", "projects", "-tmp-titles"))
}

func TestHistoryHandler_SendsShownTitle(t *testing.T) {
	fakeHome(t)
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/history")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var payload struct {
		Sessions []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			ShownTitle string `json:"shownTitle"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range payload.Sessions {
		got[s.ID] = s.ShownTitle
	}
	for i, id := range []string{"11111111-meta", "22222222-ai", "33333333-first"} {
		if got[id] != wantTitles[i] {
			t.Errorf("%s shownTitle = %q, want %q (all: %v)", id, got[id], wantTitles[i], got)
		}
	}
}

// The agents stream caps a long title (the cards clamp to two lines anyway).
func TestSSE_CapsLongTitles(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("word ", 60)
	f := filepath.Join(dir, "44444444-long.jsonl")
	line := `{"type":"user","message":{"role":"user","content":"x"}}` + "\n" + `{"type":"ai-title","aiTitle":"` + long + `"}` + "\n"
	if err := os.WriteFile(f, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{{PID: 7, Name: "a", ProviderName: "claude", Status: agent.StatusActive, TokensIn: 5000, SessionID: "l", SessionFile: f}}, nil
	})
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Get(s.URL() + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var payload struct {
			Agents []struct {
				Title string `json:"Title"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &payload); err != nil {
			t.Fatal(err)
		}
		if got := payload.Agents[0].Title; got != history.Shorten(long, 120) {
			t.Errorf("Title = %q (%d bytes), want it cut at 120 bytes", got, len(got))
		}
		return
	}
	t.Fatal("no agents event")
}
