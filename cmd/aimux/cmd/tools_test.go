package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

var toolsNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// toolsRig is a home with a registry (jira: search, get; slack: history)
// and a live catalog the test controls.
type toolsRig struct {
	env  toolsEnv
	live map[string][]json.RawMessage
	down map[string]error
}

func newToolsRig(t *testing.T) *toolsRig {
	t.Helper()
	r := &toolsRig{
		live: map[string][]json.RawMessage{"jira": {rawTool("search", "string"), rawTool("get", "string")}},
		down: map[string]error{"slack": errors.New("connection refused")},
	}
	r.env = toolsEnv{Home: t.TempDir(), Now: func() time.Time { return toolsNow }}
	r.env.Catalog = func(ctx context.Context, cfg mcpproxy.ReadonlyConfig) (*mcpproxy.Catalog, map[string]error) {
		return catalogFake(r.live, r.down)(ctx, cfg)
	}
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{
			"jira":  {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"},
			"slack": {Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:7381/mcp/slack"},
		},
		Sets: map[string]toolreg.Set{
			"jira:readonly":  {Source: "jira", Tools: []string{"search", "get"}},
			"slack:readonly": {Source: "slack", Tools: []string{"history"}},
		}}
	if err := toolreg.WriteNew(toolreg.DefaultPath(r.env.Home), reg); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *toolsRig) pinsPath() string { return toolgateway.DefaultPinsPath(r.env.Home) }

// writeV1 seeds a v1 pins file.
func (r *toolsRig) writeV1(t *testing.T, p toolgateway.Pins) {
	t.Helper()
	data, _ := json.Marshal(p)
	if err := os.MkdirAll(filepath.Dir(r.pinsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.pinsPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pinOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	p, err := toolgateway.PinTool(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func runTools(t *testing.T, env toolsEnv, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	root := &cobra.Command{Use: "aimux", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
	defer func() { jsonOutput = false }()
	root.AddCommand(newToolsCmdWith(env))
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"tools"}, args...))
	err = root.Execute()
	return out.String(), errb.String(), err
}

func TestToolsStatus_TextShowsEveryToolAndHintsWithoutWriting(t *testing.T) {
	r := newToolsRig(t)
	r.writeV1(t, toolgateway.Pins{"jira": {"search": pinOf(t, rawTool("search", "string")), "get": "sha256:old", "gone": "sha256:x"}})
	before, _ := os.ReadFile(r.pinsPath())
	out, errOut, err := runTools(t, r.env, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"jira:readonly", "search", "active", "get", "changed", "slack:readonly", "history", "unavailable", "connection refused", "gone", "not_in_registry"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "Next: aimux tools diff") || !strings.Contains(errOut, "aimux tools approve jira:readonly") {
		t.Errorf("stderr lacks the Next hint:\n%s", errOut)
	}
	after, _ := os.ReadFile(r.pinsPath())
	if !bytes.Equal(before, after) {
		t.Error("status changed pins.json")
	}
}

// status --json is a stable schema: the exact keys, one entry per tool.
func TestToolsStatus_JSONSchema(t *testing.T) {
	r := newToolsRig(t)
	approve := toolgateway.PinFile{"jira": {"search": {Hash: pinOf(t, rawTool("search", "string")), ApprovedAt: toolsNow}}}
	if err := (toolgateway.PinStore{Path: r.pinsPath()}).Save(approve); err != nil {
		t.Fatal(err)
	}
	out, _, err := runTools(t, r.env, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if keys := slices.Sorted(maps.Keys(got)); !reflect.DeepEqual(keys, []string{"needs_review", "registry_errors", "schema_version", "tools"}) {
		t.Fatalf("top-level keys = %v", keys)
	}
	var doc struct {
		SchemaVersion string                   `json:"schema_version"`
		NeedsReview   int                      `json:"needs_review"`
		Tools         []map[string]interface{} `json:"tools"`
	}
	_ = json.Unmarshal([]byte(out), &doc)
	if doc.SchemaVersion != "1" || doc.NeedsReview != 1 || len(doc.Tools) != 3 {
		t.Fatalf("doc = %+v", doc)
	}
	first := doc.Tools[0] // jira:readonly/get, unpinned
	if first["set"] != "jira:readonly" || first["source"] != "jira" || first["tool"] != "get" || first["status"] != "unpinned" {
		t.Errorf("first = %v", first)
	}
	if doc.Tools[1]["approved_at"] != "2026-11-15T12:00:00Z" {
		t.Errorf("approved_at = %v", doc.Tools[1]["approved_at"])
	}
}

// With only readonly.json (not migrated yet), status reads it and writes
// nothing: no registry, the old file keeps its name.
func TestToolsStatus_LegacyConfigIsReadNotMigrated(t *testing.T) {
	env := toolsEnv{Home: t.TempDir(), Now: time.Now, Catalog: catalogFake(map[string][]json.RawMessage{"jira": {rawTool("search", "string")}}, nil)}
	legacy := toolreg.LegacyPath(env.Home)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"HTTP":{"jira":{"URL":"http://127.0.0.1:8001/mcp"}},"Allow":{"jira":["search"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runTools(t, env, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "jira:readonly") || !strings.Contains(out, "unpinned") {
		t.Errorf("status:\n%s", out)
	}
	if !strings.Contains(errOut, "not migrated yet") {
		t.Errorf("stderr lacks the migration note:\n%s", errOut)
	}
	if _, err := os.Stat(toolreg.DefaultPath(env.Home)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("status wrote the registry: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("status moved readonly.json: %v", err)
	}
}

func TestToolsDiff_FieldLevelAndV1Note(t *testing.T) {
	r := newToolsRig(t)
	pins := toolgateway.PinFile{"jira": {"search": {Hash: "sha256:v1"}}}
	if err := (toolgateway.PinStore{Path: r.pinsPath()}).Save(pins); err != nil {
		t.Fatal(err)
	}
	// First approve search as live, then change its input schema.
	if _, _, err := runTools(t, r.env, "approve", "jira:readonly/search"); err != nil {
		t.Fatal(err)
	}
	r.live["jira"] = []json.RawMessage{rawTool("search", "number"), rawTool("get", "string")}
	out, _, err := runTools(t, r.env, "diff")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"changed jira:readonly/search", `inputSchema.properties.q.type: "string" -> "number"`, "added jira:readonly/get"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff lacks %q:\n%s", want, out)
		}
	}
	only, _, err := runTools(t, r.env, "diff", "jira:readonly/get")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(only, "search") || !strings.Contains(only, "added jira:readonly/get") {
		t.Errorf("filtered diff:\n%s", only)
	}

	r2 := newToolsRig(t)
	r2.writeV1(t, toolgateway.Pins{"jira": {"search": "sha256:old"}})
	v1, _, err := runTools(t, r2.env, "diff", "jira:readonly")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v1, toolgateway.NoteNoDefinition) || !strings.Contains(v1, `"inputSchema"`) {
		t.Errorf("v1 diff lacks the note or the live definition:\n%s", v1)
	}
}

func TestToolsDiff_JSONAndUnknownTarget(t *testing.T) {
	r := newToolsRig(t)
	out, _, err := runTools(t, r.env, "diff", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion string               `json:"schema_version"`
		Changes       []toolgateway.Change `json:"changes"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.SchemaVersion != "1" || len(doc.Changes) != 2 {
		t.Fatalf("diff json (%v): %s", err, out)
	}
	if c := doc.Changes[0]; c.Kind != "added" || c.Tool != "get" || c.After == nil {
		t.Errorf("change = %+v", c)
	}
	if _, _, err := runTools(t, r.env, "diff", "nope:readonly"); err == nil {
		t.Error("diff of an unknown set succeeded")
	}
}

func TestToolsApprove_NamedOnlyIdempotentWithDisclaimer(t *testing.T) {
	r := newToolsRig(t)
	out, _, err := runTools(t, r.env, "approve", "jira:readonly/search")
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	if !strings.Contains(out, toolgateway.Disclaimer) || !strings.Contains(out, "approved jira:readonly/search") {
		t.Errorf("approve output:\n%s", out)
	}
	fi, err := os.Stat(r.pinsPath())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pins file %v %v", fi, err)
	}
	f, _ := (toolgateway.PinStore{Path: r.pinsPath()}).LoadFile()
	rec := f["jira"]["search"]
	if rec.Hash != pinOf(t, rawTool("search", "string")) || !rec.ApprovedAt.Equal(toolsNow) || rec.Source != "http://127.0.0.1:8001/mcp" || rec.Definition == nil {
		t.Fatalf("record = %+v", rec)
	}
	if _, ok := f["jira"]["get"]; ok {
		t.Error("approved a tool that was not named")
	}
	before, _ := os.ReadFile(r.pinsPath())
	beforeFI, _ := os.Stat(r.pinsPath())

	r.env.Now = func() time.Time { return toolsNow.Add(time.Hour) }
	out, _, err = runTools(t, r.env, "approve", "jira:readonly/search")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unchanged jira:readonly/search") || !strings.Contains(out, "Nothing to write") {
		t.Errorf("second approve:\n%s", out)
	}
	after, _ := os.ReadFile(r.pinsPath())
	afterFI, _ := os.Stat(r.pinsPath())
	if !bytes.Equal(before, after) || !afterFI.ModTime().Equal(beforeFI.ModTime()) {
		t.Error("an approve with nothing changed rewrote pins.json")
	}
}

// Approving any tool rewrites a v1 file as v2, keeping every other pin.
func TestToolsApprove_UpgradesV1File(t *testing.T) {
	r := newToolsRig(t)
	r.writeV1(t, toolgateway.Pins{"jira": {"search": pinOf(t, rawTool("search", "string"))}, "slack": {"history": "sha256:h"}})
	out, _, err := runTools(t, r.env, "approve", "jira:readonly/search")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "upgraded jira:readonly/search") {
		t.Errorf("approve output:\n%s", out)
	}
	data, _ := os.ReadFile(r.pinsPath())
	if !strings.Contains(string(data), `"version": 2`) {
		t.Fatalf("not v2:\n%s", data)
	}
	f, _ := (toolgateway.PinStore{Path: r.pinsPath()}).LoadFile()
	if f["slack"]["history"].Hash != "sha256:h" || f["jira"]["search"].Definition == nil {
		t.Errorf("pins = %+v", f)
	}
}

// --all approves what it can; a tool whose backend is down is skipped, the
// rest are written, and the command fails naming it.
func TestToolsApprove_AllSkipsDownBackendAndFails(t *testing.T) {
	r := newToolsRig(t)
	out, errOut, err := runTools(t, r.env, "approve", "--all")
	if err == nil || !strings.Contains(err.Error(), "slack:readonly/history") {
		t.Fatalf("approve --all err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipped slack:readonly/history") || !strings.Contains(errOut, "Next:") {
		t.Errorf("out:\n%s\nerr:\n%s", out, errOut)
	}
	f, _ := (toolgateway.PinStore{Path: r.pinsPath()}).LoadFile()
	if f["jira"]["search"].Hash == "" || f["jira"]["get"].Hash == "" {
		t.Errorf("jira not approved: %+v", f)
	}
	if _, _, err := runTools(t, r.env, "approve"); err == nil {
		t.Error("approve with no target succeeded")
	}
	if _, _, err := runTools(t, r.env, "approve", "--all", "jira:readonly"); err == nil {
		t.Error("approve with --all and a target succeeded")
	}
}

func TestToolsApprove_JSON(t *testing.T) {
	r := newToolsRig(t)
	out, _, err := runTools(t, r.env, "approve", "jira:readonly", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion string                 `json:"schema_version"`
		Results       []toolgateway.Approval `json:"results"`
		Written       bool                   `json:"written"`
		Note          string                 `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if doc.SchemaVersion != "1" || !doc.Written || len(doc.Results) != 2 || doc.Note != toolgateway.Disclaimer {
		t.Fatalf("doc = %+v", doc)
	}
}

// The diff printer never lets a key forge a line of its own.
func TestToolsDiff_OddKeyCannotInjectLines(t *testing.T) {
	r := newToolsRig(t)
	odd := json.RawMessage(`{"name":"search","inputSchema":{"properties":{"x\n+ fake: line":{"type":"string"}}}}`)
	r.live["jira"] = []json.RawMessage{odd, rawTool("get", "string")}
	if _, _, err := runTools(t, r.env, "approve", "jira:readonly/search"); err != nil {
		t.Fatal(err)
	}
	r.live["jira"][0] = json.RawMessage(`{"name":"search","inputSchema":{"properties":{"x\n+ fake: line":{"type":"number"}}}}`)
	out, _, err := runTools(t, r.env, "diff", "jira:readonly/search")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "+ fake") {
			t.Fatalf("diff printed an injected line:\n%s", out)
		}
	}
	if !strings.Contains(out, `inputSchema.properties["x\n+ fake: line"].type: "string" -> "number"`) {
		t.Errorf("diff:\n%s", out)
	}
}

// A source re-pointed after approval shows as source_changed with the
// approve hint; approving re-stamps the source, and status is active again.
func TestToolsStatus_SourceChangedThenApproveRestamps(t *testing.T) {
	r := newToolsRig(t)
	if _, err := runToolsOK(t, r.env, "approve", "jira:readonly"); err != nil {
		t.Fatal(err)
	}
	reg, err := toolreg.Load(toolreg.DefaultPath(r.env.Home))
	if err != nil {
		t.Fatal(err)
	}
	reg.Sources["jira"] = toolreg.Source{Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:9001/mcp"}
	data, _ := toolreg.Marshal(reg)
	if err := os.WriteFile(toolreg.DefaultPath(r.env.Home), data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runTools(t, r.env, "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, toolgateway.StatusSourceChanged) != 2 || !strings.Contains(out, "http://127.0.0.1:8001/mcp") || !strings.Contains(errOut, "aimux tools approve jira:readonly") {
		t.Fatalf("status:\n%s\n%s", out, errOut)
	}
	if _, err := runToolsOK(t, r.env, "approve", "jira:readonly"); err != nil {
		t.Fatal(err)
	}
	f, _ := toolgateway.PinStore{Path: r.pinsPath()}.LoadFile()
	if f["jira"]["search"].Source != "http://127.0.0.1:9001/mcp" {
		t.Fatalf("source not re-stamped: %+v", f["jira"]["search"])
	}
	if out, _, _ := runTools(t, r.env, "status"); strings.Contains(out, toolgateway.StatusSourceChanged) {
		t.Fatalf("still source_changed after approve:\n%s", out)
	}
}

func runToolsOK(t *testing.T, env toolsEnv, args ...string) (string, error) {
	t.Helper()
	out, _, err := runTools(t, env, args...)
	return out, err
}

// A pins file that does not read is reported by status, naming the file,
// and said to fail closed.
func TestToolsStatus_BrokenPinsIsReported(t *testing.T) {
	r := newToolsRig(t)
	if err := os.MkdirAll(filepath.Dir(r.pinsPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.pinsPath(), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runTools(t, r.env, "status")
	if err == nil || !strings.Contains(err.Error(), r.pinsPath()) || !strings.Contains(err.Error(), "no tool is granted or served") {
		t.Fatalf("err = %v (stderr %q), want the pins file named and fail closed said", err, errOut)
	}
}

// A pending migration must not corrupt --json output: its messages go to
// stderr and stdout stays one JSON document.
func TestToolsAdd_JSONWithLegacyConfigStaysJSON(t *testing.T) {
	env := toolsEnv{Home: t.TempDir(), Now: time.Now, Catalog: catalogFake(map[string][]json.RawMessage{"jira": {rawTool("search", "string")}}, nil)}
	legacy := toolreg.LegacyPath(env.Home)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"HTTP":{"jira":{"URL":"http://127.0.0.1:8001/mcp"}},"Allow":{"jira":["search"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runTools(t, env, "add", "notes", "--folder", t.TempDir(), "--json")
	if err != nil {
		t.Fatalf("add: %v\nstderr: %s", err, errOut)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if !strings.Contains(errOut, "Migrated") {
		t.Errorf("migration message missing from stderr:\n%s", errOut)
	}
}

func TestToolsStatus_ClassColumn(t *testing.T) {
	r := newToolsRig(t)
	out, _, err := runTools(t, r.env, "status")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if f := strings.Fields(lines[0]); !reflect.DeepEqual(f, []string{"SET", "TOOL", "STATUS", "CLASS", "APPROVED", "REASON"}) {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(out, "outside") {
		t.Fatalf("no outside row:\n%s", out)
	}
	js, _, err := runTools(t, r.env, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools []map[string]interface{} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil || len(doc.Tools) == 0 {
		t.Fatalf("json: %v %s", err, js)
	}
	for _, tl := range doc.Tools {
		if tl["authorship"] != "outside" {
			t.Errorf("tool %v authorship = %v", tl["tool"], tl["authorship"])
		}
	}
}

// A cli source is not started yet: status says why for its set's tools.
func TestToolsStatus_RunnerSourceSaysNotStarted(t *testing.T) {
	r := newToolsRig(t)
	r.env.Catalog = mcpproxy.ReadonlyCatalog
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{"gh": {Type: toolreg.TypeCLI, Pack: "gh"}},
		Sets:    map[string]toolreg.Set{"gh:readonly": {Source: "gh", Tools: []string{"issue_list"}}}}
	data, err := toolreg.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolreg.DefaultPath(r.env.Home), data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := runTools(t, r.env, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "gh:readonly") || !strings.Contains(out, "runner sources are not started yet") {
		t.Errorf("status lacks the reason:\n%s", out)
	}
}
