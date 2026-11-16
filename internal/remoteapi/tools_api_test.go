package remoteapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

var toolsT0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func toolDef(name, qType string) json.RawMessage {
	return json.RawMessage(`{"name":"` + name + `","description":"about ` + name + `","inputSchema":{"type":"object","properties":{"q":{"type":"` + qType + `"}}}}`)
}

func toolHash(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	h, err := toolgateway.PinTool(raw)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Secrets the fixture plants in URLs, errors and definitions; none may
// reach a response.
// The fake credentials are assembled at run time so secret scanners do not
// flag the fixture's source; the planted strings are unchanged.
var (
	fakePW    = "pw9" + "secret"
	fakeTok   = "sk-" + "live-0123456789abcdef"
	fakeKey   = "abc123" + "secretvalue"
	fakeSlack = "xoxb-" + "1234-secret"
)

var toolsSecrets = []string{fakePW, fakeTok, fakeKey, fakeSlack, "slack.example.com", "user:"}

// toolsFixture is a registry, pins and gateway view with every status:
//
//	wiki:readonly   -> notes:readonly (alias); notes never listed by the gateway: unknown
//	jira:readonly   a active, b changed, c unpinned, d not listed: partial
//	slack:readonly  backend down, its error carrying a URL and a token: unavailable
//	google:readonly pinned from another endpoint: source_changed, unavailable
//	github:readonly active (requested only by a profile cli alone sees)
//	broken:readonly dropped by the registry (its source is invalid)
//	missing:readonly not in the registry; bad is not a set name
func toolsFixture(t *testing.T) (toolreg.Registry, toolreg.PinTable, map[string]toolgateway.LiveSource) {
	t.Helper()
	srcErr := errors.New(`url "https://user:` + fakePW + `@brk.example.com/mcp" must be an http or https URL with a host`)
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{
			"notes":  {Type: toolreg.TypeLocalNotes, Root: "/notes"},
			"jira":   {Type: toolreg.TypeMCPHTTP, URL: "http://user:" + fakePW + "@127.0.0.1:8001/mcp?token=" + fakeTok},
			"slack":  {Type: toolreg.TypeMCPHTTP, URL: "https://slack.example.com/mcp?key=" + fakeKey},
			"google": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8003/mcp"},
			"github": {Type: toolreg.TypeMCPHTTP, URL: "http://localhost:8004/mcp"},
		},
		Sets: map[string]toolreg.Set{
			"notes:readonly":  {Source: "notes", Tools: []string{"wiki_search", "wiki_read"}},
			"jira:readonly":   {Source: "jira", Tools: []string{"d", "c", "b", "a"}},
			"slack:readonly":  {Source: "slack", Tools: []string{"search"}},
			"google:readonly": {Source: "google", Tools: []string{"x"}},
			"github:readonly": {Source: "github", Tools: []string{"g"}},
			"unused:readonly": {Source: "github", Tools: []string{"g"}},
		},
		SourceErrors: map[string]error{"brk": fmt.Errorf("tool sources /Users/someone/.config/aimux/tool-sources.yaml: source %q: %w", "brk", srcErr)},
	}
	reg.SetErrors = map[string]error{"broken:readonly": fmt.Errorf("tool sources /Users/someone/.config/aimux/tool-sources.yaml: set %q: source %q is invalid: %w", "broken:readonly", "brk", reg.SourceErrors["brk"])}
	pins := toolreg.PinTable{
		"jira": {
			"a": {Hash: toolHash(t, toolDef("a", "string")), ApprovedAt: toolsT0},
			"b": {Hash: toolHash(t, toolDef("b", "string"))},
		},
		"slack":  {"search": {Hash: "sha256:s"}},
		"google": {"x": {Hash: toolHash(t, toolDef("x", "string")), Source: "http://127.0.0.1:9000/mcp"}},
		"github": {"g": {Hash: toolHash(t, toolDef("g", "string"))}},
		"notes":  {"wiki_read": {Hash: "sha256:w"}},
	}
	live := map[string]toolgateway.LiveSource{
		"jira":   {Tools: map[string]json.RawMessage{"a": toolDef("a", "string"), "b": toolDef("b", "integer"), "c": toolDef("c", "string")}},
		"slack":  {Err: errors.New(`Post "https://slack.example.com/mcp?key=` + fakeKey + `": Bearer ` + fakeSlack + ` rejected`)},
		"google": {Tools: map[string]json.RawMessage{"x": toolDef("x", "string")}},
		"github": {Tools: map[string]json.RawMessage{"g": toolDef("g", "string")}},
	}
	return reg, pins, live
}

