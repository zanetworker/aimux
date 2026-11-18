package remoteapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// approveRig is toolsRig with a pins file under a temp dir and client
// scoped for approving (tools_approve on, step-up kinds as given).
func approveRig(t *testing.T, client string, approve bool, kinds []string) (*profileRig, toolgateway.PinStore) {
	t.Helper()
	r, snap := toolsRig(t, nil)
	store := toolgateway.PinStore{Path: filepath.Join(t.TempDir(), "pins.json")}
	pins := toolgateway.PinFile{}
	for src, tools := range toolgateway.PinsOf(snap) {
		pins[src] = map[string]toolgateway.PinRecord{}
		for n, rec := range tools {
			pins[src][n] = rec
		}
	}
	if err := store.Save(pins); err != nil {
		t.Fatal(err)
	}
	r.api.Pins = &store
	c := r.api.Scopes.Clients[client]
	c.ToolsApprove, c.StepUpKinds = approve, kinds
	r.api.Scopes.Clients[client] = c
	r.as(client)
	return r, store
}

func postApprove(t *testing.T, r *profileRig, body string) (int, map[string]any) {
	t.Helper()
	resp, out := r.do("POST", "/v1/tools/approve", body)
	return resp.StatusCode, out
}

// jira:readonly's b is changed: live b is the integer definition.
func liveB(t *testing.T) string { return toolHash(t, toolDef("b", "integer")) }

func TestToolsApprove_MissingScopeRefused403(t *testing.T) {
	r, store := approveRig(t, "wingman", false, nil)
	before, _ := store.LoadFile()
	st, out := postApprove(t, r, `{"set":"jira:readonly","tools":[{"name":"b","pin":"`+liveB(t)+`"}]}`)
	if st != http.StatusForbidden || out["code"] != "forbidden_for_client" {
		t.Fatalf("%d %v, want 403 forbidden_for_client", st, out)
	}
	if after, _ := store.LoadFile(); after["jira"]["b"].Hash != before["jira"]["b"].Hash {
		t.Fatal("pins changed on a refused request")
	}
}

// A client allowed to approve but with no usable step-up kind (Wingman has
// none; a local socket has no login to step up) is refused.
func TestToolsApprove_NoStepUpRefused(t *testing.T) {
	for _, kinds := range [][]string{nil, {"secure-enclave"}} {
		r, _ := approveRig(t, "wingman", true, kinds)
		st, out := postApprove(t, r, `{"set":"jira:readonly","tools":[{"name":"b","pin":"`+liveB(t)+`"}]}`)
		if st != http.StatusForbidden || out["code"] != "step_up_required" {
			t.Fatalf("kinds %v: %d %v, want 403 step_up_required", kinds, st, out)
		}
	}
	// A device request with a bearer that is no device login (a sandbox
	// lease token, say) has no login age: refused the same way.
	r, _ := approveRig(t, "device", true, []string{"secure-enclave"})
	req, _ := http.NewRequest("POST", r.srv.URL+"/v1/tools/approve", strings.NewReader(`{"set":"jira:readonly","tools":[{"name":"b","pin":"`+liveB(t)+`"}]}`))
	req.Header.Set("Tailscale-User-Login", testLogin)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("ab", 32))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "step_up_required") {
		t.Fatalf("lease-shaped bearer: %d %s", resp.StatusCode, b)
	}
}

// steppedUpRig is a device client allowed to approve, with device login on
// and a fresh secure-enclave login: post sends its token, a real step-up.
type steppedUpRig struct {
	*stepUpRig
	tok   string
	store toolgateway.PinStore
}

func newSteppedUpRig(t *testing.T) *steppedUpRig {
	t.Helper()
	r := newStepUpRig(t, false)
	tr, snap := toolsRig(t, nil)
	r.api.Profiles, r.api.Tools = tr.api.Profiles, tr.api.Tools
	ar, store := approveRig(t, "device", true, []string{"secure-enclave"})
	r.api.Pins = ar.api.Pins
	_ = snap
	c := r.api.Scopes.Clients["device"]
	c.ToolsApprove, c.StepUpKinds = true, []string{"secure-enclave"}
	r.api.Scopes.Clients["device"] = c
	return &steppedUpRig{stepUpRig: r, tok: r.login.pair("iphone"), store: store}
}

