package remoteapp

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/waiting"
)

// listActivity is each listed session's activity and waiting kind.
func listActivity(t *testing.T, h http.Handler) map[string][2]string {
	t.Helper()
	rec := serveRig(h, http.MethodGet, "/v1/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("list %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sessions []struct {
			ID       string `json:"id"`
			Activity string `json:"activity"`
			Waiting  *struct {
				Kind string `json:"kind"`
			} `json:"waiting"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string][2]string{}
	for _, s := range body.Sessions {
		kind := ""
		if s.Waiting != nil {
			kind = s.Waiting.Kind
		}
		out[s.ID] = [2]string{s.Activity, kind}
	}
	return out
}

// Remote serve fills activity from the state service: a session open in a
// terminal is idle, needs_you once its hook marker appears; a closed one is
// gone.
func TestServe_ListActivityFromTheStateService(t *testing.T) {
	h, home := openOnMacRig(t, nil, neverOpen(t))
	seedClaudeSession(t, home, "held", "/w", "fix it")
	seedClaudeSession(t, home, "closed", "/w", "old work")
	if err := holdInTerminal(home, "held"); err != nil {
		t.Fatal(err)
	}
	got := listActivity(t, h)
	if got["held"] != [2]string{"idle", ""} || got["closed"] != [2]string{"gone", ""} {
		t.Fatalf("before the marker: %v", got)
	}
	if err := waiting.Write(waiting.Dir(home), "held", "Claude needs your permission to use Bash", time.Now()); err != nil {
		t.Fatal(err)
	}
	got = listActivity(t, h)
	if got["held"] != [2]string{"needs_you", "permission"} {
		t.Fatalf("after the marker: held = %v", got["held"])
	}
}