// toolsRig is a profile rig whose profiles request the fixture's sets:
// work-readonly (wingman, device, cli) and ops, which only cli may use.
func toolsRig(t *testing.T, loadErr error) (*profileRig, toolreg.Snapshot) {
	t.Helper()
	r := newProfileRig(t)
	reg, pins, live := toolsFixture(t)
	snap := toolreg.NewSnapshot(reg, pins, loadErr)
	if loadErr != nil {
		snap = toolreg.NewSnapshot(reg, nil, loadErr) // a failed pins load leaves none
	}
	profiles := testProfiles()
	wr := profiles["work-readonly"]
	wr.Tools = []string{"wiki:readonly", "jira:readonly", "slack:readonly", "google:readonly", "broken:readonly", "missing:readonly", "bad", "jira:readonly"}
	profiles["work-readonly"] = wr
	profiles["ops"] = profile.Manifest{Name: "ops", Runs: "sandbox", Available: true, Tools: []string{"github:readonly"}}
	r.api.Profiles = func() (map[string]profile.Manifest, error) { return profiles, nil }
	r.api.Tools = func() ToolsView { return ToolsView{Snapshot: snap, Live: live} }
	return r.serve(), snap
}

// getTools is GET /v1/tools as client: decoded strictly (no field the
// type lacks), and raw.
func getTools(t *testing.T, r *profileRig, client string) (ToolsList, string) {
	t.Helper()
	r.as(client)
	req, _ := http.NewRequest("GET", r.srv.URL+"/v1/tools", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("%s GET /v1/tools: %d %s", client, resp.StatusCode, raw)
	}
	var out ToolsList
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out, string(raw)
}

func setNames(l ToolsList) []string {
	var out []string
	for _, s := range l.Sets {
		out = append(out, s.Name)
	}
	return out
}

