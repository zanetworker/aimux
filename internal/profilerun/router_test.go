package profilerun

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/acprun"
)

type fakeEngine struct {
	sends, cancels []string
}

func (f *fakeEngine) New(context.Context, Meta, string) (string, error) { return "", nil }
func (f *fakeEngine) Send(_ context.Context, session, text string, onEvent func(acprun.Event)) (string, error) {
	f.sends = append(f.sends, session+":"+text)
	onEvent(acprun.Event{Kind: acprun.KindUpdate})
	return "end_turn", nil
}
func (f *fakeEngine) Cancel(session string) error {
	f.cancels = append(f.cancels, session)
	return nil
}

type fallback struct{ sends, checks, cancels []string }

func (f *fallback) send(_ context.Context, session, text string, _ func(acprun.Event)) (string, error) {
	f.sends = append(f.sends, session+":"+text)
	return "fallback_stop", nil
}
func (f *fallback) check(session string) error {
	f.checks = append(f.checks, session)
	return errors.New("fallback check")
}
func (f *fallback) cancel(session string) error {
	f.cancels = append(f.cancels, session)
	return errors.New("fallback cancel")
}

func newRouter(t *testing.T) (*Router, *fakeEngine, *fakeEngine, *fallback) {
	t.Helper()
	page, sandbox, fb := &fakeEngine{}, &fakeEngine{}, &fallback{}
	r := &Router{
		Store:    newStore(t),
		Engines:  map[string]Engine{"acp": page, "sandbox": sandbox},
		Fallback: fb.send, FallbackCheck: fb.check, FallbackCancel: fb.cancel,
		Runs: func(p string) (string, error) {
			switch p {
			case "page":
				return "acp", nil
			case "work-readonly":
				return "sandbox", nil
			case "coding":
				return "host", nil
			}
			return "", errors.New("unknown profile")
		},
	}
	return r, page, sandbox, fb
}

// Today's phone and web sessions have no Meta: they must behave exactly as
// before, through acprun's router.
func TestRouter_PlainSessionGoesToFallback(t *testing.T) {
	r, page, sandbox, fb := newRouter(t)
	stop, err := r.Send(context.Background(), "plain", "hi", func(acprun.Event) {})
	if err != nil || stop != "fallback_stop" {
		t.Fatalf("Send = %q %v", stop, err)
	}
	if err := r.Check("plain"); err == nil || err.Error() != "fallback check" {
		t.Fatalf("Check = %v, want the fallback's answer", err)
	}
	if err := r.Cancel("plain"); err == nil || err.Error() != "fallback cancel" {
		t.Fatalf("Cancel = %v, want the fallback's answer", err)
	}
	if len(fb.sends) != 1 || fb.sends[0] != "plain:hi" || len(fb.checks) != 1 || len(fb.cancels) != 1 {
		t.Fatalf("fallback saw %+v", fb)
	}
	if len(page.sends)+len(sandbox.sends)+len(page.cancels)+len(sandbox.cancels) != 0 {
		t.Fatal("an engine saw a plain session")
	}
}

