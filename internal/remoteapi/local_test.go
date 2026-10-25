package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/trace"
)

func localFixture(t *testing.T, b *Broker) *LocalIndex {
	t.Helper()
	root := t.TempDir()
	projects, registry := filepath.Join(root, "projects"), filepath.Join(root, "sessions")
	for _, d := range []string{filepath.Join(projects, "-w"), registry} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"idle", "open", "busy"} {
		if err := os.WriteFile(filepath.Join(projects, "-w", id+".jsonl"), []byte(`{"cwd":"/w"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := fmt.Sprintf(`{"pid":%d,"sessionId":"open"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(registry, "1.json"), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	return &LocalIndex{
		ProjectsDir: projects, RegistryDir: registry, Broker: b,
		Discover: func(opts history.DiscoverOpts, dir string) ([]history.Session, error) {
			return []history.Session{
				{ID: "idle", Provider: "claude", Title: "Fix the cache", Project: "/w", LastActive: now},
				{ID: "open", Provider: "claude", FirstPrompt: strings.Repeat("long prompt ", 30), LastActive: now},
				{ID: "busy", Provider: "claude", Title: "Busy one", LastActive: now},
				{ID: "sub", Provider: "claude", IsSubagent: true},
				{ID: "cron", Provider: "claude", Automated: true},
				{ID: "cx", Provider: "codex"},
			}[:limitOrAll(opts.Limit, 6)], nil
		},
		ParseTrace: func(path string) ([]trace.Turn, error) {
			var turns []trace.Turn
			for i := 1; i <= 5; i++ {
				turns = append(turns, trace.Turn{Number: i, UserLines: []string{fmt.Sprintf("q%d", i)},
					OutputLines: []string{strings.Repeat("a", 20000)}, Actions: []trace.ToolSpan{{Name: "Bash", Snippet: "ls", Success: true}}})
			}
			return turns, nil
		},
	}
}

func TestLocalIndex_Sessions(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		<-release
		return "end_turn", nil
	}}
	var b *Broker
	b = NewBroker(f.deps(&b), BrokerOptions{})
	if _, err := b.Start("busy", "work", ""); err != nil {
		t.Fatal(err)
	}
	x := localFixture(t, b)
	got, err := x.Sessions(context.Background(), 50, false)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, s := range got {
		states[s.ID] = s.State
	}
	want := map[string]string{"idle": StateIdle, "open": StateOpenInTerminal, "busy": StateBusy}
	if fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("states %v, want %v (subagents, automated and codex left out)", states, want)
	}
	for _, s := range got {
		if s.ID == "open" && (utf8.RuneCountInString(s.Title) > 81 || s.Title == "") {
			t.Fatalf("title from first prompt not shortened: %q", s.Title)
		}
	}
}

func TestLocalIndex_Session(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	d, err := x.Session(context.Background(), "idle", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Turns) != 2 {
		t.Fatalf("%d turns, want the last 2", len(d.Turns))
	}
	last := d.Turns[1]
	if last.Number != 5 || last.User != "q5" || len(last.Tools) != 1 || last.Tools[0].Name != "Bash" {
		t.Fatalf("last turn %+v", last)
	}
	if len(last.Reply) > maxTextLen+len("…") {
		t.Fatalf("reply not capped: %d bytes", len(last.Reply))
	}
	if d.State != StateIdle {
		t.Fatalf("state %q", d.State)
	}
	if _, err := x.Session(context.Background(), "missing", 2); !errors.Is(err, acprun.ErrUnknownSession) {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestLocalIndex_IncludeAutomated(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	got, err := x.Sessions(context.Background(), 50, true)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, s := range got {
		ids[s.ID] = true
	}
	if !ids["cron"] || ids["sub"] || ids["cx"] {
		t.Fatalf("ids %v, want automated sessions included, still no subagents or codex", ids)
	}
}

func TestLocalIndex_SessionDetailHasMetadata(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	d, err := x.Session(context.Background(), "idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Fix the cache" || d.Project != "/w" || d.LastActive.IsZero() {
		t.Fatalf("detail metadata %+v, want title, project and last active from the index", d.Session)
	}
}

// limitOrAll mirrors history.DiscoverOpts: a limit of 0 means no limit.
func limitOrAll(limit, n int) int {
	if limit <= 0 {
		return n
	}
	return min(limit, n)
}

func TestLocalIndex_OwnAdapterIsNotATerminal(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Owned = func(pid int) bool { return pid == os.Getpid() } // the "open" session's holder is aimux's adapter
	got, err := x.Sessions(context.Background(), 50, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.ID == "open" && s.State != StateIdle {
			t.Fatalf("aimux's own idle adapter reported as %s, want idle so clients can reply", s.State)
		}
	}
}

func TestLocalIndex_LimitAppliesAfterFiltering(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Discover = func(opts history.DiscoverOpts, dir string) ([]history.Session, error) {
		all := []history.Session{
			{ID: "cron1", Provider: "claude", Automated: true},
			{ID: "cron2", Provider: "claude", Automated: true},
			{ID: "cron3", Provider: "claude", Automated: true},
			{ID: "idle", Provider: "claude", Title: "the one interactive session"},
		}
		return all[:limitOrAll(opts.Limit, len(all))], nil
	}
	got, err := x.Sessions(context.Background(), 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "idle" {
		t.Fatalf("got %v, want the interactive session behind the automated ones", got)
	}
}

func TestCleanPrompt(t *testing.T) {
	tests := []struct{ in, want string }{
		{"fix the cache", "fix the cache"},
		{"[Image #1] what is this?", "what is this?"},
		{"[Image: original 3445x2245, displayed at 2000x1303. Multiply coordinates by 1.72 to map to original image.]", "(image)"},
		{"[Image #1]", "(image)"},
		{"The user sent a new message while you were working:\nare you still blocked?", "are you still blocked?"},
		{"Stop hook feedback:\n[python3 hook]: Docs check", "(hook feedback)"},
		{"Another Claude session sent a message: deploy done", "(message from another session)"},
		{"<task-notification>\n<task-id>x</task-id>", "(notification)"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := cleanPrompt(tt.in); got != tt.want {
			t.Errorf("cleanPrompt(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
