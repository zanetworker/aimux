package remoteapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
)

// A profile serve has no engine for is unavailable (serve's catalog marks
// it); the 409 says what to do instead: host profiles start without a
// profile today, run profiles are not there yet.
func TestNewSession_EngineLessProfiles409WithGuidance(t *testing.T) {
	r := newProfileRig(t)
	r.api.Profiles = func() (map[string]profile.Manifest, error) {
		all := testProfiles()
		c := all["coding"]
		c.Available = false
		all["coding"] = c
		all["research-with-work"] = profile.Manifest{Name: "research-with-work", Runs: "run", Available: false}
		return all, nil
	}
	r.serve().as("cli")
	out := r.expectRefused(`{"profile":"coding","text":"hi"}`, 409, "profile_not_available")
	if out["error"] != "coding sessions start without a profile today (POST /v1/sessions with cwd)" {
		t.Fatalf("host message = %q", out["error"])
	}
	out = r.expectRefused(`{"profile":"research-with-work","text":"hi"}`, 409, "profile_not_available")
	if out["error"] != "research-with-work is not available yet" {
		t.Fatalf("run message = %q", out["error"])
	}
}

// An engine failure is a 500 whose body carries no internal error text;
// the detail goes to the log only.
func TestNewSession_EngineFailure500HasNoInternalText(t *testing.T) {
	r := newProfileRig(t)
	r.api.NewProfileSession = func(context.Context, profilerun.Meta, string) (string, error) {
		return "", errors.New("sandbox aimux-work-readonly: openshell -g local at /Users/secret/.aimux/sandboxes failed")
	}
	r.serve()
	resp, out := r.do("POST", "/v1/sessions", `{"profile":"work-readonly","text":"hi"}`)
	if resp.StatusCode != 500 || out["code"] != "internal" {
		t.Fatalf("engine failure: %d %v", resp.StatusCode, out)
	}
	if msg, _ := out["error"].(string); strings.Contains(msg, "secret") || strings.Contains(msg, "openshell") || msg == "" {
		t.Fatalf("500 body leaks internal text or is empty: %q", msg)
	}
}

// Health says whether this serve is local-only, so a second serve can tell
// a Wingman-started one from a full one.
func TestHealth_ReportsLocalOnly(t *testing.T) {
	r := newProfileRig(t)
	r.api.LocalOnly = true
	r.serve()
	if _, out := r.do("GET", "/v1/health", ""); out["localOnly"] != true {
		t.Fatalf("health %v, want localOnly true", out)
	}
}