func setOf(t *testing.T, l ToolsList, name string) ToolSetInfo {
	t.Helper()
	for _, s := range l.Sets {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no set %s in %v", name, setNames(l))
	return ToolSetInfo{}
}

func toolStates(s ToolSetInfo) map[string]string {
	out := map[string]string{}
	for _, tl := range s.Tools {
		out[tl.Name] = tl.Status
	}
	return out
}

// Every set and tool status, with its reason.
func TestTools_StatusesAndReasons(t *testing.T) {
	r, snap := toolsRig(t, nil)
	l, _ := getTools(t, r, "cli")
	if l.Snapshot != snap.Version() || !strings.HasPrefix(l.Snapshot, "sha256:") {
		t.Fatalf("snapshot %q, want the store's %q", l.Snapshot, snap.Version())
	}

	jira := setOf(t, l, "jira:readonly")
	if jira.Source != "jira" || jira.Status != "partial" || jira.Reason != "1 of 4 tools active" || jira.URL != "http://127.0.0.1:8001/mcp" {
		t.Fatalf("jira = %+v", jira)
	}
	if got := toolStates(jira); !reflect.DeepEqual(got, map[string]string{"a": "active", "b": "changed", "c": "unpinned", "d": "unavailable"}) {
		t.Fatalf("jira tools %v", got)
	}
	if names := []string{jira.Tools[0].Name, jira.Tools[1].Name, jira.Tools[2].Name, jira.Tools[3].Name}; !reflect.DeepEqual(names, []string{"a", "b", "c", "d"}) {
		t.Fatalf("jira tools not sorted: %v", names)
	}
	a, b, c, d := jira.Tools[0], jira.Tools[1], jira.Tools[2], jira.Tools[3]
	if a.ApprovedAt == nil || !a.ApprovedAt.Equal(toolsT0) || a.Reason != "" || b.ApprovedAt != nil {
		t.Fatalf("approvedAt: a %+v b %+v", a, b)
	}
	if b.Reason != "definition differs from the approved one" || c.Reason != "never approved" || d.Reason != "not listed by the backend" {
		t.Fatalf("reasons: b %q c %q d %q", b.Reason, c.Reason, d.Reason)
	}

	slack := setOf(t, l, "slack:readonly")
	if slack.Status != "unavailable" || slack.URL != "" || slack.Tools[0].Status != "unavailable" || slack.Tools[0].Reason != "backend unavailable" {
		t.Fatalf("slack = %+v", slack)
	}
	google := setOf(t, l, "google:readonly")
	if google.Status != "unavailable" || google.Tools[0].Status != "source_changed" || google.Tools[0].Reason != "approved for a different address of source google" {
		t.Fatalf("google = %+v", google)
	}
	if gh := setOf(t, l, "github:readonly"); gh.Status != "available" || gh.Reason != "" || gh.URL != "http://localhost:8004/mcp" || gh.Tools[0].Status != "active" {
		t.Fatalf("github = %+v", gh)
	}

	// The alias resolves (wiki:readonly is notes:readonly); the gateway has
	// not listed notes, so its tools are unknown, and the pinned one still
	// counts as granted.
	wiki := setOf(t, l, "wiki:readonly")
	if wiki.Source != "notes" || wiki.URL != "" || wiki.Status != "partial" || !reflect.DeepEqual(toolStates(wiki), map[string]string{"wiki_read": "unknown", "wiki_search": "unknown"}) {
		t.Fatalf("wiki = %+v", wiki)
	}
	if !strings.Contains(wiki.Tools[0].Reason, "tool gateway") {
		t.Fatalf("unknown reason %q", wiki.Tools[0].Reason)
	}

	// Omitted sets, each with its reason and no tools.
	for name, reason := range map[string]string{
		"broken:readonly":  "tool-sources.yaml rejects this set (see errors)",
		"missing:readonly": "not in tool-sources.yaml",
		"bad":              "not a <name>:<kind> set name",
	} {
		s := setOf(t, l, name)
		if s.Status != "unavailable" || s.Reason != reason || len(s.Tools) != 0 || s.Source != "" {
			t.Errorf("%s = %+v, want unavailable %q", name, s, reason)
		}
	}
	want := []ToolsError{
		{Set: "broken:readonly", Message: `tool-sources.yaml: set "broken:readonly": source "brk" is invalid: tool-sources.yaml: source "brk": url "<url>" must be an http or https URL with a host`},
		{Source: "brk", Message: `tool-sources.yaml: source "brk": url "<url>" must be an http or https URL with a host`},
	}
	if !reflect.DeepEqual(l.Errors, want) {
		t.Fatalf("errors\n got %+v\nwant %+v", l.Errors, want)
	}
}

// A set every tool of which is unknown but pinned is available, saying it
// has not been listed live; one with none pinned and unknown is not.
func TestTools_UnknownLiveness(t *testing.T) {
	r, _ := toolsRig(t, nil)
	r.api.Tools = func() ToolsView {
		reg, pins, _ := toolsFixture(t)
		return ToolsView{Snapshot: toolreg.NewSnapshot(reg, pins, nil)} // the gateway has listed nothing
	}
	l, _ := getTools(t, r, "cli")
	if gh := setOf(t, l, "github:readonly"); gh.Status != "available" || gh.Reason != "the tool gateway has not listed it live yet" || gh.Tools[0].Status != "unknown" {
		t.Fatalf("github = %+v", gh)
	}
	if jira := setOf(t, l, "jira:readonly"); jira.Status != "partial" || toolStates(jira)["c"] != "unknown" {
		t.Fatalf("jira = %+v", jira)
	}
	// source_changed does not depend on liveness.
	if g := setOf(t, l, "google:readonly"); g.Status != "unavailable" || g.Tools[0].Status != "source_changed" {
		t.Fatalf("google = %+v", g)
	}
}

// A client sees the sets of the profiles in its scope only: Wingman and a
// device the work-readonly sets, cli also ops's github set; no client sees
// a registry set no profile requests.
func TestTools_ScopedPerClient(t *testing.T) {
	r, _ := toolsRig(t, nil)
	work := []string{"bad", "broken:readonly", "google:readonly", "jira:readonly", "missing:readonly", "slack:readonly", "wiki:readonly"}
	for client, want := range map[string][]string{
		"wingman": work,
		"device":  work,
		"cli":     {"bad", "broken:readonly", "github:readonly", "google:readonly", "jira:readonly", "missing:readonly", "slack:readonly", "wiki:readonly"},
	} {
		l, _ := getTools(t, r, client)
		if got := setNames(l); !reflect.DeepEqual(got, want) {
			t.Errorf("%s sees %v, want %v", client, got, want)
		}
	}
	// A client whose scope requests no set sees none, and no errors.
	r.api.Scopes.Clients["wingman"] = clientWith(r, "wingman", []string{"page"})
	l, _ := getTools(t, r, "wingman")
	if len(l.Sets) != 0 || len(l.Errors) != 0 || l.Sets == nil || l.Errors == nil {
		t.Fatalf("page-only wingman: %+v, want empty lists", l)
	}
}

func clientWith(r *profileRig, name string, profiles []string) clientscope.Client {
	c := r.api.Scopes.Clients[name]
	c.Profiles = profiles
	return c
}

// Per-set registry errors reach only clients whose scope requests the set.
func TestTools_ErrorsScoped(t *testing.T) {
	r, _ := toolsRig(t, nil)
	r.api.Scopes.Clients["wingman"] = clientWith(r, "wingman", []string{"page", "ops"})
	l, _ := getTools(t, r, "wingman")
	if !reflect.DeepEqual(setNames(l), []string{"github:readonly"}) || len(l.Errors) != 0 {
		t.Fatalf("ops-only wingman: sets %v errors %v", setNames(l), l.Errors)
	}
}

// A pins (or registry) load error is one top-level error and every set is
// unavailable, whatever the gateway listed: fail closed.
func TestTools_LoadErrorFailsClosed(t *testing.T) {
	r, snap := toolsRig(t, errors.New("pins /Users/someone/.aimux/tools/pins.json: invalid character 'x'"))
	for _, client := range []string{"wingman", "cli"} {
		l, raw := getTools(t, r, client)
		if l.Snapshot != snap.Version() {
			t.Fatalf("snapshot %q, want %q", l.Snapshot, snap.Version())
		}
		if len(l.Errors) != 1 || l.Errors[0].Set != "" || l.Errors[0].Source != "" || !strings.Contains(l.Errors[0].Message, "could not be loaded") {
			t.Fatalf("%s errors %+v, want one top-level error", client, l.Errors)
		}
		if strings.Contains(raw, "/Users/someone") || strings.Contains(raw, "invalid character") {
			t.Fatalf("%s: the load error's detail reached the client: %s", client, raw)
		}
		for _, s := range l.Sets {
			if s.Status != "unavailable" || s.Reason != "the tool configuration did not load (see errors)" {
				t.Errorf("%s: set %s = %s %q", client, s.Name, s.Status, s.Reason)
			}
			for _, tl := range s.Tools {
				if tl.Status != "unavailable" {
					t.Errorf("%s: %s/%s = %s", client, s.Name, tl.Name, tl.Status)
				}
			}
		}
	}
}

// No definition, schema, hash, credential or non-loopback URL reaches any
// client: source URLs only when loopback, without userinfo or query.
func TestTools_NoSecrets(t *testing.T) {
	r, _ := toolsRig(t, nil)
	for _, client := range []string{"wingman", "device", "cli"} {
		l, raw := getTools(t, r, client)
		body := strings.Replace(raw, l.Snapshot, "", 1) // the version is a hash by design
		for _, bad := range append(toolsSecrets, `"inputSchema"`, `"description"`, `"definition"`, `"hash"`, "sha256:", "token", "/notes") {
			if strings.Contains(body, bad) {
				t.Errorf("%s: response holds %q: %s", client, bad, body)
			}
		}
	}
}

// A confined client (Wingman) may call it; it stays confined elsewhere.
func TestTools_ConfinedClientAllowed(t *testing.T) {
	r, _ := toolsRig(t, nil)
	if resp, body := r.as("wingman").do("GET", "/v1/tools", ""); resp.StatusCode != 200 {
		t.Fatalf("wingman GET /v1/tools: %d %v", resp.StatusCode, body)
	}
	if resp, body := r.as("wingman").do("GET", "/v1/search?q=x", ""); resp.StatusCode != 403 || body["code"] != "forbidden_for_client" {
		t.Fatalf("wingman GET /v1/search: %d %v", resp.StatusCode, body)
	}
}

// Without the tool wiring it is 404 not_supported; unreadable profiles 500.
func TestTools_NotSupportedAndProfileErrors(t *testing.T) {
	r, _ := toolsRig(t, nil)
	r.api.Tools = nil
	if resp, body := r.as("cli").do("GET", "/v1/tools", ""); resp.StatusCode != 404 || body["code"] != "not_supported" {
		t.Fatalf("no tools: %d %v", resp.StatusCode, body)
	}
	r2, _ := toolsRig(t, nil)
	r2.api.Profiles = func() (map[string]profile.Manifest, error) { return nil, errors.New("bad dir") }
	if resp, body := r2.as("cli").do("GET", "/v1/tools", ""); resp.StatusCode != 500 || body["code"] != "internal" {
		t.Fatalf("profiles unreadable: %d %v", resp.StatusCode, body)
	}
}

// redactText keeps loopback URLs without userinfo, query or fragment and
// hides every other URL and token-shaped string.
func TestRedactText(t *testing.T) {
	for in, want := range map[string]string{
		"": "",
		"dial http://u:p@127.0.0.1:8001/mcp?t=1#f":  "dial http://127.0.0.1:8001/mcp",
		`Post "https://api.example.com/x?key=abc"`:  `Post "<url>"`,
		"http://[::1]:9/mcp ok":                     "http://[::1]:9/mcp ok",
		"http://localhost.evil.com/mcp":             "<url>",
		"tool sources /a/b/tool-sources.yaml: oops": "tool-sources.yaml: oops",
		"Bearer abcdef123":                          "Bearer [redacted]",
		"stat /Users/someone/notes: no such file":   "stat <path>: no such file",
		`root "/w/x y" missing`:                     `root "<path>" missing`,
		"a/b and 1/2 stay":                          "a/b and 1/2 stay",
	} {
		if got := redactText(in); got != want {
			t.Errorf("redactText(%q) = %q, want %q", in, got, want)
		}
	}
}

// A down backend's error never reaches a client: a fixed reason per source
// type (its detail is in remote serve's log), and no absolute path
// anywhere in the response.
func TestTools_DownBackendReasonsHideDetail(t *testing.T) {
	r, _ := toolsRig(t, nil)
	reg, pins, live := toolsFixture(t)
	live["notes"] = toolgateway.LiveSource{Err: errors.New("notes root: stat /Users/someone/notes: no such file or directory")}
	r.api.Tools = func() ToolsView { return ToolsView{Snapshot: toolreg.NewSnapshot(reg, pins, nil), Live: live} }
	for _, client := range []string{"wingman", "device", "cli"} {
		l, raw := getTools(t, r, client)
		wiki := setOf(t, l, "wiki:readonly")
		for _, tl := range wiki.Tools {
			if tl.Status != "unavailable" || tl.Reason != "notes folder not found" {
				t.Errorf("%s: wiki %s = %s %q", client, tl.Name, tl.Status, tl.Reason)
			}
		}
		if s := setOf(t, l, "slack:readonly"); s.Tools[0].Reason != "backend unavailable" {
			t.Errorf("%s: slack reason %q", client, s.Tools[0].Reason)
		}
		if absPath.MatchString(raw) || strings.Contains(raw, "someone") {
			t.Errorf("%s: an absolute path reached the client: %s", client, raw)
		}
	}
}

// absPath is an absolute file path in JSON text: a / right after a quote,
// a space or a colon-space, then a name.
var absPath = regexp.MustCompile(`(^|["\s(=])/[A-Za-z0-9._-]`)

// A source_changed reason names the source only: an endpoint (a notes
// folder with spaces in its path) never reaches a client.
func TestTools_SourceChangedNamesTheSourceOnly(t *testing.T) {
	r, _ := toolsRig(t, nil)
	reg, pins, live := toolsFixture(t)
	reg.Sources["notes"] = toolreg.Source{Type: toolreg.TypeLocalNotes, Root: "/Users/someone/My Notes/new place"}
	pins["notes"]["wiki_read"] = toolreg.Pin{Hash: "sha256:w", Source: "/Users/someone/Old Notes/secret folder"}
	r.api.Tools = func() ToolsView { return ToolsView{Snapshot: toolreg.NewSnapshot(reg, pins, nil), Live: live} }
	l, raw := getTools(t, r, "cli")
	wiki := setOf(t, l, "wiki:readonly")
	if wiki.Tools[0].Name != "wiki_read" || wiki.Tools[0].Status != "source_changed" || wiki.Tools[0].Reason != "approved for a different address of source notes" {
		t.Fatalf("wiki_read = %+v", wiki.Tools[0])
	}
	for _, bad := range []string{"someone", "Notes", "place", "secret", "folder"} {
		if strings.Contains(raw, bad) {
			t.Errorf("response holds %q: %s", bad, raw)
		}
	}
}

// A source the gateway still serves but whose last relist failed is
// unknown with that reason, not unavailable, and its pinned tools still
// count as granted.
func TestTools_RelistFailedIsUnknownNotUnavailable(t *testing.T) {
	r, _ := toolsRig(t, nil)
	r.api.Tools = func() ToolsView {
		reg, pins, live := toolsFixture(t)
		delete(live, "github")
		return ToolsView{Snapshot: toolreg.NewSnapshot(reg, pins, nil), Live: live, RelistFailed: map[string]bool{"github": true}}
	}
	l, _ := getTools(t, r, "cli")
	gh := setOf(t, l, "github:readonly")
	if gh.Status != "available" || gh.Tools[0].Status != "unknown" || gh.Tools[0].Reason != reasonRelistFailed {
		t.Fatalf("github = %+v", gh)
	}
}
