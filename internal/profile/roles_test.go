package profile

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// research-with-work's planner is work-readonly itself: the same tool
// grant and no web, and the same sandbox, so a run never gives its planner
// more reach than a Workspace turn has and needs no sandbox of its own.
func TestBuiltin_ResearchPlannerIsWorkReadonly(t *testing.T) {
	rww, err := Resolve("research-with-work", nil)
	if err != nil {
		t.Fatal(err)
	}
	planner, ok := rww.Role("planner")
	if !ok || planner != "work-readonly" {
		t.Fatalf("research-with-work planner = %q %v, want work-readonly (its sandbox and grant)", planner, ok)
	}
	p, err := Resolve(planner, nil)
	if err != nil {
		t.Fatal(err)
	}
	wr, _ := Resolve("work-readonly", nil)
	if p.Runs != "sandbox" || !reflect.DeepEqual(p.Tools, wr.Tools) || slices.Contains(p.Network.Allow, "web") {
		t.Fatalf("planner %s = runs %q tools %v network %v; want work-readonly's grant and no web", planner, p.Runs, p.Tools, p.Network.Allow)
	}
}

// research-with-work's writer runs like Page: no tools, model egress only.
func TestBuiltin_ResearchWriterHasNoToolsNoNetwork(t *testing.T) {
	rww, _ := Resolve("research-with-work", nil)
	writer, ok := rww.Role("writer")
	if !ok {
		t.Fatal("research-with-work has no writer role")
	}
	w, err := Resolve(writer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w.Runs != "acp" || len(w.Tools) != 0 || !reflect.DeepEqual(w.Network.Allow, []string{"model"}) || w.Network.Default != "deny" {
		t.Fatalf("writer %s = runs %q tools %v network %+v", writer, w.Runs, w.Tools, w.Network)
	}
}

// Every role a profile names must resolve, so a run engine never starts a
// stage under a profile that does not exist.
func TestValidate_RoleTargetMustExist(t *testing.T) {
	user := map[string]Manifest{"mine": {Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "nope"}}}
	_, err := resolveChecked("mine", user)
	if err == nil || !strings.Contains(err.Error(), "role planner: profile nope not found") {
		t.Fatalf("Resolve = %v, want role planner: profile nope not found", err)
	}
	// A role naming a user profile resolves.
	user["myplanner"] = Manifest{Name: "myplanner", Extends: "builtin:work-readonly"}
	user["mine"] = Manifest{Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "myplanner", "writer": "page"}}
	m, err := resolveChecked("mine", user)
	if err != nil {
		t.Fatalf("Resolve with a user role target: %v", err)
	}
	if got, ok := m.Role("planner"); !ok || got != "myplanner" {
		t.Fatalf("Role(planner) = %q %v", got, ok)
	}
	if _, ok := m.Role("reader"); ok {
		t.Fatal("Role(reader) found a role the profile does not name")
	}
}

// A role target that exists but extends a missing profile names that
// ancestor, not the target, and a target whose resolved manifest is invalid
// is refused with its own reason.
func TestValidate_RoleTargetBrokenChainNamesTheAncestor(t *testing.T) {
	user := map[string]Manifest{
		"broken": {Name: "broken", Extends: "ghost"},
		"mine":   {Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "broken", "writer": "page"}},
	}
	_, err := resolveChecked("mine", user)
	if err == nil || !strings.Contains(err.Error(), "role planner") || !strings.Contains(err.Error(), `"ghost"`) || strings.Contains(err.Error(), "profile broken not found") {
		t.Fatalf("Resolve = %v, want role planner naming the missing ancestor ghost", err)
	}
	// Resolves (deny narrowed to another word is not a widening) but fails
	// validation: only validate catches it.
	user["badnet"] = Manifest{Name: "badnet", Extends: "builtin:base", Network: NetworkSpec{Default: "maybe"}}
	user["mine"] = Manifest{Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "badnet", "writer": "page"}}
	_, err = resolveChecked("mine", user)
	if err == nil || !strings.Contains(err.Error(), "role planner") || !strings.Contains(err.Error(), "network.default") {
		t.Fatalf("Resolve = %v, want the role target's own validation error", err)
	}
}

