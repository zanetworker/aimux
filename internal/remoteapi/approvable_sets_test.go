package remoteapi

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profilerun"

	"github.com/zanetworker/aimux/internal/profile"
)

// ApprovableSets keeps only the sets this client may diff and approve under
// its own scope (the diff endpoint's check), in order; a role does not
// widen it.
func TestAPI_ApprovableSetsFollowsClientScope(t *testing.T) {
	r := newProfileRig(t)
	r.api.Profiles = func() (map[string]profile.Manifest, error) {
		all := testProfiles()
		wr := all["work-readonly"]
		wr.Tools = []string{"jira:readonly", "wiki:readonly"}
		all["work-readonly"] = wr
		all["research-with-work"] = profile.Manifest{Name: "research-with-work", Runs: "run", Available: true, Roles: map[string]string{"planner": "work-readonly", "writer": "page"}}
		return all, nil
	}
	sc := r.api.Scopes.Clients["wingman"]
	sc.ToolsApprove = true
	r.api.Scopes.Clients["wingman"] = sc
	if got := r.api.ApprovableSets("wingman", []string{"wiki:readonly", "gh:readonly", "jira:readonly"}); !reflect.DeepEqual(got, []string{"wiki:readonly", "jira:readonly"}) {
		t.Fatalf("wingman = %v", got)
	}
	// Without tools_approve the diff endpoint refuses every set: none.
	sc.ToolsApprove = false
	r.api.Scopes.Clients["wingman"] = sc
	if got := r.api.ApprovableSets("wingman", []string{"wiki:readonly"}); len(got) != 0 {
		t.Fatalf("tools_approve off: %v", got)
	}
	sc.ToolsApprove = true
	// A client whose scope has research-with-work but not work-readonly may
	// approve none of the planner's sets.
	sc.Profiles = []string{"page", "research", "research-with-work"}
	r.api.Scopes.Clients["wingman"] = sc
	if got := r.api.ApprovableSets("wingman", []string{"wiki:readonly", "jira:readonly"}); len(got) != 0 {
		t.Fatalf("roles widened the scope: %v", got)
	}
}

// Both turn starts hand the engine the authenticated caller (not the
// session's creator), so per-client filtering uses who asked.
func TestAPI_TurnCarriesCaller(t *testing.T) {
	r := newProfileRig(t)
	var callers []string
	var mu sync.Mutex
	r.f.sendF = func(ctx context.Context, _, _ string, _ func(acprun.Event)) (string, error) {
		mu.Lock()
		callers = append(callers, profilerun.CallerFrom(ctx))
		mu.Unlock()
		return "end_turn", nil
	}
	r.serve()
	resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","url":"https://e.example/a","text":"q"}`)
	if resp.StatusCode != 202 {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	if resp, out := r.do("POST", "/v1/sessions/p-1/messages", `{"text":"more"}`); resp.StatusCode != 202 {
		t.Fatalf("message: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(callers, []string{"wingman", "wingman"}) {
		t.Fatalf("callers %v", callers)
	}
}
