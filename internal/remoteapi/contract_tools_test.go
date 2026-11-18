package remoteapi

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// TestContract_Tools drives GET /v1/tools through the contract with every
// set and tool status, a load failure and the unsupported answer
// (TestContract leaves it to this test).
func TestContract_Tools(t *testing.T) {
	api, as := contractProfileAPI(t, profileFixture{})
	reg, pins, live := toolsFixture(t)
	all, _ := api.Profiles()
	m := all["work-readonly"]
	m.Tools = []string{"wiki:readonly", "jira:readonly", "slack:readonly", "google:readonly", "broken:readonly", "missing:readonly", "bad"}
	all["work-readonly"] = m
	all["ops"] = profile.Manifest{Name: "ops", Runs: "sandbox", Tools: []string{"github:readonly"}}
	api.Profiles = func() (map[string]profile.Manifest, error) { return all, nil }
	view := ToolsView{Snapshot: toolreg.NewSnapshot(reg, pins, nil), Live: live}
	api.Tools = func() ToolsView { return view }
	c := newContract(t, api.Handler([]string{testLogin}, []string{"127.0.0.1"}))

	statuses := map[string]bool{}
	for _, client := range []string{"wingman", "device", "cli"} {
		as(client)
		st, body := c.call("GET", "/v1/tools", "", testLogin)
		if st != 200 || body["snapshot"] != view.Snapshot.Version() {
			t.Fatalf("%s: %d %v", client, st, body)
		}
		for _, s := range body["sets"].([]any) {
			set := s.(map[string]any)
			statuses["set "+set["status"].(string)] = true
			for _, tl := range set["tools"].([]any) {
				statuses["tool "+tl.(map[string]any)["status"].(string)] = true
			}
		}
	}
	for _, want := range []string{"set available", "set partial", "set unavailable", "tool active", "tool unpinned", "tool changed", "tool unavailable", "tool source_changed", "tool unknown"} {
		if !statuses[want] {
			t.Errorf("never served: %s", want)
		}
	}

	_, health := c.call("GET", "/v1/health", "", testLogin)
	if !slices.Contains(health["capabilities"].([]any), any(CapTools)) {
		t.Fatalf("capabilities %v, want %s", health["capabilities"], CapTools)
	}

	// The diff, and approve's refusals (its success needs a device step-up;
	// tools_approve_test.go covers it).
	as("cli")
	if st, body := c.call("GET", "/v1/tools/jira:readonly/diff", "", testLogin); st != 403 || body["code"] != "forbidden_for_client" {
		t.Fatalf("diff without tools_approve: %d %v", st, body)
	}
	cli := api.Scopes.Clients["cli"]
	cli.ToolsApprove = true
	api.Scopes.Clients["cli"] = cli
	if st, body := c.call("GET", "/v1/tools/jira:readonly/diff", "", testLogin); st != 200 || body["source"] != "jira" {
		t.Fatalf("diff: %d %v", st, body)
	}
	if st, body := c.call("GET", "/v1/tools/nope:readonly/diff", "", testLogin); st != 403 || body["code"] != "forbidden_for_client" {
		t.Fatalf("diff of a set no profile requests: %d %v", st, body)
	}
	profiles := api.Profiles
	api.Profiles = func() (map[string]profile.Manifest, error) { return nil, errors.New("profiles unreadable") }
	if st, body := c.call("GET", "/v1/tools/jira:readonly/diff", "", testLogin); st != 500 || body["code"] != "internal" {
		t.Fatalf("diff with profiles unreadable: %d %v", st, body)
	}
	api.Profiles = profiles
	approve := `{"set":"jira:readonly","tools":[{"name":"b","pin":"sha256:x"}]}`
	cli.ToolsApprove = false
	api.Scopes.Clients["cli"] = cli
	if st, body := c.call("POST", "/v1/tools/approve", approve, testLogin); st != 403 || body["code"] != "forbidden_for_client" {
		t.Fatalf("approve without the scope: %d %v", st, body)
	}
	cli.ToolsApprove = true
	api.Scopes.Clients["cli"] = cli
	if st, body := c.call("POST", "/v1/tools/approve", approve, testLogin); st != 403 || body["code"] != "step_up_required" {
		t.Fatalf("approve without a step-up: %d %v", st, body)
	}

	view = ToolsView{Snapshot: toolreg.NewSnapshot(reg, nil, errors.New("pins unreadable"))}
	st, body := c.call("GET", "/v1/tools", "", testLogin)
	if st != 200 || len(body["errors"].([]any)) != 1 {
		t.Fatalf("load failure: %d %v", st, body)
	}
	api.Tools = nil
	st, body = c.call("GET", "/v1/tools", "", testLogin)
	if st != 404 || body["code"] != "not_supported" {
		t.Fatalf("unsupported: %d %v", st, body)
	}
	if _, health = c.call("GET", "/v1/health", "", testLogin); slices.Contains(health["capabilities"].([]any), any(CapTools)) {
		t.Fatalf("capabilities %v offer %s without the tool view", health["capabilities"], CapTools)
	}

	for path, item := range c.doc.Paths.Map() {
		if !strings.HasPrefix(path, "/v1/tools") {
			continue
		}
		for method, op := range item.Operations() {
			if !c.ops[op.OperationID] {
				t.Errorf("never exercised: %s %s", method, path)
			}
		}
	}
	if !c.ops["listTools"] {
		t.Fatal("GET /v1/tools is not in the contract")
	}
}