// A role target's own roles are checked too, and a cycle of roles ends.
func TestValidate_NestedRolesAndCycles(t *testing.T) {
	user := map[string]Manifest{
		"inner": {Name: "inner", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "missing"}},
		"mine":  {Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "inner", "writer": "page"}},
	}
	_, err := resolveChecked("mine", user)
	if err == nil || !strings.Contains(err.Error(), "profile missing not found") {
		t.Fatalf("Resolve = %v, want the nested role's missing target", err)
	}
	user["a"] = Manifest{Name: "a", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "b", "writer": "page"}}
	user["b"] = Manifest{Name: "b", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "a", "writer": "page"}}
	if _, err := resolveChecked("a", user); err != nil {
		t.Fatalf("a role cycle of valid profiles: %v", err)
	}
}

// A sandbox role target whose sandbox name OpenShell would refuse is
// refused here, before a run starts a stage under it.
func TestValidate_RoleTargetSandboxNameTooLong(t *testing.T) {
	user := map[string]Manifest{
		"my-very-long-planner": {Name: "my-very-long-planner", Extends: "builtin:work-readonly"},
		"mine":                 {Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "my-very-long-planner", "writer": "page"}},
	}
	_, err := resolveChecked("mine", user)
	if err == nil || !strings.Contains(err.Error(), "role planner") || !strings.Contains(err.Error(), "sandbox name too long") {
		t.Fatalf("Resolve = %v, want the role target's sandbox name refused", err)
	}
}

// A role target whose sandbox name passes the length limit but not the
// launcher's naming rule is refused too.
func TestValidate_RoleTargetSandboxNameLauncherRule(t *testing.T) {
	user := map[string]Manifest{
		"Plan_A": {Name: "Plan_A", Extends: "builtin:work-readonly"},
		"mine":   {Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "Plan_A", "writer": "page"}},
	}
	_, err := resolveChecked("mine", user)
	if err == nil || !strings.Contains(err.Error(), "role planner") || !strings.Contains(err.Error(), "aimux-Plan_A") {
		t.Fatalf("Resolve = %v, want the launcher's naming rule to refuse aimux-Plan_A", err)
	}
	if !ValidSandboxName("aimux-work-readonly") || ValidSandboxName("-flag") || ValidSandboxName("aimux-Plan_A") {
		t.Fatal("ValidSandboxName disagrees with the launcher rule")
	}
}

// resolveChecked is Resolve then CheckRoles, as the places that admit a
// profile do.
func resolveChecked(name string, user map[string]Manifest) (Manifest, error) {
	m, err := Resolve(name, user)
	if err != nil {
		return m, err
	}
	return m, CheckRoles(m, user)
}

// Resolve itself ignores roles: a session already running under a profile
// whose role broke can still be routed, cancelled and recovered.
func TestResolve_IgnoresBrokenRoles(t *testing.T) {
	user := map[string]Manifest{"mine": {Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "nope", "writer": "page"}}}
	m, err := Resolve("mine", user)
	if err != nil || m.Runs != "run" {
		t.Fatalf("Resolve = %+v %v, want the profile despite its broken role", m, err)
	}
	if CheckRoles(m, user) == nil {
		t.Fatal("CheckRoles accepted a missing role target")
	}
}

// A sandbox name ending in a dash or holding two dashes in a row is not a
// DNS-1123 label, so OpenShell would refuse it.
func TestValidSandboxName_DashRules(t *testing.T) {
	for name, want := range map[string]bool{"aimux-notes": true, "aimux-team-notes": true, "aimux-notes-": false, "aimux-team--notes": false, "aimux-": false, "-aimux": false, "aimux-9x": true} {
		if got := ValidSandboxName(name); got != want {
			t.Errorf("ValidSandboxName(%q) = %v, want %v", name, got, want)
		}
	}
}

// An empty role target is refused by name before any lookup, not reported
// as a profile "" that was not found.
func TestCheckRoles_EmptyTargetRefused(t *testing.T) {
	user := map[string]Manifest{"mine": {Name: "mine", Extends: "builtin:research-with-work", Roles: map[string]string{"planner": "", "writer": "page"}}}
	m, err := Resolve("mine", user)
	if err != nil {
		t.Fatal(err)
	}
	err = CheckRoles(m, user)
	if err == nil || !strings.Contains(err.Error(), "role planner: no profile named") {
		t.Fatalf("CheckRoles = %v, want role planner: no profile named", err)
	}
}