func (r *steppedUpRig) post(body string) (int, map[string]any) {
	r.t.Helper()
	return r.login.do("POST", "/v1/tools/approve", body, r.tok)
}

// A step-up older than 60 seconds is refused like none.
func TestToolsApprove_OldStepUpRefused(t *testing.T) {
	r := newSteppedUpRig(t)
	r.login.clock.add(61 * time.Second)
	if st, out := r.post(`{"set":"jira:readonly","tools":[{"name":"b","pin":"` + liveB(t) + `"}]}`); st != 403 || out["code"] != "step_up_required" {
		t.Fatalf("%d %v", st, out)
	}
}

func TestToolsApprove_StalePinRefused(t *testing.T) {
	r := newSteppedUpRig(t)
	store := r.store
	postApprove := func(_ *testing.T, _ *steppedUpRig, body string) (int, map[string]any) { return r.post(body) }
	before, _ := store.LoadFile()
	stale := toolHash(t, toolDef("b", "number")) // what the client showed; live is integer
	st, out := postApprove(t, r, `{"set":"jira:readonly","tools":[{"name":"b","pin":"`+stale+`"}]}`)
	if st != http.StatusConflict || out["code"] != "pin_stale" {
		t.Fatalf("%d %v, want 409 pin_stale", st, out)
	}
	// Not listed live (d): stale too.
	st, out = postApprove(t, r, `{"set":"jira:readonly","tools":[{"name":"d","pin":"sha256:x"}]}`)
	if st != http.StatusConflict || out["code"] != "pin_stale" {
		t.Fatalf("unlisted: %d %v, want 409 pin_stale", st, out)
	}
	// One stale tool refuses the whole request.
	st, _ = postApprove(t, r, `{"set":"jira:readonly","tools":[{"name":"c","pin":"`+toolHash(t, toolDef("c", "string"))+`"},{"name":"b","pin":"`+stale+`"}]}`)
	if after, _ := store.LoadFile(); st != http.StatusConflict || len(after["jira"]) != len(before["jira"]) {
		t.Fatalf("%d, pins %v: nothing may be written", st, after["jira"])
	}
}

func TestToolsApprove_SuccessWritesPins(t *testing.T) {
	r := newSteppedUpRig(t)
	store := r.store
	st, out := r.post(`{"set":"jira:readonly","tools":[{"name":"b","pin":"` + liveB(t) + `"},{"name":"c","pin":"` + toolHash(t, toolDef("c", "string")) + `"}]}`)
	if st != http.StatusOK || out["written"] != true {
		t.Fatalf("%d %v, want 200 written", st, out)
	}
	pins, err := store.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	b, c := pins["jira"]["b"], pins["jira"]["c"]
	if b.Hash != liveB(t) || c.Hash != toolHash(t, toolDef("c", "string")) || b.Definition == nil || b.ApprovedAt.IsZero() || time.Since(b.ApprovedAt) > time.Minute {
		t.Fatalf("pins b %+v c %+v, want the live definitions approved now", b, c)
	}
	if pins["jira"]["a"].ApprovedAt != toolsT0 {
		t.Fatal("a tool not in the request changed")
	}
	results, _ := out["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results %v", out["results"])
	}
}

func TestToolsApprove_BadRequests(t *testing.T) {
	r := newSteppedUpRig(t)
	for body, code := range map[string]string{
		`{"set":"jira:readonly","tools":[]}`:                               "bad_request",
		`{"set":"jira:readonly","tools":[{"name":"zz","pin":"sha256:x"}]}`: "bad_request", // not in the set
		`{"set":"nope:readonly","tools":[{"name":"a","pin":"sha256:x"}]}`:  "forbidden_for_client",
		`{"set":"jira:readonly","tools":[{"name":"a"}]}`:                   "bad_request",
		`{"set":"jira:readonly","extra":1}`:                                "bad_request",
	} {
		if st, out := r.post(body); out["code"] != code || st/100 != 4 {
			t.Errorf("%s: %d %v, want %s", body, st, out, code)
		}
	}
}

