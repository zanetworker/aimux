package remoteapi

import (
	"os"
	"slices"
	"testing"
)

// dropProfile removes profile from client's scope, as an edited
// clients.yaml does after a serve restart.
func (r *profileRig) dropProfile(client, profile string) {
	c := r.api.Scopes.Clients[client]
	c.Profiles = slices.DeleteFunc(slices.Clone(c.Profiles), func(p string) bool { return p == profile })
	r.api.Scopes.Clients[client] = c
}

// A follow-up on a profile session is checked against the caller's scope
// as it is now, not as it was when the session started.
func TestFollowUp_ProfileRemovedFromScope403NothingStarted(t *testing.T) {
	r := newProfileRig(t).serve()
	if resp, out := r.do("POST", "/v1/sessions", `{"profile":"work-readonly","url":"https://w.example/a","text":"q1"}`); resp.StatusCode != 202 || out["id"] != "p-1" {
		t.Fatalf("new session: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	if resp, out := r.do("POST", "/v1/sessions/p-1/messages", `{"text":"q2"}`); resp.StatusCode != 202 {
		t.Fatalf("allowed follow-up: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")

	r.dropProfile("wingman", "work-readonly")
	sends := r.f.sends.Load()
	resp, out := r.do("POST", "/v1/sessions/p-1/messages", `{"text":"q3"}`)
	if resp.StatusCode != 403 || out["code"] != "profile_not_allowed" {
		t.Fatalf("follow-up after the scope dropped the profile: %d %v", resp.StatusCode, out)
	}
	if n := r.f.sends.Load(); n != sends {
		t.Fatalf("%d turns sent after the refusal, want none", n-sends)
	}
	// The scope is the caller's: the owner's terminal may still continue it.
	r.as("cli")
	if resp, out := r.do("POST", "/v1/sessions/p-1/messages", `{"text":"q4"}`); resp.StatusCode != 202 {
		t.Fatalf("cli follow-up: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
}

// A session without a profile is not a profile session: its follow-ups are
// as before, whatever the scopes say about profiles.
func TestFollowUp_HostSessionUnaffected(t *testing.T) {
	r := newProfileRig(t).as("device").serve()
	r.dropProfile("device", "coding")
	if resp, out := r.do("POST", "/v1/sessions/s1/messages", `{"text":"next"}`); resp.StatusCode != 202 {
		t.Fatalf("host follow-up: %d %v", resp.StatusCode, out)
	}
	r.idle("s1")
}

// A profile session whose record cannot be read is refused before any
// turn starts: it must never run as a host session or skip the scope.
func TestFollowUp_UnreadableProfileStoreFailsClosed(t *testing.T) {
	r := newProfileRig(t).as("device").serve()
	if resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","url":"https://w.example/a","text":"q1"}`); resp.StatusCode != 202 || out["id"] != "p-1" {
		t.Fatalf("new session: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	if err := os.WriteFile(r.store.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sends := r.f.sends.Load()
	resp, out := r.do("POST", "/v1/sessions/p-1/messages", `{"text":"q2"}`)
	if resp.StatusCode != 500 || out["code"] != "internal" {
		t.Fatalf("unreadable store: %d %v", resp.StatusCode, out)
	}
	if n := r.f.sends.Load(); n != sends {
		t.Fatalf("%d turns sent, want none", n-sends)
	}
}
