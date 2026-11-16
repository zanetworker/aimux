package remoteapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/browserhost"
	"github.com/zanetworker/aimux/internal/profilerun"
)

// relayPanel is one browser's native host: a real Relay on the wingman
// socket, driven through native messaging frames.
type relayPanel struct {
	t   *testing.T
	in  *io.PipeWriter
	out *io.PipeReader
}

func startRelayPanel(t *testing.T, sock string) *relayPanel {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { _ = inW.Close(); cancel() })
	go func() {
		_ = (&browserhost.Relay{Socket: sock}).Serve(ctx, inR, outW)
		_ = outW.Close()
	}()
	return &relayPanel{t: t, in: inW, out: outR}
}

// request sends req and returns its first done, error or history event.
func (p *relayPanel) request(req browserhost.Request) browserhost.Event {
	p.t.Helper()
	b, _ := json.Marshal(req)
	if err := browserhost.WriteMessage(p.in, b); err != nil {
		p.t.Fatal(err)
	}
	for {
		msg, err := browserhost.ReadMessage(p.out)
		if err != nil {
			p.t.Fatalf("relay closed before %s ended: %v", req.ID, err)
		}
		var e browserhost.Event
		if err := json.Unmarshal(msg, &e); err != nil {
			p.t.Fatal(err)
		}
		if e.ID == req.ID && (e.Type == "done" || e.Type == "error" || e.Type == "history") {
			return e
		}
	}
}

// Two browsers (two relays with their own instances) asking on the same
// page get two sessions, each continuing its own; each one's history lists
// its own page plus the legacy session from before instances.
func TestRelayInstances_TwoBrowsersKeepSeparateSessions(t *testing.T) {
	r := newProfileRig(t)
	const pageURL = "https://a.example/x"
	r.put("legacy", profilerun.Meta{Profile: "page", Client: "wingman", URL: "https://old.example/", Title: "Old", Created: time.Now().Add(-time.Hour)})

	dir, err := os.MkdirTemp("", "wi") // short enough for a macOS socket path
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

	chrome, beta := startRelayPanel(t, sock), startRelayPanel(t, sock)
	ask := func(p *relayPanel, id, instance string) {
		t.Helper()
		e := p.request(browserhost.Request{ID: id, Type: "ask", Lane: "page", URL: pageURL, Title: "X", Question: "q " + id, Instance: instance})
		if e.Type != "done" {
			t.Fatalf("%s: %+v, want done", id, e)
		}
	}
	ask(chrome, "c1", "chrome-1")
	ask(beta, "b1", "beta-2")
	ask(chrome, "c2", "chrome-1")
	ask(beta, "b2", "beta-2")

	if n := r.started(); n != 2 {
		t.Fatalf("started %d sessions, want 2 (one per browser)", n)
	}
	if n := r.f.sends.Load(); n != 4 {
		t.Fatalf("%d turns sent, want 4", n)
	}
	got := map[string]string{}
	for id, m := range r.store.List("wingman") {
		if m.URL == pageURL {
			got[m.Instance] = id
		}
	}
	if len(got) != 2 || got["chrome-1"] == "" || got["beta-2"] == "" || got["chrome-1"] == got["beta-2"] {
		t.Fatalf("sessions by instance %v, want one each", got)
	}

	for _, c := range []struct {
		p        *relayPanel
		instance string
	}{{chrome, "chrome-1"}, {beta, "beta-2"}} {
		e := c.p.request(browserhost.Request{ID: "h-" + c.instance, Type: "history.list", Instance: c.instance})
		if len(e.Pages) != 2 || e.Pages[0].URL != pageURL || e.Pages[1].URL != "https://old.example/" {
			t.Fatalf("%s history = %+v, want its page and the legacy one", c.instance, e.Pages)
		}
		if e.Pages[0].PageSessionID != got[c.instance] {
			t.Fatalf("%s history shows session %s, want its own %s", c.instance, e.Pages[0].PageSessionID, got[c.instance])
		}
	}
}
