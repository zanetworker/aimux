package iterm2

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRun records each call's args and answers with the next reply.
type fakeRun struct {
	calls   [][]string
	scripts []string // the script file's content during each call
	replies []string
}

func (f *fakeRun) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) > 0 {
		b, _ := os.ReadFile(args[0])
		f.scripts = append(f.scripts, string(b))
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return []byte(r + "\n"), nil
}

func newOpener(replies ...string) (*Opener, *fakeRun) {
	f := &fakeRun{replies: replies}
	return &Opener{Run: f.run}, f
}

func TestShellLine_QuotesEverything(t *testing.T) {
	got := ShellLine([]string{"/usr/local/bin/claude", "--resume", "3f2a-…"}, "/tmp/a b/it's $HOME")
	want := `cd '/tmp/a b/it'"'"'s $HOME' && '/usr/local/bin/claude' '--resume' '3f2a-…'`
	if got != want {
		t.Fatalf("ShellLine =\n%s\nwant\n%s", got, want)
	}
	got = ShellLine([]string{"/bin/x"}, "/tmp/a\nb")
	if want := "cd '/tmp/a\nb' && '/bin/x'"; got != want {
		t.Fatalf("newline: got %q want %q", got, want)
	}
	if got := ShellLine([]string{"/bin/x", ""}, "/"); got != `cd '/' && '/bin/x' ''` {
		t.Fatalf("empty arg: got %q", got)
	}
}

func TestOpener_PassesDataAsArgs(t *testing.T) {
	o, f := newOpener(`{"window":5,"pane":1}`)
	title := `say "hi" 'there' $(rm -rf ~)`
	line := ShellLine([]string{"/bin/claude", "--resume", "abc"}, "/tmp/w")
	if err := o.OpenTab(context.Background(), title, []string{"/bin/claude", "--resume", "abc"}, "/tmp/w"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	args := f.calls[0]
	if len(args) != 4 {
		t.Fatalf("args = %q, want script + 3", args)
	}
	if f.scripts[0] != opentabJS {
		t.Fatalf("script file does not hold the embedded opentab.js")
	}
	if args[1] != strconv.Itoa(MaxPanes) || args[2] != title || args[3] != line {
		t.Fatalf("args = %q", args[1:])
	}
	if _, err := os.Stat(args[0]); !os.IsNotExist(err) {
		t.Fatalf("script temp file left behind: %v", err)
	}
}

func TestOpener_MaxPanesIsTen(t *testing.T) {
	if MaxPanes != 10 {
		t.Fatalf("MaxPanes = %d, want 10", MaxPanes)
	}
}

// Every call passes the same arguments: the script finds aimux's window by
// its panes' user.aimux variable, so nothing is remembered between calls.
func TestOpener_NothingRememberedBetweenCalls(t *testing.T) {
	o, f := newOpener(`{"window":42,"pane":1}`, `{"window":42,"pane":2}`)
	for i := 0; i < 2; i++ {
		if err := o.OpenTab(context.Background(), "t", []string{"/bin/x"}, "/"); err != nil {
			t.Fatal(err)
		}
	}
	if a, b := f.calls[0][1:], f.calls[1][1:]; a[0] != b[0] || a[1] != b[1] || a[2] != b[2] {
		t.Fatalf("args differ between calls: %q vs %q", a, b)
	}
}

// No state file: an open writes nothing under HOME, and an old
// ~/.aimux/iterm-aimux-window.json is neither read nor changed.
func TestOpener_NoStateFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	o, f := newOpener(`{"window":3,"pane":1}`, `{"window":3,"pane":2}`)
	if err := o.OpenTab(context.Background(), "t", []string{"/bin/x"}, "/"); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Fatalf("HOME has %d entries after an open, want none", len(ents))
	}
	old := home + "/.aimux/iterm-aimux-window.json"
	if err := os.MkdirAll(home+"/.aimux", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte(`{"window":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := o.OpenTab(context.Background(), "t", []string{"/bin/x"}, "/"); err != nil {
		t.Fatal(err)
	}
	if f.calls[1][1] != strconv.Itoa(MaxPanes) {
		t.Fatalf("first arg = %q, want maxPanes (the old window id must be ignored)", f.calls[1][1])
	}
	b, _ := os.ReadFile(old) // #nosec G304 -- test temp dir
	if string(b) != `{"window":99}` {
		t.Fatalf("old state file changed: %q", b)
	}
}

// Two opens at once never run the script at the same time, so both land in
// the window the first one chose.
func TestOpener_SerializesOpens(t *testing.T) {
	var inFlight, overlap, calls atomic.Int32
	o := &Opener{Run: func(context.Context, ...string) ([]byte, error) {
		if inFlight.Add(1) > 1 {
			overlap.Store(1)
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		n := calls.Add(1)
		return []byte(`{"window":7,"pane":` + strconv.Itoa(int(n)) + `}`), nil
	}}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = o.OpenTab(context.Background(), "t", []string{"/bin/x"}, "/")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	if overlap.Load() != 0 {
		t.Fatal("the script ran for two opens at once")
	}
	if calls.Load() != 4 {
		t.Fatalf("calls = %d, want 4", calls.Load())
	}
}

func TestOpener_NotAuthorized(t *testing.T) {
	o, _ := newOpener(`{"error":"not_authorized"}`)
	if err := o.OpenTab(context.Background(), "t", []string{"/bin/x"}, "/"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("err = %v, want ErrNotAuthorized", err)
	}
}

func TestOpener_BadReplies(t *testing.T) {
	for _, reply := range []string{
		`not json`, `{"error":"boom"}`, `{"window":5,"error":"boom"}`,
		`{"window":0,"pane":1}`, `{"window":5}`, `{"window":5,"pane":0}`,
		`{"window":5,"pane":11}`, `{}`,
	} {
		o, _ := newOpener(reply)
		err := o.OpenTab(context.Background(), "t", []string{"/bin/x"}, "/")
		if err == nil || errors.Is(err, ErrNotAuthorized) {
			t.Fatalf("reply %q: err = %v, want a plain error", reply, err)
		}
	}
}

func TestOpener_PaneBounds(t *testing.T) {
	for _, reply := range []string{`{"window":5,"pane":1}`, `{"window":5,"pane":10}`} {
		o, _ := newOpener(reply)
		if err := o.OpenTab(context.Background(), "t", []string{"/bin/x"}, "/"); err != nil {
			t.Fatalf("reply %q: %v", reply, err)
		}
	}
}

func TestOpener_RunErrorIsReturned(t *testing.T) {
	boom := errors.New("exec failed")
	o := &Opener{Run: func(context.Context, ...string) ([]byte, error) { return nil, boom }}
	if err := o.OpenTab(context.Background(), "t", []string{"/bin/x"}, "/"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestOpener_EmptyArgvRefused(t *testing.T) {
	o, f := newOpener()
	if err := o.OpenTab(context.Background(), "t", nil, "/"); err == nil {
		t.Fatal("want an error for empty argv")
	}
	if len(f.calls) != 0 {
		t.Fatal("script ran for empty argv")
	}
}
