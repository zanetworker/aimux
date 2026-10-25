package acprun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// claudeFixture lays out ~/.claude/projects and ~/.claude/sessions for one
// idle session ("idle-1"), one open in a terminal ("open-1", held by this
// test process) and one whose terminal has exited ("gone-1").
func claudeFixture(t *testing.T) (projects, registry string) {
	t.Helper()
	root := t.TempDir()
	projects = filepath.Join(root, "projects")
	registry = filepath.Join(root, "sessions")
	for _, d := range []string{filepath.Join(projects, "-w-app"), registry} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"idle-1", "open-1", "gone-1"} {
		body := `{"type":"summary","summary":"x"}` + "\n" +
			`{"type":"user","sessionId":"` + id + `","cwd":"/w/app","message":{"role":"user","content":"hi"}}` + "\n"
		if err := os.WriteFile(filepath.Join(projects, "-w-app", id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write := func(pid int, id string) {
		body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/w/app","status":"idle"}`, pid, id)
		if err := os.WriteFile(filepath.Join(registry, fmt.Sprintf("%d.json", pid)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(os.Getpid(), "open-1")
	write(999999, "gone-1") // no such process
	return projects, registry
}

func TestClaudeLookup(t *testing.T) {
	projects, registry := claudeFixture(t)
	lookup := ClaudeLookup(projects, registry)
	tests := []struct {
		id      string
		open    bool
		wantErr error
	}{
		{"idle-1", false, nil},
		{"open-1", true, nil},
		{"gone-1", false, nil},
		{"missing", false, ErrUnknownSession},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			info, err := lookup(tt.id)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if info.Cwd != "/w/app" || info.Provider != "claude" || info.OpenInTerminal != tt.open {
				t.Fatalf("info = %+v, want cwd /w/app, provider claude, open %v", info, tt.open)
			}
		})
	}
}

func TestRouter(t *testing.T) {
	projects, registry := claudeFixture(t)
	var gotCwd string
	fs := &fakeRunStarter{}
	start := func(ctx context.Context, session, cwd string) (Runner, error) {
		gotCwd = cwd
		return fs.start(ctx, session, cwd)
	}
	r := &Router{
		Lookup:  ClaudeLookup(projects, registry),
		Manager: NewManager(ManagerOptions{Start: start}),
	}
	defer r.Manager.Close()
	noop := func(Event) {}
	ctx := context.Background()

	if _, err := r.Send(ctx, "missing", "hi", noop); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session: %v", err)
	}
	if _, err := r.Send(ctx, "open-1", "hi", noop); !errors.Is(err, ErrOpenInTerminal) {
		t.Fatalf("session open in a terminal: %v, want ErrOpenInTerminal", err)
	}
	if fs.count() != 0 {
		t.Fatal("an adapter was started for a session open in a terminal")
	}
	if _, err := r.Send(ctx, "idle-1", "hi", noop); err != nil {
		t.Fatalf("idle session: %v", err)
	}
	if gotCwd != "/w/app" {
		t.Fatalf("adapter started in %q, want the session's own directory", gotCwd)
	}
}

func TestRouter_SessionAimuxAlreadyRunsIsNotOpenInTerminal(t *testing.T) {
	projects, registry := claudeFixture(t)
	fs := &fakeRunStarter{}
	// The registry entry below is the adapter's own Claude process: a child of
	// the adapter (pid 4242) that aimux started.
	parents := map[int]int{os.Getppid(): 4242}
	r := &Router{Lookup: ClaudeLookup(projects, registry), Manager: NewManager(ManagerOptions{Start: fs.start, ParentPID: func(p int) int { return parents[p] }})}
	defer r.Manager.Close()
	ctx := context.Background()
	if _, err := r.Send(ctx, "idle-1", "one", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	writeHolder(t, registry, os.Getppid(), "idle-1")
	if _, err := r.Send(ctx, "idle-1", "two", func(Event) {}); err != nil {
		t.Fatalf("second message to a session aimux runs: %v", err)
	}
}

// The user opens a session in a terminal while aimux still keeps an idle
// adapter for it: aimux must stop sending, or two agents drive one session.
func TestRouter_TerminalOpenedWhileAimuxHoldsAnIdleRun(t *testing.T) {
	projects, registry := claudeFixture(t)
	fs := &fakeRunStarter{}
	r := &Router{Lookup: ClaudeLookup(projects, registry), Manager: NewManager(ManagerOptions{Start: fs.start, ParentPID: func(int) int { return 1 }})}
	defer r.Manager.Close()
	ctx := context.Background()
	if _, err := r.Send(ctx, "idle-1", "one", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	writeHolder(t, registry, os.Getppid(), "idle-1") // an unrelated live process: the user's terminal
	if _, err := r.Send(ctx, "idle-1", "two", func(Event) {}); !errors.Is(err, ErrOpenInTerminal) {
		t.Fatalf("got %v, want ErrOpenInTerminal once a terminal holds the session", err)
	}
}

func writeHolder(t *testing.T, registry string, pid int, session string) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/w/app"}`, pid, session)
	if err := os.WriteFile(filepath.Join(registry, fmt.Sprintf("%d.json", pid)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRouter_Check(t *testing.T) {
	projects, registry := claudeFixture(t)
	r := &Router{Lookup: ClaudeLookup(projects, registry), Manager: NewManager(ManagerOptions{Start: (&fakeRunStarter{}).start})}
	defer r.Manager.Close()
	if err := r.Check("idle-1"); err != nil {
		t.Fatalf("idle session: %v", err)
	}
	if err := r.Check("open-1"); !errors.Is(err, ErrOpenInTerminal) {
		t.Fatalf("open session: %v", err)
	}
	if err := r.Check("missing"); !errors.Is(err, ErrUnknownSession) {
		t.Fatalf("unknown session: %v", err)
	}
}

// A stale registry entry whose PID now belongs to an unrelated process must
// not make the session look open in a terminal.
func TestClaudeLookup_ReusedPIDIsNotAHolder(t *testing.T) {
	projects, registry := claudeFixture(t)
	body := fmt.Sprintf(`{"pid":%d,"sessionId":"idle-1","procStart":"Thu Jan  1 00:00:00 2026"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(registry, "stale.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ClaudeLookup(projects, registry)("idle-1")
	if err != nil {
		t.Fatal(err)
	}
	if info.OpenInTerminal || len(info.Holders) != 0 {
		t.Fatalf("info %+v: a reused PID was treated as the session's terminal", info)
	}
}

func TestClaudeLookup_NoRegistryRefuses(t *testing.T) {
	projects, _ := claudeFixture(t)
	missing := filepath.Join(t.TempDir(), "no-sessions-dir")
	r := &Router{Lookup: ClaudeLookup(projects, missing), Manager: NewManager(ManagerOptions{Start: (&fakeRunStarter{}).start})}
	defer r.Manager.Close()
	if err := r.Check("idle-1"); !errors.Is(err, ErrOwnershipUnknown) {
		t.Fatalf("got %v, want ErrOwnershipUnknown when Claude's registry is unavailable", err)
	}
}

// A Claude process started with the session id on its command line holds the
// session even when the registry has no entry for it.
func TestClaudeLookup_CommandLineHolder(t *testing.T) {
	projects, registry := claudeFixture(t)
	prev := listProcesses
	defer func() { listProcesses = prev }()
	listProcesses = func() ([]processArgs, error) {
		return []processArgs{
			{PID: os.Getppid(), Args: "/usr/local/bin/claude --dangerously-skip-permissions --resume idle-1"}, // no registry entry
			{PID: 1, Args: "vim notes-about-idle-1.md"},                                                       // not claude
		}, nil
	}
	info, err := ClaudeLookup(projects, registry)("idle-1")
	if err != nil {
		t.Fatal(err)
	}
	if !info.OpenInTerminal || len(info.Holders) != 1 || info.Holders[0] != os.Getppid() {
		t.Fatalf("info %+v, want the claude --resume process as the only holder", info)
	}
}

// After /clear or a fork, a process started as "claude --resume old" holds
// a new session; its live registry entry says which, and the old id on its
// command line must not keep the old session locked.
func TestClaudeLookup_RegistryBeatsAStaleCommandLine(t *testing.T) {
	projects, registry := claudeFixture(t) // this process's entry: open-1
	prev := listProcesses
	defer func() { listProcesses = prev }()
	listProcesses = func() ([]processArgs, error) {
		return []processArgs{{PID: os.Getpid(), Args: "claude --resume idle-1"}}, nil
	}
	info, err := ClaudeLookup(projects, registry)("idle-1")
	if err != nil {
		t.Fatal(err)
	}
	if info.OpenInTerminal {
		t.Fatalf("info %+v: the process now holds open-1, not idle-1", info)
	}
}

func TestClaudeLookup_UndecodableRegistryEntryRefuses(t *testing.T) {
	projects, registry := claudeFixture(t)
	if err := os.WriteFile(filepath.Join(registry, "777.json"), []byte(`{"pid":777,"sessi`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaudeLookup(projects, registry)("idle-1"); !errors.Is(err, ErrOwnershipUnknown) {
		t.Fatalf("got %v, want ErrOwnershipUnknown: the broken entry may be the terminal holding this session", err)
	}
}
