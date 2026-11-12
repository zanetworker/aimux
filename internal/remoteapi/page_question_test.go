package remoteapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/browserhost"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/trace"
)

// bigPageIndex is a LocalIndex holding one page session, "p-big", whose
// first prompt carries a page far longer than maxTextLen and ends with a
// short question.
func bigPageIndex(t *testing.T, prompt string) *LocalIndex {
	t.Helper()
	projects := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(filepath.Join(projects, "-w"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "-w", "p-big.jsonl"), []byte(`{"cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &LocalIndex{NewProvider: "claude", Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Providers: stubProviders(&stubStore{
		ProjectsDir: projects,
		Discover: func(history.DiscoverOpts, string) ([]history.Session, error) {
			return []history.Session{{ID: "p-big", Provider: "claude", LastActive: time.Now()}}, nil
		},
		ParseTrace: func(string) ([]trace.Turn, error) {
			return []trace.Turn{{Number: 1, Timestamp: time.Now(), UserLines: strings.Split(prompt, "\n"), OutputLines: []string{"It is about caching."}}}, nil
		},
	}, nil)}
}

func bigPagePrompt() string {
	return profilerun.Prompt(profilerun.AskRequest{URL: "https://big.example/a", Title: "Big",
		Page: strings.Repeat("lorem ipsum dolor sit amet ", 1000), Question: "what is the gist?"})
}

// The question of a page prompt longer than the text limit is found in the
// whole prompt, before the limit cuts it: every client gets the question.
func TestSessionDetail_LongPagePromptGivesTheQuestion(t *testing.T) {
	prompt := bigPagePrompt()
	if len(prompt) <= 2*maxTextLen {
		t.Fatalf("prompt is %d bytes; the test needs one well past %d", len(prompt), maxTextLen)
	}
	x := bigPageIndex(t, prompt)
	r := newProfileRig(t).as("device")
	r.api.Session = x.Session
	r.serve()
	resp, out := r.do("GET", "/v1/sessions/p-big", "")
	if resp.StatusCode != 200 {
		t.Fatalf("detail: %d %v", resp.StatusCode, out)
	}
	turns, _ := out["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("turns = %v", out["turns"])
	}
	if user := turns[0].(map[string]any)["user"]; user != "what is the gist?" {
		t.Fatalf("user = %.200q…, want the question", user)
	}
	// Text that is not an engine prompt is still capped at the limit.
	long := strings.Repeat("x", maxTextLen+100)
	if got := turnOf(trace.Turn{UserLines: []string{long}}).User; len(got) > maxTextLen+len("…") {
		t.Fatalf("plain user text not capped: %d bytes", len(got))
	}
}

// Wingman's history of the page (the relay over the real API on its
// socket) shows the question of the long first prompt.
func TestRelayHistory_LongPagePromptShowsTheQuestion(t *testing.T) {
	x := bigPageIndex(t, bigPagePrompt())
	r := newProfileRig(t)
	r.put("p-big", profilerun.Meta{Profile: "page", Client: "wingman", URL: "https://big.example/a", Title: "Big", Created: time.Now()})
	r.api.Session = x.Session

	dir, err := os.MkdirTemp("", "wq") // short enough for a macOS socket path
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

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { _ = inW.Close(); cancel() })
	go func() {
		_ = (&browserhost.Relay{Socket: sock}).Serve(ctx, inR, outW)
		_ = outW.Close()
	}()
	req, _ := json.Marshal(browserhost.Request{ID: "g1", Type: "history.get", URL: "https://big.example/a"})
	if err := browserhost.WriteMessage(inW, req); err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := browserhost.ReadMessage(outR)
		if err != nil {
			t.Fatalf("relay closed before history: %v", err)
		}
		var e browserhost.Event
		if err := json.Unmarshal(msg, &e); err != nil {
			t.Fatal(err)
		}
		if e.ID != "g1" {
			continue
		}
		if e.Type != "history" || e.Page == nil || len(e.Page.Turns) != 1 {
			t.Fatalf("event = %s", msg)
		}
		if q := e.Page.Turns[0].Question; q != "what is the gist?" {
			t.Fatalf("question = %.200q…, want the question", q)
		}
		return
	}
}
