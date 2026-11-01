package terminput

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/harness"
)

// stubReader is a Composer for another harness: its own process name, a
// fixed screen, its own text rule and room keys.
type stubReader struct {
	proc   string
	scr    harness.Screen
	refuse string // a prefix it refuses
	room   harness.RoomKeys
}

func (r stubReader) Read(string) harness.Screen { return r.scr }
func (r stubReader) BoxShows(string, string) harness.Match {
	return harness.Match{Words: true, Shown: true, Typed: true}
}
func (r stubReader) IsAgentProcess(comm string) bool { return comm == r.proc }
func (r stubReader) ClearKeys(int) []harness.Key     { return []harness.Key{harness.KeyClear} }
func (r stubReader) RoomKeys() harness.RoomKeys      { return r.room }
func (r stubReader) RefuseText(text string, _ bool) bool {
	return r.refuse != "" && strings.HasPrefix(text, r.refuse)
}

// fixture reads a Claude Code screen from the parser's fixtures.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "harness", "claude", "testdata", "screens", name)) //nolint:gosec // fixed test fixture names
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func withReader(e *env, r Composer) {
	svc := NewServiceFor(config.TerminalInputConfig{Enabled: true}, fakeSessions{"s1": 42}, e.sys, r, e.log, e.drv)
	svc.goos, svc.now, svc.sleep = e.svc.goos, e.svc.now, e.svc.sleep
	e.svc = svc
}

func TestServiceAsksItsReaderForTheProcess(t *testing.T) {
	e := newEnv(t)
	e.sys.cmd = "codex"
	withReader(e, stubReader{proc: "codex", scr: harness.Screen{InputEmpty: true}})
	if err := e.svc.Send(context.Background(), "s1", "hi", ""); err != nil {
		t.Fatalf("Send with the reader's own process: %v", err)
	}
	if len(e.drv.sent) != 1 || e.drv.sent[0] != "hi" {
		t.Fatalf("sent = %q", e.drv.sent)
	}
	e = newEnv(t) // claude is not this reader's process
	withReader(e, stubReader{proc: "codex", scr: harness.Screen{InputEmpty: true}})
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "not_claude")
}

func TestServiceAsksItsReaderForTheScreen(t *testing.T) {
	e := newEnv(t) // the screen is Claude Code's idle box; the reader says working
	withReader(e, stubReader{proc: "claude", scr: harness.Screen{InputEmpty: true, Working: true, WorkingSeconds: 7}})
	wantCode(t, e.svc.Send(context.Background(), "s1", "hi", ""), "busy")
	if st := e.svc.Status(context.Background(), "s1", "busy"); !st.Working || st.WorkingSeconds != 7 {
		t.Fatalf("Status = %+v", st)
	}
}

func TestNewServiceForNilComposerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewServiceFor(nil composer) did not panic")
		}
	}()
	NewServiceFor(config.TerminalInputConfig{Enabled: true}, fakeSessions{}, &fakeSystem{}, nil, nil)
}

func TestServiceAsksItsComposerForTheTextRule(t *testing.T) {
	e := newEnv(t)
	withReader(e, stubReader{proc: "claude", scr: harness.Screen{InputEmpty: true}, refuse: "%"})
	wantCode(t, e.svc.Send(context.Background(), "s1", "%x", ""), "bad_text")
	if err := e.svc.Send(context.Background(), "s1", "!x", ""); err != nil {
		t.Fatalf("a leading ! is Claude Code's rule, not this harness's: %v", err)
	}
	wantCode(t, e.svc.Send(context.Background(), "s1", "a\x1bb", ""), "bad_text") // controls are the pty's rule
}

// A harness without a probe key goes straight to clearing a draft.
func TestMakeRoomWithoutProbe(t *testing.T) {
	e := newEnv(t)
	withReader(e, stubReader{proc: "claude", scr: harness.Screen{BoxHasText: true, BoxRows: 1}})
	err := e.svc.makeRoom(context.Background(), e.drv, Pane{}, "draft", false, func() error { return nil })
	wantCode(t, err, "box_not_cleared")
	for _, k := range e.drv.keys {
		if k != KeyClear {
			t.Fatalf("keys = %v, want only clears", e.drv.keys)
		}
	}
	if len(e.drv.keys) != clearRounds {
		t.Fatalf("keys = %v, want %d clears", e.drv.keys, clearRounds)
	}
}