// The diff is the set's tools with their live definitions and pins, for
// the client to show and send back; no URL or secret of the fixture.
func TestToolsDiff_LiveDefinitionsAndPins(t *testing.T) {
	r, _ := approveRig(t, "wingman", true, nil)
	resp, err := http.Get(r.srv.URL + "/v1/tools/jira:readonly/diff")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	req, _ := http.NewRequest("GET", r.srv.URL+"/v1/tools/jira:readonly/diff", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	for _, s := range toolsSecrets {
		if strings.Contains(string(raw), s) {
			t.Fatalf("diff carries %q: %s", s, raw)
		}
	}
	var d ToolsDiff
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		t.Fatal(err)
	}
	by := map[string]ToolDiff{}
	for _, tl := range d.Tools {
		by[tl.Name] = tl
	}
	if d.Set != "jira:readonly" || d.Source != "jira" || len(d.Tools) != 4 {
		t.Fatalf("diff %+v", d)
	}
	if b := by["b"]; b.Status != toolgateway.StatusChanged || b.Pin != liveB(t) || b.Approved != toolHash(t, toolDef("b", "string")) || len(b.Definition) == 0 {
		t.Fatalf("b %+v", b)
	}
	if dd := by["d"]; dd.Pin != "" || dd.Definition != nil {
		t.Fatalf("d is not listed live: %+v", dd)
	}
	// A set no profile of the client requests is refused.
	req, _ = http.NewRequest("GET", r.srv.URL+"/v1/tools/github:readonly/diff", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("github:readonly for wingman: %d, want 403", resp2.StatusCode)
	}
}

// aimux's own annotations (an http-openapi tool's base URL and path) stay
// out of the definition a client sees; the pin is over the whole tool.
func TestClientDefinition_DropsAimuxAnnotations(t *testing.T) {
	raw := json.RawMessage(`{"name":"get","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true,"x-aimux-base":"https://api.internal.example","x-aimux-http":"GET /x"}}`)
	got := string(clientDefinition(raw))
	if strings.Contains(got, "x-aimux") || strings.Contains(got, "internal.example") || !strings.Contains(got, "readOnlyHint") {
		t.Fatalf("client definition %s", got)
	}
	if string(clientDefinition(json.RawMessage(`{"name":"plain"}`))) != `{"name":"plain"}` {
		t.Fatal("a definition without annotations changed")
	}
}

// Trailing data after the object is refused, never half read; approvals
// are audited with the device and the health offers the routes.
func TestToolsApprove_StrictBodyAuditAndCapability(t *testing.T) {
	r := newSteppedUpRig(t)
	body := `{"set":"jira:readonly","tools":[{"name":"b","pin":"` + liveB(t) + `"}]}`
	if st, out := r.post(body + `{"set":"x"}`); st != 400 || out["code"] != "bad_request" {
		t.Fatalf("trailing object: %d %v", st, out)
	}
	if after, _ := r.store.LoadFile(); after["jira"]["b"].Hash == liveB(t) {
		t.Fatal("pins written for a refused body")
	}
	if st, _ := r.post(body); st != 200 {
		t.Fatalf("approve: %d", st)
	}
	if !strings.Contains(r.login.audit.String(), `"action":"tools_approve"`) {
		t.Fatalf("audit lacks the approval:\n%s", r.login.audit.String())
	}
	_, health := r.login.do("GET", "/v1/health", "", r.tok)
	caps, _ := health["capabilities"].([]any)
	if !slices.Contains(caps, any(CapToolsApprove)) {
		t.Fatalf("capabilities %v", caps)
	}
}

