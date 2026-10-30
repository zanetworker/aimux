package remoteapi

import (
	"context"
	"encoding/json"
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
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/terminput"
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
				{ID: "idle", Provider: "claude", Title: "Fix the cache", Project: "/w", Cwd: "/w", LastActive: now},
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

// A session whose recorded folder is a repo carries its web address and
// branch; any other session omits repo, not an empty object.
func TestLocalIndex_SessionDetailCarriesRepo(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	var asked []string
	x.Repo = func(dir string) (string, string, string, bool) {
		asked = append(asked, dir)
		return "https://github.com/acme/widget", "main", "/w", true
	}
	d, err := x.Session(context.Background(), "idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Repo == nil || d.Repo.Web != "https://github.com/acme/widget" || d.Repo.Branch != "main" || d.Repo.Root != "/w" {
		t.Fatalf("repo %+v, want the web address and branch", d.Repo)
	}
	if len(asked) != 1 || asked[0] != "/w" {
		t.Fatalf("Repo asked for %v, want the session's recorded cwd /w", asked)
	}
	raw, _ := json.Marshal(d)
	if !strings.Contains(string(raw), `"repo":{"web":"https://github.com/acme/widget","branch":"main","root":"/w"}`) {
		t.Fatalf("detail JSON %s, want repo{web,branch}", raw)
	}

	x.Repo = func(string) (string, string, string, bool) { return "", "", "", false }
	d, err = x.Session(context.Background(), "idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(d)
	if d.Repo != nil || strings.Contains(string(raw), `"repo"`) {
		t.Fatalf("not a repo: detail %s, want repo omitted", raw)
	}

	x.Repo = nil
	if d, _ = x.Session(context.Background(), "idle", 1); d.Repo != nil {
		t.Fatalf("no Repo lookup configured: repo %+v, want nil", d.Repo)
	}
}

// The repo is looked up in the recorded working directory only; Project is
// rebuilt from Claude's folder name, which different paths can share, so a
// session with no recorded cwd gets no repo rather than a guessed folder's.
func TestLocalIndex_RepoUsesRecordedCwd(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Discover = func(history.DiscoverOpts, string) ([]history.Session, error) {
		return []history.Session{
			{ID: "idle", Provider: "claude", Title: "t", Project: "/a/b/c", Cwd: "/a/b-c"},
			{ID: "open", Provider: "claude", Title: "u", Project: "/a/b/d"},
		}, nil
	}
	var asked []string
	x.Repo = func(dir string) (string, string, string, bool) {
		asked = append(asked, dir)
		return "https://github.com/acme/widget", "main", dir, true
	}
	repos := map[string]*Repo{}
	for _, id := range []string{"idle", "open"} {
		d, err := x.Session(context.Background(), id, 1)
		if err != nil {
			t.Fatal(err)
		}
		repos[id] = d.Repo
	}
	if len(asked) != 1 || asked[0] != "/a/b-c" {
		t.Fatalf("Repo asked for %v, want only the recorded cwd /a/b-c", asked)
	}
	if repos["idle"] == nil || repos["open"] != nil {
		t.Fatalf("repos %+v, want one for the recorded cwd and none for the guessed project", repos)
	}
}

func TestCwdOf(t *testing.T) {
	if got := cwdOf(nil); got != "" {
		t.Fatalf("no indexed session: %q, want empty", got)
	}
	if got := cwdOf(&history.Session{Project: "/a/b/d"}); got != "" {
		t.Fatalf("project only: %q, want empty", got)
	}
	if got := cwdOf(&history.Session{Project: "/a/b/c", Cwd: "/a/b-c"}); got != "/a/b-c" {
		t.Fatalf("recorded cwd: %q, want /a/b-c", got)
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

// The session view reads only the end of the file when it can: session files
// grow to hundreds of MB and only the latest turns are shown.
func TestLocalIndex_SessionReadsTheTail(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.ParseTrace = func(string) ([]trace.Turn, error) { t.Fatal("whole file parsed"); return nil, nil }
	var asked int
	x.ParseTail = func(path string, n int) ([]trace.Turn, error) {
		asked = n
		var turns []trace.Turn
		for i := 1; i <= n+1; i++ { // a tail may hold a turn more than asked
			turns = append(turns, trace.Turn{Number: i, UserLines: []string{fmt.Sprintf("q%d", i)}})
		}
		return turns, nil
	}
	d, err := x.Session(context.Background(), "idle", 3)
	if err != nil {
		t.Fatal(err)
	}
	if asked != 3 || len(d.Turns) != 3 || d.Turns[2].User != "q4" {
		t.Fatalf("asked %d, turns %+v; want the latest 3", asked, d.Turns)
	}
}

func TestLocalIndexSessionCarriesStats(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	calls := 0
	x.Discover = func(history.DiscoverOpts, string) ([]history.Session, error) {
		calls++
		return []history.Session{{ID: "idle", Provider: "claude", Title: "Fix the cache", StartTime: start,
			CostUSD: 1.25, TokensIn: 1000, TokensOut: 200, CacheReadTokens: 5000, CacheWriteTokens: 300,
			TurnCount: 12, DurationMin: 42.5, Model: "claude-opus-x", GitBranch: "main"}}, nil
	}
	d, err := x.Session(context.Background(), "idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := SessionStats{CostUSD: 1.25, TokensIn: 1000, TokensOut: 200, CacheReadTokens: 5000, CacheWriteTokens: 300,
		Turns: 12, StartTime: &start, DurationMin: 42.5, Model: "claude-opus-x", GitBranch: "main"}
	if d.Stats == nil || d.Stats.StartTime == nil || !d.Stats.StartTime.Equal(start) {
		t.Fatalf("stats %+v, want start time %v", d.Stats, start)
	}
	got := *d.Stats
	got.StartTime, want.StartTime = nil, nil
	if got != want {
		t.Fatalf("stats %+v, want %+v", got, want)
	}
	if calls != 1 {
		t.Fatalf("Discover called %d times, want one scan per request", calls)
	}
}

func TestLocalIndexSessionWithoutStartTimeOmitsIt(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Discover = func(history.DiscoverOpts, string) ([]history.Session, error) {
		return []history.Session{{ID: "idle", Provider: "claude", Title: "Fix the cache", TurnCount: 2}}, nil
	}
	d, err := x.Session(context.Background(), "idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stats == nil {
		t.Fatal("stats missing for an indexed session")
	}
	raw, err := json.Marshal(d.Stats)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["startTime"]; ok {
		t.Fatalf("stats %s: an index without a start time must leave startTime out, not send the zero time", raw)
	}
	if fields["turns"] != float64(2) {
		t.Fatalf("stats %s: want the other fields kept", raw)
	}
}

func TestLocalIndexSessionWithoutIndexHasNoStats(t *testing.T) {
	b := NewBroker(BrokerDeps{}, BrokerOptions{})
	x := localFixture(t, b)
	x.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "created.json")}
	if err := x.Created.Add("new1", "/w", "hello"); err != nil {
		t.Fatal(err)
	}
	d, err := x.Session(context.Background(), "new1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stats != nil {
		t.Fatalf("stats %+v, want none for a session not in the index", d.Stats)
	}
	// A file exists but the index does not list it yet: also no stats.
	x.Discover = func(history.DiscoverOpts, string) ([]history.Session, error) { return nil, nil }
	d, err = x.Session(context.Background(), "idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Stats != nil {
		t.Fatalf("stats %+v, want none when the index lacks the session", d.Stats)
	}
}

// fakeTerminalStatus records what Status was asked and answers st.
type fakeTerminalStatus struct {
	st             terminput.Status
	calls          []string // session ids
	registryStatus string
}

func (f *fakeTerminalStatus) Status(_ context.Context, id, registryStatus string) terminput.Status {
	f.calls = append(f.calls, id)
	f.registryStatus = registryStatus
	return f.st
}

func TestLocalIndex_SessionCarriesTerminal(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	reg := fmt.Sprintf(`{"pid":%d,"sessionId":"open","status":"busy"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(x.RegistryDir, "1.json"), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTerminalStatus{st: terminput.Status{State: "busy", Reason: "not_at_input",
		Prompt: &terminput.Prompt{ID: "p1", Question: "Do you want to create a.go?", Detail: "Create file\na.go", Options: []terminput.Option{{Key: "1", Label: "Yes"}, {Key: "3", Label: "No"}}}}}
	x.Terminal = f
	d, err := x.Session(context.Background(), "open", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != StateOpenInTerminal || d.Terminal == nil {
		t.Fatalf("state %s terminal %+v, want open_in_terminal with a terminal status", d.State, d.Terminal)
	}
	want := TerminalStatus{Status: "busy", CanType: false, Reason: "not_at_input", Prompt: &TerminalPrompt{PromptID: "p1",
		Question: "Do you want to create a.go?", Detail: "Create file\na.go", Options: []TerminalOption{{Key: "1", Label: "Yes"}, {Key: "3", Label: "No"}}}}
	if got, _ := json.Marshal(d.Terminal); string(got) != string(must(json.Marshal(want))) {
		t.Fatalf("terminal %s, want %s", got, must(json.Marshal(want)))
	}
	if got := string(must(json.Marshal(d.Terminal))); !strings.Contains(got, `"detail":"Create file\na.go"`) {
		t.Fatalf("terminal %s, want the prompt's detail", got)
	}
	if f.registryStatus != "busy" || len(f.calls) != 1 || f.calls[0] != "open" {
		t.Fatalf("asked %v with registry status %q, want open once with busy", f.calls, f.registryStatus)
	}

	// A session not open in a terminal has no terminal field, and the
	// terminal is not touched for it.
	f.calls = nil
	d, err = x.Session(context.Background(), "idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Terminal != nil || len(f.calls) != 0 {
		t.Fatalf("idle session terminal %+v, calls %v; want none", d.Terminal, f.calls)
	}
	if raw := string(must(json.Marshal(d))); strings.Contains(raw, `"terminal"`) {
		t.Fatalf("idle detail %s carries terminal", raw)
	}
}

// Title (push notification titles) never reads the terminal: no screen read
// or Apple Event for a notification.
func TestLocalIndex_TitleSkipsTerminal(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	reg := fmt.Sprintf(`{"pid":%d,"sessionId":"open","status":"busy"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(x.RegistryDir, "1.json"), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTerminalStatus{st: terminput.Status{State: "busy"}}
	x.Terminal = f
	want, err := x.Session(context.Background(), "open", 1)
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	got, err := x.Title(context.Background(), "open")
	if err != nil || got != want.Title || got == "" {
		t.Fatalf("title %q, %v, want %q", got, err, want.Title)
	}
	if len(f.calls) != 0 {
		t.Fatalf("Title asked the terminal: %v", f.calls)
	}
	if _, err := x.Title(context.Background(), "nope"); err == nil {
		t.Fatal("unknown session: want an error")
	}
}

// The queue routes' check reads neither the terminal nor the repository.
func TestLocalIndex_SessionExistsReadsNoTerminal(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	term := &fakeTerminalStatus{st: terminput.Status{State: "idle", CanType: true}}
	repos := 0
	x.Terminal = term
	x.Repo = func(string) (string, string, string, bool) { repos++; return "", "", "", false }
	if err := x.SessionExists(context.Background(), "open"); err != nil {
		t.Fatalf("open session: %v", err)
	}
	if len(term.calls) != 0 || repos != 0 {
		t.Fatalf("terminal reads %v, repo lookups %d, want none", term.calls, repos)
	}
	if err := x.SessionExists(context.Background(), "missing"); !errors.Is(err, acprun.ErrUnknownSession) {
		t.Fatalf("missing session: %v, want unknown session", err)
	}
	x.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "created.json")}
	if err := x.Created.Add("fresh", "/w", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := x.SessionExists(context.Background(), "fresh"); err != nil {
		t.Fatalf("created session without a file yet: %v", err)
	}
}

func TestLocalIndex_SessionTerminalCanType(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Terminal = &fakeTerminalStatus{st: terminput.Status{State: "unknown", CanType: true}}
	d, err := x.Session(context.Background(), "open", 1)
	if err != nil {
		t.Fatal(err)
	}
	if raw := string(must(json.Marshal(d.Terminal))); raw != `{"status":"unknown","canType":true,"working":false}` {
		t.Fatalf("terminal %s, want status, canType and working only", raw)
	}
	x.Terminal = &fakeTerminalStatus{st: terminput.Status{State: "busy", CanType: true, Working: true}}
	if d, _ = x.Session(context.Background(), "open", 1); d.Terminal == nil || !d.Terminal.Working {
		t.Fatalf("terminal %+v, want working", d.Terminal)
	}
}

func TestLocalIndex_TerminalPID(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	if pid, ok := x.TerminalPID("open"); !ok || pid != os.Getpid() {
		t.Fatalf("open: %d %v, want its registry pid", pid, ok)
	}
	if _, ok := x.TerminalPID("idle"); ok {
		t.Fatal("idle session has a terminal pid")
	}
	x.Owned = func(pid int) bool { return pid == os.Getpid() }
	if _, ok := x.TerminalPID("open"); ok {
		t.Fatal("a session aimux's own adapter holds has a terminal pid")
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestTurnOf_ToolDetail(t *testing.T) {
	big := func(n int) string { return strings.Repeat("a", n) }
	turn := turnOf(trace.Turn{Actions: []trace.ToolSpan{
		{Name: "Bash", Snippet: "$ ls", Success: false, Input: "  " + big(5000) + "  ", ErrorMsg: big(3000)},
		{Name: "Edit", Snippet: "a.go", Success: true, OldString: "\tif x {\n", NewString: big(5000)},
		{Name: "Write", Snippet: "b.go", Success: true, Content: "package b\n"},
		{Name: "Read", Snippet: "c.go", Success: true},
		{Name: "Bash", Success: true, Input: big(4096), ErrorMsg: big(2048), OldString: big(4096)},
		{Name: "Bash", Success: true, Input: strings.Repeat("é", 3000)},
	}})
	b, c, w, r, edge, utf := turn.Tools[0], turn.Tools[1], turn.Tools[2], turn.Tools[3], turn.Tools[4], turn.Tools[5]

	if b.Snippet != "$ ls" || b.OK {
		t.Errorf("snippet/ok changed: %+v", b)
	}
	ell := len("…")
	if b.Detail == nil || len(b.Detail.Input) != 4096+ell || !strings.HasSuffix(b.Detail.Input, "…") {
		t.Fatalf("input not capped: %+v", b.Detail)
	}
	if len(b.Detail.Error) != 2048+ell || !strings.HasSuffix(b.Detail.Error, "…") {
		t.Errorf("error len %d, want %d ending in …", len(b.Detail.Error), 2048+ell)
	}
	if c.Detail == nil || c.Detail.Old != "\tif x {\n" {
		t.Errorf("old = %+v, want whitespace kept", c.Detail)
	}
	if len(c.Detail.New) != 4096+ell || !strings.HasSuffix(c.Detail.New, "…") {
		t.Errorf("new len %d, want %d ending in …", len(c.Detail.New), 4096+ell)
	}
	if w.Detail == nil || w.Detail.New != "package b\n" {
		t.Errorf("Write content should map to new: %+v", w.Detail)
	}
	if r.Detail != nil {
		t.Errorf("empty detail should be nil: %+v", r.Detail)
	}
	if js, _ := json.Marshal(r); strings.Contains(string(js), "detail") {
		t.Errorf("json has detail key: %s", js)
	}
	// Exactly at the cap: untouched.
	if edge.Detail.Input != big(4096) || edge.Detail.Error != big(2048) || edge.Detail.Old != big(4096) {
		t.Errorf("values at the cap must pass through unchanged")
	}
	// Multi-byte text is cut on a rune boundary.
	if !utf8.ValidString(utf.Detail.Input) || !strings.HasSuffix(utf.Detail.Input, "…") || len(utf.Detail.Input) > 4096+ell {
		t.Errorf("utf8 cut bad: len %d valid %v", len(utf.Detail.Input), utf8.ValidString(utf.Detail.Input))
	}
}

// An input the snippet already shows is dropped, so the step is not openable
// just to repeat its line.
func TestTurnOf_ToolDetailDropsRepeatedInput(t *testing.T) {
	long := "go test ./internal/billing/... ./internal/invoice/... -run 'TestRounding' -count=1 -race -v"
	turn := turnOf(trace.Turn{Actions: []trace.ToolSpan{
		{Name: "Read", Snippet: "/src/a.go", Success: true, Input: "/src/a.go"},
		{Name: "Grep", Snippet: "/func main/", Success: true, Input: "func main"},
		{Name: "Bash", Snippet: "$ " + long[:57] + "...", Success: true, Input: long},
		{Name: "Read", Snippet: "/src/a.go", Success: false, Input: "/src/a.go", ErrorMsg: "no such file"},
		{Name: "Glob", Snippet: "", Success: true, Input: "**/*.go"},
	}})
	if d := turn.Tools[0].Detail; d != nil {
		t.Errorf("Read path equal to snippet: detail = %+v, want nil", d)
	}
	if d := turn.Tools[1].Detail; d != nil {
		t.Errorf("Grep pattern in slashes: detail = %+v, want nil", d)
	}
	if d := turn.Tools[2].Detail; d == nil || d.Input != long {
		t.Errorf("Bash long command: detail = %+v, want input kept", d)
	}
	if d := turn.Tools[3].Detail; d == nil || d.Input != "" || d.Error != "no such file" {
		t.Errorf("failed Read: detail = %+v, want error only", d)
	}
	if d := turn.Tools[4].Detail; d == nil || d.Input != "**/*.go" {
		t.Errorf("empty snippet: detail = %+v, want input kept", d)
	}
}

func TestCapText(t *testing.T) {
	if got := capText("  a\n", 10); got != "  a\n" {
		t.Errorf("capText trimmed: %q", got)
	}
	if got := capText("", 3); got != "" {
		t.Errorf("empty: %q", got)
	}
	if got := capText("abcd", 4); got != "abcd" {
		t.Errorf("at cap: %q", got)
	}
	if got := capText("abcde", 4); got != "abcd…" {
		t.Errorf("over cap: %q", got)
	}
	if got := capText("aéé", 2); got != "a…" {
		t.Errorf("rune boundary: %q", got)
	}
}

const twoTablesReply = "| Check | Result |\n|---|---|\n| a | yes |\n\nWhat's running there:\n\n| Item | Value |\n|---|---|\n| Nodes | 3 control-plane, 2 workers |\n\nEverything I ran was read-only."

// A reply keeps its blank lines, so a table ends before the next paragraph.
func TestTurnOf_ReplyKeepsBlankLines(t *testing.T) {
	var lines []string
	for _, l := range strings.Split(twoTablesReply, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	got := turnOf(trace.Turn{OutputLines: lines, OutputText: twoTablesReply}).Reply
	if got != twoTablesReply {
		t.Errorf("Reply = %q, want %q", got, twoTablesReply)
	}
}

// Through the real Claude parser: two text blocks of one message, and a later
// message, are separated by a blank line.
func TestTurnOf_ReplyFromParsedTrace(t *testing.T) {
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"check"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"| Check | Result |\n|---|---|\n| a | yes |"},{"type":"text","text":"What's running there:"}]}}
{"type":"assistant","timestamp":"2026-01-01T10:00:06Z","message":{"role":"assistant","content":[{"type":"text","text":"| Item | Value |\n|---|---|\n| Nodes | 3 control-plane, 2 workers |\n\nEverything I ran was read-only."}]}}`
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := (&provider.Claude{}).ParseTrace(path)
	if err != nil || len(turns) != 1 {
		t.Fatalf("parse: %v, %d turns", err, len(turns))
	}
	if got := turnOf(turns[0]).Reply; got != twoTablesReply {
		t.Errorf("Reply = %q, want %q", got, twoTablesReply)
	}
}

// Providers that do not fill OutputText keep today's join of OutputLines.
func TestTurnOf_ReplyFallsBackToOutputLines(t *testing.T) {
	got := turnOf(trace.Turn{OutputLines: []string{"one", "two"}}).Reply
	if got != "one\ntwo" {
		t.Errorf("Reply = %q, want %q", got, "one\ntwo")
	}
}

// A long reply is cut under the cap at a line boundary, and the ellipsis is a
// paragraph of its own so it cannot become a table row.
func TestTurnOf_LongReplyCutsAtALine(t *testing.T) {
	line := "| row | " + strings.Repeat("v", 90) + " |" // 100 bytes
	text := strings.Repeat(line+"\n", 100)              // 10100 bytes
	got := turnOf(trace.Turn{OutputText: text}).Reply
	if !strings.HasSuffix(got, "|\n\n…") {
		t.Fatalf("reply should end at a line then a blank line and …, ends %q", got[len(got)-20:])
	}
	body := strings.TrimSuffix(got, "\n\n…")
	if len(body) > maxTextLen || len(body)%(len(line)+1) != len(line) {
		t.Errorf("body len %d: want whole lines within %d", len(body), maxTextLen)
	}
	// One long line has no boundary: cut on a rune and append … as before.
	long := strings.Repeat("é", maxTextLen)
	got = turnOf(trace.Turn{OutputText: long}).Reply
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || len(got) > maxTextLen+len("…") {
		t.Errorf("long line cut bad: len %d", len(got))
	}
	// At the cap: untouched.
	exact := strings.Repeat("a\n", maxTextLen/2-1) + "ab"
	if got := turnOf(trace.Turn{OutputText: exact}).Reply; got != exact {
		t.Errorf("reply at the cap changed: len %d", len(got))
	}
}

// RegistryStatus lets terminal input refuse a session Claude's registry
// calls busy (terminput.RegistryStatuses).
func TestLocalIndex_RegistryStatus(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	var _ terminput.RegistryStatuses = x
	if got := x.RegistryStatus("open"); got != "" {
		t.Fatalf("no status in the registry: %q", got)
	}
	reg := fmt.Sprintf(`{"pid":%d,"sessionId":"open","status":"busy"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(x.RegistryDir, "1.json"), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := x.RegistryStatus("open"); got != "busy" {
		t.Fatalf("RegistryStatus = %q, want busy", got)
	}
	if got := x.RegistryStatus("idle"); got != "" {
		t.Fatalf("session not open: %q", got)
	}
}
