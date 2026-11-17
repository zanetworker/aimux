package toolgateway

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolreg"
)

func tool(name, qType string) json.RawMessage {
	return json.RawMessage(`{"name":"` + name + `","description":"about ` + name + `","inputSchema":{"type":"object","properties":{"q":{"type":"` + qType + `"}}}}`)
}

func canon(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	c, err := Canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func record(t *testing.T, raw json.RawMessage, at time.Time) PinRecord {
	return PinRecord{Hash: mustPin(t, string(raw)), Definition: canon(t, raw), ApprovedAt: at, Source: "http://127.0.0.1:8001/mcp"}
}

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func reviewRegistry() toolreg.Registry {
	return toolreg.Registry{
		Version: 1,
		Sources: map[string]toolreg.Source{
			"jira":  {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"},
			"slack": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:7381/mcp/slack"},
		},
		Sets: map[string]toolreg.Set{
			"jira:readonly":  {Source: "jira", Tools: []string{"same", "fresh", "drifted", "gone", "never"}},
			"slack:readonly": {Source: "slack", Tools: []string{"search"}},
		},
	}
}

// reviewFixture: same is active, fresh unpinned, drifted changed, gone
// pinned but no longer listed, never neither pinned nor listed, slack down,
// and jira/old pinned but in no set.
func reviewFixture(t *testing.T) (toolreg.Registry, PinFile, map[string]LiveSource) {
	slackRecord := record(t, tool("search", "string"), t0)
	slackRecord.Source = "http://127.0.0.1:7381/mcp/slack" // record stamps jira's URL
	pins := PinFile{
		"jira": {
			"same":    record(t, tool("same", "string"), t0),
			"drifted": record(t, tool("drifted", "string"), t0),
			"gone":    record(t, tool("gone", "string"), t0),
			"old":     record(t, tool("old", "string"), t0),
		},
		"slack": {"search": slackRecord},
	}
	live := map[string]LiveSource{
		"jira": {Tools: map[string]json.RawMessage{
			"same": tool("same", "string"), "fresh": tool("fresh", "string"), "drifted": tool("drifted", "number"), "extra": tool("extra", "string"),
		}},
		"slack": {Err: errors.New("connection refused")},
	}
	return reviewRegistry(), pins, live
}

func TestReview_EveryStatus(t *testing.T) {
	reg, pins, live := reviewFixture(t)
	got := map[string]ToolStatus{}
	var order []string
	for _, s := range Review(reg, pins, live) {
		k := s.Set + "/" + s.Tool
		got[k] = s
		order = append(order, k)
	}
	want := map[string]string{
		"jira:readonly/same":    StatusActive,
		"jira:readonly/fresh":   StatusUnpinned,
		"jira:readonly/drifted": StatusChanged,
		"jira:readonly/gone":    StatusChanged,
		"jira:readonly/never":   StatusUnavailable,
		"slack:readonly/search": StatusUnavailable,
		"/old":                  StatusNotInRegistry,
		"/extra":                StatusNewOnServer,
	}
	for k, st := range want {
		if got[k].Status != st {
			t.Errorf("%s = %q (%s), want %q", k, got[k].Status, got[k].Reason, st)
		}
	}
	if len(got) != len(want) {
		t.Errorf("statuses = %v", order)
	}
	if !strings.Contains(got["slack:readonly/search"].Reason, "connection refused") {
		t.Errorf("down reason = %q", got["slack:readonly/search"].Reason)
	}
	if !strings.Contains(got["jira:readonly/gone"].Reason, "no longer listed") {
		t.Errorf("gone reason = %q", got["jira:readonly/gone"].Reason)
	}
	if a := got["jira:readonly/same"].ApprovedAt; a == nil || !a.Equal(t0) {
		t.Errorf("approved_at = %v", a)
	}
	if got["jira:readonly/fresh"].ApprovedAt != nil {
		t.Error("unpinned tool has an approval time")
	}
	if got["/old"].Source != "jira" {
		t.Errorf("not_in_registry source = %q", got["/old"].Source)
	}
	if r := got["/extra"]; r.Source != "jira" || r.Reason != "offered by jira, not in any set" || r.ApprovedAt != nil {
		t.Errorf("new_on_server row = %+v", r)
	}
	wantOrder := []string{"jira:readonly/drifted", "jira:readonly/fresh", "jira:readonly/gone", "jira:readonly/never", "jira:readonly/same", "slack:readonly/search", "/old", "/extra"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("order = %v, want %v", order, wantOrder)
	}
}

// A v1 pin with a matching hash is active; it has no approval time.
func TestReview_V1PinIsActive(t *testing.T) {
	reg := reviewRegistry()
	pins := PinFile{"jira": {"same": {Hash: mustPin(t, string(tool("same", "string")))}}}
	live := map[string]LiveSource{"jira": {Tools: map[string]json.RawMessage{"same": tool("same", "string")}}}
	for _, s := range Review(reg, pins, live) {
		if s.Tool == "same" && (s.Status != StatusActive || s.ApprovedAt != nil) {
			t.Fatalf("v1 pin = %#v", s)
		}
	}
}

func TestDiff_Kinds(t *testing.T) {
	reg, pins, live := reviewFixture(t)
	got := map[string]Change{}
	for _, c := range Diff(reg, pins, live) {
		got[c.Set+"/"+c.Tool] = c
	}
	if len(got) != 3 {
		t.Fatalf("changes = %v", got)
	}
	if c := got["jira:readonly/fresh"]; c.Kind != KindAdded || c.Before != nil || c.After == nil {
		t.Errorf("fresh = %#v", c)
	}
	if c := got["jira:readonly/gone"]; c.Kind != KindRemoved || c.After != nil || c.Before == nil {
		t.Errorf("gone = %#v", c)
	}
	c := got["jira:readonly/drifted"]
	if c.Kind != KindChanged {
		t.Fatalf("drifted = %#v", c)
	}
	want := []FieldChange{{Path: "inputSchema.properties.q.type", Before: `"string"`, After: `"number"`}}
	if !reflect.DeepEqual(c.Fields, want) {
		t.Errorf("fields = %#v, want %#v", c.Fields, want)
	}
	if strings.Contains(string(c.After), "description") {
		t.Errorf("after is not canonical: %s", c.After)
	}
}

func TestDiff_V1PinShowsLiveOnly(t *testing.T) {
	reg := reviewRegistry()
	pins := PinFile{"jira": {"drifted": {Hash: "sha256:old"}}}
	live := map[string]LiveSource{"jira": {Tools: map[string]json.RawMessage{"drifted": tool("drifted", "number")}}}
	var c *Change
	for _, ch := range Diff(reg, pins, live) {
		if ch.Tool == "drifted" {
			c = &ch
		}
	}
	if c == nil || c.Kind != KindChanged || c.Before != nil || c.After == nil || c.Fields != nil {
		t.Fatalf("v1 change = %#v", c)
	}
	if c.Note != NoteNoDefinition {
		t.Errorf("note = %q", c.Note)
	}
}

func TestFieldDiff_AddedRemovedArraysDeterministic(t *testing.T) {
	before := json.RawMessage(`{"name":"t","inputSchema":{"required":["a","b"],"properties":{"a":{"type":"string"},"z":{"type":"string"}}}}`)
	after := json.RawMessage(`{"name":"t","inputSchema":{"required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"number"}}},"annotations":{"readOnlyHint":true}}`)
	want := []FieldChange{
		{Path: "annotations", After: `{"readOnlyHint":true}`},
		{Path: "inputSchema.properties.b", After: `{"type":"number"}`},
		{Path: "inputSchema.properties.z", Before: `{"type":"string"}`},
		{Path: "inputSchema.required[1]", Before: `"b"`},
	}
	for i := 0; i < 5; i++ {
		got, err := FieldDiff(before, after)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("FieldDiff = %#v\nwant %#v", got, want)
		}
	}
	if got, _ := FieldDiff(before, before); len(got) != 0 {
		t.Errorf("identical = %#v", got)
	}
}

func TestApprove_OnlyNamedToolAndIdempotent(t *testing.T) {
	reg, pins, live := reviewFixture(t)
	now := t0.Add(time.Hour)
	out, res, err := Approve(reg, pins, live, []string{"jira:readonly/drifted"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionApproved {
		t.Fatalf("results = %#v", res)
	}
	r := out["jira"]["drifted"]
	if r.Hash != mustPin(t, string(tool("drifted", "number"))) || !r.ApprovedAt.Equal(now) || r.Source != "http://127.0.0.1:8001/mcp" || string(r.Definition) != string(canon(t, tool("drifted", "number"))) {
		t.Fatalf("drifted = %#v", r)
	}
	if _, ok := out["jira"]["fresh"]; ok {
		t.Error("approved a tool that was not named")
	}
	if pins["jira"]["drifted"].ApprovedAt != t0 {
		t.Error("Approve changed its input")
	}

	again, res, err := Approve(reg, out, live, []string{"jira:readonly/drifted"}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionUnchanged {
		t.Fatalf("second results = %#v", res)
	}
	if !again["jira"]["drifted"].ApprovedAt.Equal(now) {
		t.Errorf("idempotent approve moved approved_at to %v", again["jira"]["drifted"].ApprovedAt)
	}
	if !reflect.DeepEqual(again, out) {
		t.Error("second approve changed the pins")
	}
}

func TestApprove_SetUpgradesV1RemovesGoneSkipsDown(t *testing.T) {
	reg, pins, live := reviewFixture(t)
	pins["jira"]["same"] = PinRecord{Hash: mustPin(t, string(tool("same", "string")))} // v1
	now := t0.Add(time.Hour)
	out, res, err := Approve(reg, pins, live, []string{"jira:readonly", "slack:readonly"}, now)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, r := range res {
		actions[r.Set+"/"+r.Tool] = r.Action
	}
	want := map[string]string{
		"jira:readonly/same":    ActionUpgraded,
		"jira:readonly/fresh":   ActionApproved,
		"jira:readonly/drifted": ActionApproved,
		"jira:readonly/gone":    ActionRemoved,
		"jira:readonly/never":   ActionSkipped,
		"slack:readonly/search": ActionSkipped,
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions = %v", actions)
	}
	if s := out["jira"]["same"]; s.Definition == nil || !s.ApprovedAt.Equal(now) {
		t.Errorf("v1 pin not upgraded: %#v", s)
	}
	if _, ok := out["jira"]["gone"]; ok {
		t.Error("removed tool kept its pin")
	}
	if !reflect.DeepEqual(out["slack"], pins["slack"]) || !reflect.DeepEqual(out["jira"]["old"], pins["jira"]["old"]) {
		t.Error("pins outside the approved tools changed")
	}
}

func TestApprove_BadTargets(t *testing.T) {
	reg, pins, live := reviewFixture(t)
	for _, target := range []string{"nope:readonly", "jira:readonly/extra", "jira:readonly/", ""} {
		if _, _, err := Approve(reg, pins, live, []string{target}, t0); err == nil {
			t.Errorf("Approve(%q) = nil error", target)
		}
	}
}

func TestLiveFromCatalog(t *testing.T) {
	cat := &mcpproxy.Catalog{Servers: map[string]mcpproxy.CatalogEntry{}}
	cat.Set("jira", []json.RawMessage{tool("a", "string"), json.RawMessage(`{"bad`)}, t0)
	live := LiveFromCatalog(cat, map[string]error{"slack": errors.New("down")})
	if _, ok := live["jira"].Tools["a"]; !ok || len(live["jira"].Tools) != 1 || live["jira"].Err != nil {
		t.Errorf("jira = %#v", live["jira"])
	}
	if live["slack"].Err == nil {
		t.Errorf("slack = %#v", live["slack"])
	}
}

// A key that is not a plain name is quoted, so a "." cannot look like
// nesting and a newline cannot start a fake line.
func TestFieldDiff_QuotesOddKeys(t *testing.T) {
	before := json.RawMessage(`{"properties":{"x.y":{"type":"string"},"a\n+ fake: line":1}}`)
	after := json.RawMessage(`{"properties":{"x.y":{"type":"number"},"a\n+ fake: line":2}}`)
	got, err := FieldDiff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []FieldChange{
		{Path: `properties["a\n+ fake: line"]`, Before: "1", After: "2"},
		{Path: `properties["x.y"].type`, Before: `"string"`, After: `"number"`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FieldDiff = %#v\nwant %#v", got, want)
	}
	for _, f := range got {
		if strings.ContainsAny(f.Path, "\n\r") {
			t.Errorf("path has a raw newline: %q", f.Path)
		}
	}
}

// sourceFixture: jira's URL moved after "moved" was approved; "v1" has no
// recorded source (unknown), "same" was approved from the current URL.
func sourceFixture(t *testing.T) (toolreg.Registry, PinFile, map[string]LiveSource) {
	reg := reviewRegistry()
	reg.Sets["jira:readonly"] = toolreg.Set{Source: "jira", Tools: []string{"moved", "v1", "same"}}
	moved := record(t, tool("moved", "string"), t0)
	moved.Source = "http://127.0.0.1:7000/mcp"
	pins := PinFile{"jira": {
		"moved": moved,
		"v1":    {Hash: mustPin(t, string(tool("v1", "string")))},
		"same":  record(t, tool("same", "string"), t0),
	}}
	live := map[string]LiveSource{"jira": {Tools: map[string]json.RawMessage{
		"moved": tool("moved", "string"), "v1": tool("v1", "string"), "same": tool("same", "string"),
	}}}
	return reg, pins, live
}

func TestReview_SourceChanged(t *testing.T) {
	reg, pins, live := sourceFixture(t)
	got := map[string]ToolStatus{}
	for _, s := range Review(reg, pins, live) {
		got[s.Tool] = s
	}
	if s := got["moved"]; s.Status != StatusSourceChanged || !strings.Contains(s.Reason, "http://127.0.0.1:7000/mcp") || !s.NeedsReview() {
		t.Errorf("moved = %+v, want source_changed naming the old URL", s)
	}
	for _, n := range []string{"v1", "same"} {
		if got[n].Status != StatusActive {
			t.Errorf("%s = %+v, want active (an empty source is unknown)", n, got[n])
		}
	}
	// The backend being down does not hide that the source moved.
	live["jira"] = LiveSource{Err: errors.New("refused")}
	for _, s := range Review(reg, pins, live) {
		if s.Tool == "moved" && s.Status != StatusSourceChanged {
			t.Errorf("down: moved = %+v, want source_changed", s)
		}
	}
}

// Approving a source_changed tool re-stamps its source with the current
// URL (and a new time) even though its definition is the same; the
// unaffected tools are left as they are.
func TestApprove_RestampsChangedSource(t *testing.T) {
	reg, pins, live := sourceFixture(t)
	now := t0.Add(time.Hour)
	out, res, err := Approve(reg, pins, live, []string{"jira:readonly"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if r := out["jira"]["moved"]; r.Source != "http://127.0.0.1:8001/mcp" || !r.ApprovedAt.Equal(now) {
		t.Errorf("moved = %+v, want re-stamped", r)
	}
	acts := map[string]string{}
	for _, a := range res {
		acts[a.Tool] = a.Action
	}
	if acts["moved"] != ActionApproved || acts["same"] != ActionUnchanged {
		t.Errorf("actions = %v", acts)
	}
	if st := Review(reg, out, live); st[0].Status != StatusActive {
		t.Errorf("after approve: %+v", st)
	}
	// Down backend: the moved tool is skipped, never dropped.
	live["jira"] = LiveSource{Err: errors.New("refused")}
	out, res, err = Approve(reg, pins, live, []string{"jira:readonly/moved"}, now)
	if err != nil || res[0].Action != ActionSkipped || out["jira"]["moved"].Hash == "" {
		t.Errorf("down: res=%+v err=%v pin=%+v", res, err, out["jira"]["moved"])
	}
}

func TestDiff_SourceChangedNamesBothEndpoints(t *testing.T) {
	reg, pins, live := sourceFixture(t)
	d := Diff(reg, pins, live)
	if len(d) != 1 || d[0].Tool != "moved" || d[0].Kind != KindChanged ||
		!strings.Contains(d[0].Note, "http://127.0.0.1:7000/mcp") || !strings.Contains(d[0].Note, "http://127.0.0.1:8001/mcp") {
		t.Fatalf("diff = %+v", d)
	}
}

// A tool the server offers that no set lists is new_on_server only while
// no pin covers it (a pinned one is not_in_registry), and never for a
// source the registry does not name.
func TestReview_NewOnServerSkipsPinnedAndUnknownSources(t *testing.T) {
	reg, pins, live := reviewFixture(t)
	pins["jira"]["extra"] = record(t, tool("extra", "string"), t0)
	live["ghost"] = LiveSource{Tools: map[string]json.RawMessage{"x": tool("x", "string")}}
	for _, s := range Review(reg, pins, live) {
		if s.Status == StatusNewOnServer {
			t.Fatalf("unexpected new_on_server row %+v", s)
		}
	}
}