// The diff is only for a client that may approve.
func TestToolsDiff_NeedsToolsApprove(t *testing.T) {
	r, _ := approveRig(t, "wingman", false, nil)
	if resp, out := r.do("GET", "/v1/tools/jira:readonly/diff", ""); resp.StatusCode != 403 || out["code"] != "forbidden_for_client" {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
}

// What the diff cannot show is approved on the Mac only: a tool whose
// source's address changed (google x), and one whose hidden request
// details changed against the approved definition.
func TestToolsApprove_ReviewOnMac(t *testing.T) {
	r := newSteppedUpRig(t)
	x := toolHash(t, toolDef("x", "string"))
	if st, out := r.post(`{"set":"google:readonly","tools":[{"name":"x","pin":"` + x + `"}]}`); st != 409 || out["code"] != "review_on_mac" {
		t.Fatalf("source changed: %d %v", st, out)
	}

	// jira c, approved with a base URL; live now names another one.
	withBase := func(base string) json.RawMessage {
		return json.RawMessage(`{"name":"c","inputSchema":{"type":"object"},"annotations":{"x-aimux-base":"` + base + `","x-aimux-http":"GET /c"}}`)
	}
	approved, live := withBase("https://a.example"), withBase("https://b.example")
	if _, err := r.store.Update(func(f toolgateway.PinFile) (toolgateway.PinFile, bool, error) {
		f["jira"]["c"] = toolgateway.PinRecord{Hash: toolHash(t, approved), Definition: approved}
		return f, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	view := r.api.Tools()
	f, _ := r.store.LoadFile()
	view.Snapshot = toolreg.NewSnapshot(view.Snapshot.Registry(), f.Table(), nil) // as remote serve reloads it
	view.Live["jira"].Tools["c"] = live
	r.api.Tools = func() ToolsView { return view }
	if st, out := r.post(`{"set":"jira:readonly","tools":[{"name":"c","pin":"` + toolHash(t, live) + `"}]}`); st != 409 || out["code"] != "review_on_mac" {
		t.Fatalf("hidden details changed: %d %v", st, out)
	}
	// The same hidden details as approved: a client may approve it.
	view.Live["jira"].Tools["c"] = withBase("https://a.example")
	if st, out := r.post(`{"set":"jira:readonly","tools":[{"name":"c","pin":"` + toolHash(t, approved) + `"}]}`); st != 200 {
		t.Fatalf("same hidden details: %d %v", st, out)
	}
}

// Hidden details that disappear are a change too.
func TestLocalReview_HiddenDetailsRemoved(t *testing.T) {
	approved := json.RawMessage(`{"name":"c","annotations":{"x-aimux-base":"https://a.example"}}`)
	view := ToolsView{Live: map[string]toolgateway.LiveSource{"api": {Tools: map[string]json.RawMessage{"c": json.RawMessage(`{"name":"c"}`)}}}}
	pins := toolgateway.PinFile{"api": {"c": {Hash: "sha256:a", Definition: approved}}}
	if localReview(view, pins, "api", "c", toolgateway.StatusChanged) == "" {
		t.Fatal("removed hidden details approved from a client")
	}
	if localReview(view, toolgateway.PinFile{"api": {"c": {Hash: "sha256:a", Definition: json.RawMessage(`{"name":"c"}`)}}}, "api", "c", toolgateway.StatusChanged) != "" {
		t.Fatal("a tool with nothing hidden needs the Mac")
	}
}

// An approval that lands between the check and the write is not
// overwritten: the request is refused as stale.
func TestApprovePins_RefusesARecordThatMoved(t *testing.T) {
	r := newSteppedUpRig(t)
	checked, _ := r.store.LoadFile()
	if _, err := r.store.Update(func(f toolgateway.PinFile) (toolgateway.PinFile, bool, error) {
		f["jira"]["b"] = toolgateway.PinRecord{Hash: "sha256:from-the-mac"}
		return f, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.api.approvePins(r.api.Tools(), []string{"jira:readonly/b"}, checked); !errors.Is(err, errPinsMoved) {
		t.Fatalf("err %v, want errPinsMoved", err)
	}
	if f, _ := r.store.LoadFile(); f["jira"]["b"].Hash != "sha256:from-the-mac" {
		t.Fatal("the Mac's approval was overwritten")
	}
}

// A request against another snapshot than the current one, or whose pins
// moved after the snapshot it is checked under, is stale.
func TestToolsApprove_SnapshotAndPinsMustAgree(t *testing.T) {
	r := newSteppedUpRig(t)
	if st, out := r.post(`{"set":"jira:readonly","snapshot":"other","tools":[{"name":"b","pin":"` + liveB(t) + `"}]}`); st != 409 || out["code"] != "pin_stale" {
		t.Fatalf("other snapshot: %d %v", st, out)
	}
	if _, err := r.store.Update(func(f toolgateway.PinFile) (toolgateway.PinFile, bool, error) {
		f["jira"]["b"] = toolgateway.PinRecord{Hash: "sha256:cli-meanwhile"}
		return f, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if st, out := r.post(`{"set":"jira:readonly","tools":[{"name":"b","pin":"` + liveB(t) + `"}]}`); st != 409 || out["code"] != "pin_stale" {
		t.Fatalf("pins moved past the snapshot: %d %v", st, out)
	}
	if f, _ := r.store.LoadFile(); f["jira"]["b"].Hash != "sha256:cli-meanwhile" {
		t.Fatal("the other approval was overwritten")
	}
}
