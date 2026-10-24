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
	r := &Router{Lookup: ClaudeLookup(projects, registry), Manager: NewManager(ManagerOptions{Start: fs.start})}
	defer r.Manager.Close()
	ctx := context.Background()
	if _, err := r.Send(ctx, "idle-1", "one", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	// The adapter's own Claude process now holds the session in the registry.
	body := fmt.Sprintf(`{"pid":%d,"sessionId":"idle-1","cwd":"/w/app"}`, os.Getppid())
	if err := os.WriteFile(filepath.Join(registry, fmt.Sprintf("%d.json", os.Getppid())), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Send(ctx, "idle-1", "two", func(Event) {}); err != nil {
		t.Fatalf("second message to a session aimux runs: %v", err)
	}
}
