package remoteapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/browserhost"
	"github.com/zanetworker/aimux/internal/profile"
)

// The panel's profiles.list, through a real Relay on the wingman socket
// served by the real API (LocalHandler("wingman")) over the builtin
// profiles: it lists exactly the profiles GET /v1/profiles allows Wingman,
// research-with-work among them (runs run), and never a host profile.
func TestRelayProfilesList_RealWingmanAPI(t *testing.T) {
	r := newProfileRig(t)
	builtins := map[string]profile.Manifest{}
	for _, n := range profile.BuiltinNames() {
		m, err := profile.Resolve("builtin:"+n, nil)
		if err != nil {
			t.Fatalf("builtin %s: %v", n, err)
		}
		m.Name = n
		builtins[n] = m
	}
	r.api.Profiles = func() (map[string]profile.Manifest, error) { return builtins, nil }

	evs := relayProfilesList(t, r)
	if len(evs) != 2 || evs[0].Type != "profiles" || evs[1].Type != "done" {
		t.Fatalf("events %+v, want profiles then done", evs)
	}
	var got struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal(evs[0].Data, &got); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{}
	for n, m := range builtins {
		if r.api.profileAllowed("wingman", m) {
			want[n] = true
		}
	}
	if len(got.Profiles) != len(want) {
		t.Fatalf("profiles %v, want exactly %v", got.Profiles, want)
	}
	byName := map[string]map[string]any{}
	for _, pr := range got.Profiles {
		n, _ := pr["name"].(string)
		if !want[n] || len(pr) != 4 || pr["digest"] != builtins[n].Digest() {
			t.Fatalf("profile %v: not allowed for wingman, or not name, runs, available and digest", pr)
		}
		byName[n] = pr
	}
	rww, ok := byName["research-with-work"]
	if !ok || rww["runs"] != "run" {
		t.Fatalf("research-with-work %v, want it listed with runs run", rww)
	}
	for _, pr := range got.Profiles {
		if pr["runs"] == runsHost {
			t.Fatalf("host profile %v offered to Wingman", pr)
		}
	}
}

// relayProfilesList sends profiles.list through a real Relay to r's API on
// a wingman socket and returns its events up to the terminal one.
func relayProfilesList(t *testing.T, r *profileRig) []browserhost.Event {
	t.Helper()
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
	b, _ := json.Marshal(browserhost.Request{ID: "p", Type: "profiles.list"})
	if err := browserhost.WriteMessage(p.in, b); err != nil {
		t.Fatal(err)
	}
	var evs []browserhost.Event
	for {
		msg, err := browserhost.ReadMessage(p.out)
		if err != nil {
			t.Fatal(err)
		}
		var e browserhost.Event
		if err := json.Unmarshal(msg, &e); err != nil {
			t.Fatal(err)
		}
		if e.ID != "p" {
			continue
		}
		evs = append(evs, e)
		if e.Type == "done" || e.Type == "error" {
			return evs
		}
	}
}

// A catalog that cannot be read reaches the panel as the API's own error:
// one error event with code internal, nothing else.
func TestRelayProfilesList_RealAPIUnreadableCatalog(t *testing.T) {
	r := newProfileRig(t)
	r.api.Profiles = func() (map[string]profile.Manifest, error) { return nil, errors.New("profiles dir unreadable") }
	evs := relayProfilesList(t, r)
	if len(evs) != 1 || evs[0].Type != "error" || evs[0].Code != "internal" {
		t.Fatalf("events %+v, want one internal error", evs)
	}
}

// A real catalog with more allowed profiles than one native message holds
// gets the host's one profiles_too_large error, no profiles and no done.
func TestRelayProfilesList_RealAPITooLarge(t *testing.T) {
	r := newProfileRig(t)
	many := map[string]profile.Manifest{}
	c := r.api.Scopes.Clients["wingman"]
	for i := range 12000 {
		n := fmt.Sprintf("page-%05d", i)
		many[n] = profile.Manifest{Name: n, Harness: "claude-code", Runs: "acp", Available: true, Network: profile.NetworkSpec{Default: "deny"}}
		c.Profiles = append(c.Profiles, n)
	}
	r.api.Scopes.Clients["wingman"] = c
	r.api.Profiles = func() (map[string]profile.Manifest, error) { return many, nil }
	evs := relayProfilesList(t, r)
	if len(evs) != 1 || evs[0].Type != "error" || evs[0].Code != "profiles_too_large" {
		t.Fatalf("events %+v, want one profiles_too_large error", evs)
	}
}
