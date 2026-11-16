package remoteapi

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/browserhost"
	"github.com/zanetworker/aimux/internal/profile"
)

// withResearch makes research available in the rig, as the real manifest is.
func withResearch(r *profileRig) *profileRig {
	r.api.Profiles = func() (map[string]profile.Manifest, error) {
		all := testProfiles()
		all["research"] = profile.Manifest{Name: "research", Harness: "claude-code", Runs: "sandbox", Available: true,
			Network: profile.NetworkSpec{Default: "deny"}}
		return all, nil
	}
	return r
}

// Page text is bound on a session's first turn only, so a question asked
// without page text must never continue a session that has it (the page's
// text would reach the model anyway), and one asked with it must not
// continue a session without it (the text would be dropped). Same-mode
// follow-ups still continue one session.
func TestNewSession_PageTextKeysContinuation(t *testing.T) {
	r := withResearch(newProfileRig(t)).serve()
	ask := func(url, pageText, want string) {
		t.Helper()
		body := `{"profile":"research","url":"` + url + `","title":"T","text":"q","instance":"i-1"`
		if pageText != "" {
			body += `,"pageText":"` + pageText + `"`
		}
		resp, out := r.do("POST", "/v1/sessions", body+`}`)
		if resp.StatusCode != 202 || out["id"] != want {
			t.Fatalf("%s pageText %q: %d %v, want %s", url, pageText, resp.StatusCode, out, want)
		}
		r.idle(want)
	}
	const a, b = "https://mail.google.com/mail/u/0/#inbox/A", "https://example.com/b"
	// With, then without: two sessions; the second has no page text.
	ask(a, "private mail", "p-1")
	ask(a, "", "p-2")
	ask(a, "", "p-2")             // a follow-up without page text continues it
	ask(a, "private mail", "p-1") // and one with page text the first
	// Without, then with: two sessions; the second binds the page text.
	ask(b, "", "p-3")
	ask(b, "public page", "p-4")
	ask(b, "public page", "p-4")
	r.mu.Lock()
	got := append([]string(nil), r.pages...)
	r.mu.Unlock()
	if len(got) != 4 || got[0] != "private mail" || got[1] != "" || got[2] != "" || got[3] != "public page" {
		t.Fatalf("engine pages %q, want [private mail, '', '', public page]", got)
	}
	for id, want := range map[string]bool{"p-1": true, "p-2": false, "p-3": false, "p-4": true} {
		if m, _ := r.store.Get(id); m.WithPage != want {
			t.Fatalf("%s WithPage = %v, want %v", id, m.WithPage, want)
		}
	}
	if n := r.f.sends.Load(); n != 7 {
		t.Fatalf("%d turns, want 7", n)
	}
}

// A retry key is scoped to page text too: the same key without page text
// never lands in the session a request with page text made.
func TestNewSession_RetryKeyScopedToPageText(t *testing.T) {
	r := withResearch(newProfileRig(t)).serve()
	for _, c := range []struct{ page, want string }{{"x", "p-1"}, {"", "p-2"}, {"x", "p-1"}} {
		body := `{"profile":"research","url":"https://example.com/a","text":"q","key":"k"`
		if c.page != "" {
			body += `,"pageText":"` + c.page + `"`
		}
		resp, out := r.do("POST", "/v1/sessions", body+`}`)
		if resp.StatusCode != 202 || out["id"] != c.want {
			t.Fatalf("page %q: %d %v, want %s", c.page, resp.StatusCode, out, c.want)
		}
		r.idle(c.want)
	}
}

// Through the real relay, as the panel sends them: Research with "Use this
// page" on, then off, on the same page are two conversations, and the
// second never gets the page text; Page asks (which always carry the page)
// keep one conversation.
func TestRelayPageText_ResearchChipSeparatesConversations(t *testing.T) {
	r := withResearch(newProfileRig(t))
	dir, err := os.MkdirTemp("", "wp") // short enough for a macOS socket path
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
	const url = "https://mail.google.com/mail/u/0/#inbox/A"
	ask := func(id, lane, page string) {
		t.Helper()
		e := p.request(browserhost.Request{ID: id, Type: "ask", Lane: lane, URL: url, Title: "Mail", Page: page, Question: "q", Instance: "i-1"})
		if e.Type != "done" {
			t.Fatalf("%s: %+v, want done", id, e)
		}
	}
	ask("r1", "research", "private mail") // chip on
	ask("r2", "research", "")             // chip off
	ask("p1", "page", "private mail")
	ask("p2", "page", "private mail")
	if n := r.started(); n != 3 {
		t.Fatalf("started %d sessions, want 3 (research with, research without, page)", n)
	}
	r.mu.Lock()
	got := append([]string(nil), r.pages...)
	r.mu.Unlock()
	if got[0] != "private mail" || got[1] != "" || got[2] != "private mail" {
		t.Fatalf("engine pages %q", got)
	}
}
