package remoteapi

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/browserhost"
)

// originOnly is recorded on the session and keyed like pageText: the same
// page asked with and without it is two sessions, so marking a site private
// mid-conversation starts a fresh one; a retry key is scoped to it too.
func TestNewSession_OriginOnlyKeysContinuation(t *testing.T) {
	r := withResearch(newProfileRig(t)).serve()
	ask := func(originOnly bool, key, want string) {
		t.Helper()
		body := `{"profile":"research","url":"https://example.com/a","title":"T","text":"q","instance":"i-1","key":"` + key + `"`
		if originOnly {
			body += `,"originOnly":true`
		}
		resp, out := r.do("POST", "/v1/sessions", body+`}`)
		if resp.StatusCode != 202 || out["id"] != want {
			t.Fatalf("originOnly %v key %q: %d %v, want %s", originOnly, key, resp.StatusCode, out, want)
		}
		r.idle(want)
	}
	ask(false, "", "p-1")
	ask(true, "", "p-2") // the owner marked the site private
	ask(true, "", "p-2")
	ask(false, "", "p-1")
	ask(true, "k", "p-2")
	ask(false, "k", "p-1") // the same key never crosses
	for id, want := range map[string]bool{"p-1": false, "p-2": true} {
		if m, _ := r.store.Get(id); m.OriginOnly != want || m.URL != "https://example.com/a" {
			t.Fatalf("%s = %+v, want OriginOnly %v and the full URL", id, m, want)
		}
	}
}

// Through the real relay: two Gmail messages asked in Research with the
// chip off (origin only) are two sessions, each kept under its own full
// URL, so "Earlier on this page" (history.get) finds each one.
func TestRelayOriginOnly_TwoMessagesKeepTheirOwnPages(t *testing.T) {
	r := withResearch(newProfileRig(t))
	r.api.Session = func(_ context.Context, id string, _ int) (SessionDetail, error) {
		return SessionDetail{Session: Session{ID: id}}, nil
	}
	dir, err := os.MkdirTemp("", "wo") // short enough for a macOS socket path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "wingman.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: r.api.LocalHandler("wingman"), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	p := startRelayPanel(t, sock)
	const a, b = "https://mail.google.com/mail/u/0/#inbox/A", "https://mail.google.com/mail/u/0/#inbox/B"
	for i, u := range []string{a, b, a} {
		e := p.request(browserhost.Request{ID: "r" + string(rune('0'+i)), Type: "ask", Lane: "research", URL: u, Question: "q", Instance: "i-1", OriginOnly: true})
		if e.Type != "done" {
			t.Fatalf("ask %d: %+v, want done", i, e)
		}
	}
	if n := r.started(); n != 2 {
		t.Fatalf("started %d sessions, want 2 (one per message)", n)
	}
	r.mu.Lock()
	starts := append(r.starts[:0:0], r.starts...)
	r.mu.Unlock()
	if starts[0].URL != a || starts[1].URL != b || !starts[0].OriginOnly || !starts[1].OriginOnly {
		t.Fatalf("starts %+v, want each full URL with OriginOnly", starts)
	}
	for _, u := range []string{a, b} {
		e := p.request(browserhost.Request{ID: "h" + u, Type: "history.get", URL: u, Instance: "i-1"})
		if e.Type != "history" || e.Page == nil || e.Page.URL != u {
			t.Fatalf("history.get %s = %+v, want its own page", u, e)
		}
	}
}
