package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// frontMachine is the machine setup and doctor probe: podman, the OpenShell
// gateway and provider, the sandboxes, running processes and port 7390.
type frontMachine struct {
	imagePresent, providerPresent, gatewayUp bool
	sandboxes                                []string
	ps                                       string // ps -axww -o pid=,lstart=,command= output
	lsof                                     string // lsof -t output; "" means nothing listens (exit 1)
	listErr                                  error  // sandbox list fails while status works
}

func (m *frontMachine) answer(argv []string) (string, error) {
	s := strings.Join(argv, " ")
	switch {
	case strings.HasPrefix(s, "podman image exists"):
		if m.imagePresent {
			return "", nil
		}
		return "", exitCodeErr{1, ""}
	case strings.HasPrefix(s, "podman build"):
		m.imagePresent = true
		return "", nil
	case s == "openshell -g local status":
		if !m.gatewayUp {
			return "", exitCodeErr{1, "Error: connection refused"}
		}
		return "Status: Connected\n", nil
	case strings.HasPrefix(s, "openshell -g local provider list-profiles"):
		if m.providerPresent {
			return "NAME  TYPE  CATEGORY\nclaude-code-oauth  provider  AGENT\n", nil
		}
		return "NAME  TYPE  CATEGORY\n", nil
	case s == "openshell -g local provider list":
		if m.providerPresent {
			return providersWithWingman, nil
		}
		return "NAME  TYPE  CREDENTIAL_KEYS\n", nil
	case strings.HasPrefix(s, "openshell -g local provider create"):
		m.providerPresent = true
		return "", nil
	case s == "openshell -g local sandbox list":
		if m.listErr != nil {
			return "", m.listErr
		}
		out := "NAME  CREATED  PHASE\n"
		for _, n := range m.sandboxes {
			out += n + "  2026-11-11 20:10:09  Ready\n"
		}
		return out, nil
	case strings.HasPrefix(s, "security find-generic-password"):
		return "sk-ant-oat01-FRONTSECRET\n", nil
	case strings.HasPrefix(s, "ps "):
		return m.ps, nil
	case strings.HasPrefix(s, "lsof "):
		if m.lsof == "" {
			return "", exitCodeErr{1, ""}
		}
		return m.lsof, nil
	}
	return "", nil
}

// readOnlyCommands are the only commands a run that changes nothing may run.
var readOnlyCommands = []string{"podman image exists", "openshell -g local status", "openshell -g local provider list",
	"openshell -g local provider list-profiles", "openshell -g local sandbox list", "ps -axww", "lsof -nP"}

func assertOnlyReadOnly(t *testing.T, f *fakeRun) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		s := strings.Join(c.argv, " ")
		ok := false
		for _, a := range readOnlyCommands {
			if s == a || strings.HasPrefix(s, a+" ") {
				ok = true
			}
		}
		if !ok {
			t.Errorf("ran %q", s)
		}
	}
}

// frontRig is a healthy home and machine: registry and pins for the four
// work sets, clients.yaml, current sandboxes, the router key in the
// Keychain and the Wingman host installed. f records every command.
type frontRig struct {
	env      frontEnv
	f        *fakeRun
	m        *frontMachine
	live     map[string][]json.RawMessage
	down     map[string]error
	keyFound bool
	tty      bool
	input    string
}

var frontNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func frontRegistry(home string) toolreg.Registry {
	reg := toolreg.Registry{Version: 1, Sources: map[string]toolreg.Source{"wiki": {Type: toolreg.TypeLocalNotes, Root: home}},
		Sets: map[string]toolreg.Set{"wiki:readonly": {Source: "wiki", Tools: []string{"wiki_search"}}}}
	for n, tool := range map[string]string{"jira": "search", "slack": "history", "google": "get_doc"} {
		reg.Sources[n] = toolreg.Source{Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:9/mcp/" + n}
		reg.Sets[n+":readonly"] = toolreg.Set{Source: n, Tools: []string{tool}}
	}
	return reg
}

func newFrontRig(t *testing.T) *frontRig {
	t.Helper()
	m := &frontMachine{imagePresent: true, providerPresent: true, gatewayUp: true}
	f := &fakeRun{answer: m.answer}
	be := testEnv(t, f)
	r := &frontRig{f: f, m: m, keyFound: true,
		live: map[string][]json.RawMessage{"wiki": {rawTool("wiki_search", "string")}, "jira": {rawTool("search", "string")},
			"slack": {rawTool("history", "string")}, "google": {rawTool("get_doc", "string")}}}
	be.Catalog = func(ctx context.Context, cfg mcpproxy.ReadonlyConfig) (*mcpproxy.Catalog, map[string]error) {
		return catalogFake(r.live, r.down)(ctx, cfg)
	}
	be.AutoKey = keyProbe{Exists: func(string) bool { return r.keyFound }}
	be.Health = func(context.Context) error { m.gatewayUp = true; return nil }
	r.env = frontEnv{browserEnv: be, GOOS: "darwin", Now: func() time.Time { return frontNow }, ProfileDir: t.TempDir(),
		IsTTY: func() bool { return r.tty }, In: func() io.Reader { return strings.NewReader(r.input) },
		Gateway: fakeGatewayStatus{st: toolgateway.ControlStatus{Leases: 0}}}
	home := be.Home

	if err := toolreg.WriteNew(toolreg.DefaultPath(home), frontRegistry(home)); err != nil {
		t.Fatal(err)
	}
	r.approveAll(t)
	if err := writeDefaultClients(home, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	// The binary host.sh execs, at a path with a space in it.
	installed := rigBinary(home)
	be.Executable = func() (string, error) { return installed, nil }
	r.env.browserEnv = be
	if err := browserInstall(context.Background(), be, testExtID, nil, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(installed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("binary"), 0o700); err != nil { //nolint:gosec // G306: a fake executable
		t.Fatal(err)
	}
	if err := os.Chtimes(installed, frontNow.Add(-time.Hour), frontNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, p := range collectSandboxStatus(context.Background(), be, r.env.ProfileDir).Profiles {
		writeDigest(t, home, p.Sandbox, p.CompiledDigest)
		m.sandboxes = append(m.sandboxes, p.Sandbox)
	}
	f.calls = nil
	return r
}

// approveAll pins every set's live tools, as a reviewed install has.
func (r *frontRig) approveAll(t *testing.T) {
	t.Helper()
	reg, err := toolreg.Load(toolreg.DefaultPath(r.env.Home))
	if err != nil {
		t.Fatal(err)
	}
	rv, err := reviewFrom(context.Background(), r.env.toolsEnv(), reg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := approveAndSave(rv, slices.Sorted(maps.Keys(reg.Sets)), frontNow); err != nil {
		t.Fatal(err)
	}
}

func (r *frontRig) pins(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(toolgateway.DefaultPinsPath(r.env.Home))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runFront(t *testing.T, cmd *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	root := &cobra.Command{Use: "aimux", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
	defer func() { jsonOutput = false }()
	root.AddCommand(cmd)
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errb.String(), err
}

func (r *frontRig) setup(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runFront(t, newSetupCmdWith(r.env), append([]string{"setup"}, args...)...)
}

type setupDoc struct {
	SchemaVersion string `json:"schema_version"`
	Steps         []struct {
		Name   string   `json:"name"`
		Action string   `json:"action"`
		Output []string `json:"output"`
	} `json:"steps"`
	Approved []string `json:"approved"`
	Summary  []struct {
		Concern string   `json:"concern"`
		Status  string   `json:"status"`
		Detail  string   `json:"detail"`
		Next    []string `json:"next"`
	} `json:"summary"`
}

func decodeSetup(t *testing.T, s string) setupDoc {
	t.Helper()
	var d setupDoc
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatalf("setup --json is not JSON: %v\n%s", err, s)
	}
	return d
}

// summaryLines are the six summary lines of setup's text output.
func summaryLines(t *testing.T, out string) []string {
	t.Helper()
	var got []string
	for _, l := range strings.Split(out, "\n") {
		for _, sym := range []string{"✓ ", "! ", "✗ "} {
			if strings.HasPrefix(l, sym) {
				got = append(got, l)
			}
		}
	}
	if len(got) != 6 {
		t.Fatalf("want 6 summary lines, got %d:\n%s", len(got), out)
	}
	for i, c := range []string{"sandbox", "tools", "router", "browser", "gateway"} {
		if f := strings.Fields(got[i]); len(f) < 2 || f[1] != c {
			t.Fatalf("summary line %d = %q, want concern %s", i, got[i], c)
		}
	}
	return got
}

// A healthy install: setup has nothing to do, in every mode, and runs only
// read-only probes and writes nothing; the summary is all ✓.
func TestSetup_HealthyChangesNothing(t *testing.T) {
	r := newFrontRig(t)
	before := snapshotTree(t, r.env.Home)
	out, errOut, err := r.setup(t)
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	for _, l := range summaryLines(t, out) {
		if !strings.HasPrefix(l, "✓ ") {
			t.Errorf("healthy summary line %q", l)
		}
	}
	if strings.Contains(errOut, "Next:") {
		t.Errorf("healthy run printed Next: %s", errOut)
	}
	jout, _, err := r.setup(t, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if d := decodeSetup(t, jout); len(d.Steps) != 0 || len(d.Approved) != 0 || len(d.Summary) != 6 || d.SchemaVersion != "2" {
		t.Fatalf("doc = %+v", d)
	}
	r.tty = true
	if _, _, err := r.setup(t); err != nil {
		t.Fatal(err)
	}
	if after := snapshotTree(t, r.env.Home); after != before {
		t.Fatalf("setup changed home:\nbefore %s\nafter  %s", before, after)
	}
	assertOnlyReadOnly(t, r.f)
}

// legacyFrontJSON is the rig's registry as an old readonly.json.
func legacyFrontJSON(home string) string {
	return `{"HTTP":{"jira":{"URL":"http://127.0.0.1:9/mcp/jira"},"slack":{"URL":"http://127.0.0.1:9/mcp/slack"},"google":{"URL":"http://127.0.0.1:9/mcp/google"}},` +
		`"WikiRoot":"` + home + `","Allow":{"wiki":["wiki_search"],"jira":["search"],"slack":["history"],"google":["get_doc"]}}`
}

// brokenFront is a rig missing what setup sets up: the image, the
// provider, the registry (readonly.json not migrated) and clients.yaml.
func brokenFront(t *testing.T) *frontRig {
	t.Helper()
	r := newFrontRig(t)
	r.m.imagePresent, r.m.providerPresent = false, false
	home := r.env.Home
	for _, p := range []string{toolreg.DefaultPath(home), filepath.Join(home, ".config", "aimux", "clients.yaml")} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(toolreg.LegacyPath(home), []byte(legacyFrontJSON(home)), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

// Without --yes and no terminal to ask on, setup runs no step: it lists
// each missing one as pending and says how to run them.
func TestSetup_NonInteractiveWithoutYesRunsNoStep(t *testing.T) {
	r := brokenFront(t)
	before := snapshotTree(t, r.env.Home)
	out, errOut, err := r.setup(t, "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	d := decodeSetup(t, out)
	var names []string
	for _, s := range d.Steps {
		names = append(names, s.Name)
		if s.Action != "pending" {
			t.Errorf("step %s action %s, want pending", s.Name, s.Action)
		}
	}
	if !slices.Equal(names, []string{"tools registry", "clients", "sandbox"}) {
		t.Fatalf("steps = %v", names)
	}
	if !strings.Contains(errOut, "Next: aimux setup --yes") {
		t.Fatalf("stderr = %q", errOut)
	}
	if out, _, _ := r.setup(t); !strings.Contains(out, "Pending (needs --yes, or a terminal to ask on): Prepare the sandbox: build "+wingmanImage) {
		t.Fatalf("text lacks the pending sandbox step:\n%s", out)
	}
	if after := snapshotTree(t, r.env.Home); after != before {
		t.Fatalf("setup without --yes changed home:\nbefore %s\nafter  %s", before, after)
	}
	assertOnlyReadOnly(t, r.f)
}

// With --yes setup runs exactly the missing steps; a second run finds
// nothing missing and changes nothing.
func TestSetup_YesRunsMissingStepsThenSecondRunChangesNothing(t *testing.T) {
	r := brokenFront(t)
	out, errOut, err := r.setup(t, "--yes")
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, errOut)
	}
	if len(r.f.find("podman", "build", "-t", wingmanImage)) != 1 || len(r.f.find("openshell", "-g", "local", "provider", "create", "--name", wingmanProvider)) != 1 {
		t.Fatalf("calls = %v", r.f.calls)
	}
	if _, err := toolreg.Load(toolreg.DefaultPath(r.env.Home)); err != nil {
		t.Fatalf("registry not migrated: %v", err)
	}
	if _, err := os.Stat(toolreg.LegacyPath(r.env.Home) + ".migrated"); err != nil {
		t.Fatalf("readonly.json not kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.env.Home, ".config", "aimux", "clients.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Migrated ", "Building " + wingmanImage, "Creating provider " + wingmanProvider} {
		if !strings.Contains(out, want) {
			t.Errorf("first run lacks %q:\n%s", want, out)
		}
	}
	summaryLines(t, out)

	r.f.calls = nil
	before := snapshotTree(t, r.env.Home)
	out, _, err = r.setup(t, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if d := decodeSetup(t, out); len(d.Steps) != 0 {
		t.Fatalf("second run steps = %+v", d.Steps)
	}
	if after := snapshotTree(t, r.env.Home); after != before {
		t.Fatalf("second run changed home:\nbefore %s\nafter  %s", before, after)
	}
	assertOnlyReadOnly(t, r.f)
}

// On a terminal without --yes, each missing step is asked; a no skips it.
func TestSetup_TTYAsksPerStep(t *testing.T) {
	r := newFrontRig(t)
	r.m.imagePresent = false
	r.tty, r.input = true, "\nn\n" // the parts menu: none; then the step
	out, errOut, err := r.setup(t)
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if !strings.Contains(out, "[Y/n]") || len(r.f.find("podman", "build")) != 0 {
		t.Fatalf("out:\n%s\ncalls %v", out, r.f.calls)
	}
	if !strings.Contains(out, "declined") {
		t.Errorf("out lacks declined:\n%s", out)
	}
	r.input = "" // end of input is no, even where Enter is yes
	if _, _, err := r.setup(t); err != nil {
		t.Fatal(err)
	}
	if len(r.f.find("podman", "build")) != 0 {
		t.Fatalf("EOF ran the step: %v", r.f.calls)
	}
	r.input = "\n\n" // the parts menu: none; then Enter is yes
	if _, _, err := r.setup(t); err != nil {
		t.Fatal(err)
	}
	if len(r.f.find("podman", "build")) != 1 {
		t.Fatalf("Enter did not run the step: %v", r.f.calls)
	}
}

// changeTools makes jira/search and slack/history change live.
func (r *frontRig) changeTools() {
	r.live["jira"] = []json.RawMessage{rawTool("search", "number")}
	r.live["slack"] = []json.RawMessage{rawTool("history", "number")}
}

// Setup never approves a tool and never asks to: it says how many need review and names the command, whatever
// stdin holds, with or without a terminal.
func TestSetup_NeverApprovesOrAsksAboutTools(t *testing.T) {
	for _, c := range []struct {
		name string
		tty  bool
		args []string
	}{
		{"no terminal", false, nil},
		{"no terminal --yes", false, []string{"--yes"}},
		{"terminal", true, nil},
		{"terminal --yes", true, []string{"--yes"}},
		{"terminal --json", true, []string{"--json"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newFrontRig(t)
			r.changeTools()
			r.tty, r.input = c.tty, "\ny\ny\na\ny\n"
			before := r.pins(t)
			out, errOut, err := r.setup(t, c.args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, errOut)
			}
			if r.pins(t) != before {
				t.Fatal("setup approved a tool")
			}
			if strings.Contains(out, "Approve") {
				t.Fatalf("setup asked about a tool:\n%s", out)
			}
			if !strings.Contains(errOut, "Next: aimux tools review") {
				t.Fatalf("stderr = %q", errOut)
			}
		})
	}
}

// reviewRun runs `aimux tools review` on the rig, with its scripted terminal.
func (r *frontRig) reviewRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	env := r.env.toolsEnv()
	env.IsTTY, env.In = func() bool { return r.tty }, func() io.Reader { return strings.NewReader(r.input) }
	return runTools(t, env, append([]string{"review"}, args...)...)
}

// Each tool to review is shown with its diff, and only a yes pins it; all are written in one save.
func TestToolsReview_ApprovesOnlyAnExplicitYes(t *testing.T) {
	r := newFrontRig(t)
	r.changeTools()
	r.tty, r.input = true, "y\n\n" // jira: yes; slack: Enter, which is no
	out, _, err := r.reviewRun(t)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"changed jira:readonly/search", "q.type: \"string\" -> \"number\"",
		"Approve jira:readonly/search? [y/N/a]", "Approve slack:readonly/history? [y/N/a]", "Approved jira:readonly/search", "1 tool still needs review"} {
		if !strings.Contains(out, want) {
			t.Errorf("out lacks %q:\n%s", want, out)
		}
	}
	st := toolsStatusOf(t, r)
	if st["jira:readonly/search"] != toolgateway.StatusActive || st["slack:readonly/history"] != toolgateway.StatusChanged {
		t.Fatalf("statuses = %v", st)
	}
}

// "a" approves that tool and every one after it, each still shown with its diff.
func TestToolsReview_AApprovesTheRest(t *testing.T) {
	r := newFrontRig(t)
	r.changeTools()
	r.tty, r.input = true, "a\n"
	out, _, err := r.reviewRun(t)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	st := toolsStatusOf(t, r)
	if st["jira:readonly/search"] != toolgateway.StatusActive || st["slack:readonly/history"] != toolgateway.StatusActive {
		t.Fatalf("statuses = %v\n%s", st, out)
	}
	if strings.Count(out, "? [y/N/a]") != 1 || !strings.Contains(out, "changed slack:readonly/history") || strings.Contains(out, "still need") {
		t.Fatalf("out:\n%s", out)
	}
}

// Without a terminal, or with --json, nothing is asked or approved, and the commands that work there are named.
func TestToolsReview_NeedsATerminal(t *testing.T) {
	for _, args := range [][]string{nil, {"--json"}} {
		r := newFrontRig(t)
		r.changeTools()
		r.tty, r.input = len(args) > 0, "a\n"
		before := r.pins(t)
		_, _, err := r.reviewRun(t, args...)
		if err == nil || !strings.Contains(err.Error(), "aimux tools diff") || !strings.Contains(err.Error(), "aimux tools approve") {
			t.Fatalf("%v: err = %v", args, err)
		}
		if r.pins(t) != before {
			t.Fatalf("%v: approved a tool", args)
		}
	}
}

func TestToolsReview_NothingToReview(t *testing.T) {
	r := newFrontRig(t)
	r.tty = true
	out, _, err := r.reviewRun(t)
	if err != nil || !strings.Contains(out, "Nothing to review") {
		t.Fatalf("%q, %v", out, err)
	}
}

func toolsStatusOf(t *testing.T, r *frontRig) map[string]string {
	t.Helper()
	rv, err := loadToolsReview(context.Background(), r.env.toolsEnv())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range toolgateway.Review(rv.reg, rv.pins, rv.live) {
		got[s.Set+"/"+s.Tool] = s.Status
	}
	return got
}

// The end of input answers no.
func TestToolsReview_EOFApprovesNothing(t *testing.T) {
	r := newFrontRig(t)
	r.changeTools()
	r.tty, r.input = true, ""
	before := r.pins(t)
	if _, _, err := r.reviewRun(t); err != nil {
		t.Fatal(err)
	}
	if r.pins(t) != before {
		t.Fatal("approved on EOF")
	}
}

// removeBrowser removes the host manifests and wrapper (and with id the
// pinned extension id).
func (r *frontRig) removeBrowser(t *testing.T, id bool) {
	t.Helper()
	paths := browserHostFiles(r.env.Home)
	if id {
		paths = append(paths, extensionIDFile(r.env.Home))
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

// --extension-id registers the host; a pinned id with no host registered
// re-registers it; a host registered for that id is left alone.
func TestSetup_BrowserStep(t *testing.T) {
	const otherID = "ponmlkjihgfedcbaponmlkjihgfedcba"
	r := newFrontRig(t)
	r.removeBrowser(t, true)
	out, _, err := r.setup(t)
	if err != nil {
		t.Fatal(err)
	}
	if l := summaryLines(t, out)[3]; !strings.HasPrefix(l, "! browser") {
		t.Fatalf("browser line = %q", l)
	}
	if _, errOut, _ := r.setup(t); !strings.Contains(errOut, "Next: aimux setup --extension-id <id>") {
		t.Fatalf("stderr = %q", errOut)
	}

	if _, errOut, err := r.setup(t, "--yes", "--extension-id", otherID); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if id, err := pinnedExtensionID(r.env.Home); err != nil || id != otherID {
		t.Fatalf("pinned = %q, %v", id, err)
	}

	r.removeBrowser(t, false) // id pinned, host gone
	out, _, err = r.setup(t, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if d := decodeSetup(t, out); len(d.Steps) != 1 || d.Steps[0].Name != "browser" || d.Steps[0].Action != "ran" {
		t.Fatalf("steps = %+v", d.Steps)
	}
	for _, p := range browserHostFiles(r.env.Home) {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s not restored: %v", p, err)
		}
	}

	out, _, _ = r.setup(t, "--yes", "--json", "--extension-id", otherID) // same id, registered: nothing to do
	if d := decodeSetup(t, out); len(d.Steps) != 0 {
		t.Fatalf("steps = %+v", d.Steps)
	}
	if _, _, err := r.setup(t, "--extension-id", "bad"); err == nil || !strings.Contains(err.Error(), "invalid --extension-id") {
		t.Fatalf("err = %v", err)
	}
}

// The summary says what is wrong and one Next line per problem.
func TestSetup_SummaryProblemsAndNext(t *testing.T) {
	r := newFrontRig(t)
	r.keyFound = false
	r.m.gatewayUp = false
	r.changeTools()
	out, errOut, err := r.setup(t)
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	lines := summaryLines(t, out)
	for i, sym := range []string{"! ", "! ", "! ", "✓ ", "✗ "} {
		if !strings.HasPrefix(lines[i], sym) {
			t.Errorf("line %d = %q, want %q", i, lines[i], sym)
		}
	}
	for _, want := range []string{"Next: aimux tools review", "Next: store the OpenRouter key", "Next: start the OpenShell gateway"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(out+errOut, "not_allowed") {
		t.Fatal("not_allowed is not a status")
	}
}

// browser uninstall removes the host manifests and the wrapper and keeps
// the pinned id, unless --all; a second run has nothing to remove.
func TestBrowserUninstall(t *testing.T) {
	r := newFrontRig(t)
	adapter := filepath.Join(r.env.Home, ".aimux", "wingman", "adapter")
	if err := os.MkdirAll(adapter, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		out, errOut, err := runFront(t, newBrowserCmdWith(r.env.browserEnv), append([]string{"browser", "uninstall"}, args...)...)
		if err != nil {
			t.Fatalf("%v\n%s", err, errOut)
		}
		return out
	}
	out := run()
	for _, p := range browserHostFiles(r.env.Home) {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still there: %v", p, err)
		}
		if !strings.Contains(out, "Removed "+p) {
			t.Errorf("out lacks Removed %s:\n%s", p, out)
		}
	}
	if _, err := os.Stat(extensionIDFile(r.env.Home)); err != nil {
		t.Fatalf("extension id removed without --all: %v", err)
	}
	if out := run(); !strings.Contains(out, "Nothing to remove") {
		t.Fatalf("second run:\n%s", out)
	}
	out = run("--all")
	if _, err := os.Stat(extensionIDFile(r.env.Home)); !errors.Is(err, os.ErrNotExist) || !strings.Contains(out, "Removed "+extensionIDFile(r.env.Home)) {
		t.Fatalf("--all kept the id: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(r.env.Home, ".aimux", "wingman", "adapter")); err != nil {
		t.Fatalf("uninstall removed the adapter: %v", err)
	}
}

// agent-context lists setup, doctor and browser uninstall.
func TestAgentContextCmd_ListsSetupDoctorUninstall(t *testing.T) {
	var stdout bytes.Buffer
	s, d, b := newSetupCmdWith(frontEnv{}), newDoctorCmdWith(frontEnv{}), newBrowserCmdWith(browserEnv{})
	c := newAgentContextCmd([]string{"claude"})
	rootCmd.AddCommand(s, d, b, c)
	defer rootCmd.RemoveCommand(s, d, b, c)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"agent-context"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Commands map[string]commandInfo `json:"commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"setup", "doctor", "browser uninstall"} {
		if _, ok := result.Commands[n]; !ok {
			t.Errorf("agent-context lacks %q", n)
		}
	}
	for _, f := range []string{"--yes", "--extension-id"} {
		if _, ok := result.Commands["setup"].Flags[f]; !ok {
			t.Errorf("setup lacks %s", f)
		}
	}
	if _, ok := result.Commands["browser uninstall"].Flags["--all"]; !ok {
		t.Error("browser uninstall lacks --all")
	}
}

// rigBinary is the aimux the rig's host.sh execs.
func rigBinary(home string) string { return filepath.Join(home, "App Bin", "aimux") }

// Two sets that list the same tool of one source share one approval: it is asked once, and a no holds for both,
// also when a later answer approves all the rest.
func TestToolsReview_ANoHoldsForEverySetThatListsTheTool(t *testing.T) {
	r := newFrontRig(t)
	reg := frontRegistry(r.env.Home)
	reg.Sets["jira:extra"] = toolreg.Set{Source: "jira", Tools: []string{"search"}}
	path := toolreg.DefaultPath(r.env.Home)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := toolreg.WriteNew(path, reg); err != nil {
		t.Fatal(err)
	}
	r.changeTools()
	r.tty, r.input = true, "n\na\na\n"
	out, _, err := r.reviewRun(t)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	st := toolsStatusOf(t, r)
	if st["jira:readonly/search"] == toolgateway.StatusActive || st["jira:extra/search"] == toolgateway.StatusActive || st["slack:readonly/history"] != toolgateway.StatusActive {
		t.Fatalf("statuses = %v\n%s", st, out)
	}
	if strings.Count(out, "? [y/N/a]") != 2 || !strings.Contains(out, "jira:extra/search, jira:readonly/search") {
		t.Fatalf("the shared tool was not asked about once, with both sets named:\n%s", out)
	}
}