func TestRouter_ProfileSessionGoesToEngine(t *testing.T) {
	r, page, sandbox, fb := newRouter(t)
	for id, p := range map[string]string{"p1": "page", "w1": "work-readonly"} {
		if err := r.Store.Put(id, Meta{Profile: p, Client: "cli"}); err != nil {
			t.Fatal(err)
		}
	}
	var events int
	stop, err := r.Send(context.Background(), "p1", "q", func(acprun.Event) { events++ })
	if err != nil || stop != "end_turn" || events != 1 {
		t.Fatalf("Send = %q %v, %d events", stop, err, events)
	}
	if _, err := r.Send(context.Background(), "w1", "q2", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	if err := r.Check("w1"); err != nil {
		t.Fatalf("Check = %v", err)
	}
	if err := r.Cancel("w1"); err != nil {
		t.Fatalf("Cancel = %v", err)
	}
	if len(page.sends) != 1 || page.sends[0] != "p1:q" {
		t.Fatalf("page engine saw %v", page.sends)
	}
	if len(sandbox.sends) != 1 || sandbox.sends[0] != "w1:q2" || len(sandbox.cancels) != 1 {
		t.Fatalf("sandbox engine saw %+v", sandbox)
	}
	if len(fb.sends)+len(fb.checks)+len(fb.cancels) != 0 {
		t.Fatalf("fallback saw a profile session: %+v", fb)
	}
}

// A profile session never falls back to the unlocked host path: a profile
// with no engine here, an unknown profile, or an unreadable store is an error.
func TestRouter_ProfileSessionWithoutEngineFailsClosed(t *testing.T) {
	r, _, _, fb := newRouter(t)
	_ = r.Store.Put("h1", Meta{Profile: "coding"})
	_ = r.Store.Put("x1", Meta{Profile: "gone"})
	for _, id := range []string{"h1", "x1"} {
		if _, err := r.Send(context.Background(), id, "q", func(acprun.Event) {}); err == nil {
			t.Fatalf("%s: Send succeeded", id)
		}
		if err := r.Check(id); err == nil {
			t.Fatalf("%s: Check succeeded", id)
		}
		if err := r.Cancel(id); err == nil {
			t.Fatalf("%s: Cancel succeeded", id)
		}
	}
	if err := os.WriteFile(r.Store.Path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Send(context.Background(), "any", "q", func(acprun.Event) {}); err == nil {
		t.Fatal("Send with an unreadable store succeeded")
	}
	if err := r.Check("any"); err == nil {
		t.Fatal("Check with an unreadable store succeeded")
	}
	if len(fb.sends)+len(fb.checks)+len(fb.cancels) != 0 {
		t.Fatalf("fallback reached: %+v", fb)
	}
}

func corruptStore(t *testing.T, r *Router, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(r.Store.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.Store.Path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A corrupt store fails every session closed, with an error that says which
// file and how to recover, logged once per distinct failure.
func TestRouter_CorruptStoreErrorNamesFile(t *testing.T) {
	r, _, _, fb := newRouter(t)
	var logs syncBuffer
	r.Log = slog.New(slog.NewTextHandler(&logs, nil))
	corruptStore(t, r, "not json")
	calls := []func() error{
		func() error { _, err := r.Send(context.Background(), "plain", "q", func(acprun.Event) {}); return err },
		func() error { return r.Check("plain") },
		func() error { return r.Cancel("plain") },
	}
	for i, call := range calls {
		err := call()
		if err == nil || !strings.Contains(err.Error(), r.Store.Path) || !strings.Contains(err.Error(), "move it aside") {
			t.Fatalf("call %d: error %v, want the file path and how to recover", i, err)
		}
	}
	if n := strings.Count(logs.String(), "store unreadable"); n != 1 {
		t.Fatalf("logged %d times, want once:\n%s", n, logs.String())
	}
	corruptStore(t, r, "{also not json")
	_ = r.Check("plain")
	_ = r.Check("plain")
	if n := strings.Count(logs.String(), "store unreadable"); n != 2 {
		t.Fatalf("a new failure logged %d lines in all, want 2:\n%s", n, logs.String())
	}
	if len(fb.sends)+len(fb.checks)+len(fb.cancels) != 0 {
		t.Fatalf("fallback reached: %+v", fb)
	}
}

func TestRouter_HealthReportsCorruptStore(t *testing.T) {
	r, _, _, _ := newRouter(t)
	if err := r.Health(); err != nil {
		t.Fatalf("no file yet: %v", err)
	}
	_ = r.Store.Put("s", Meta{Profile: "page"})
	if err := r.Health(); err != nil {
		t.Fatalf("good file: %v", err)
	}
	corruptStore(t, r, "not json")
	if err := r.Health(); err == nil || !strings.Contains(err.Error(), r.Store.Path) {
		t.Fatalf("corrupt file: %v", err)
	}
	if err := os.Rename(r.Store.Path, r.Store.Path+".corrupt"); err != nil {
		t.Fatal(err)
	}
	if err := r.Health(); err != nil {
		t.Fatalf("after moving it aside: %v", err)
	}
}
