package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/parts"
	"github.com/zanetworker/aimux/internal/toolreg"
)

func menuConfig(t *testing.T, r *frontRig) string {
	t.Helper()
	return filepath.Join(r.env.Home, ".aimux", "config.yaml")
}

// On a terminal setup opens with every part, and an empty answer turns nothing on and writes nothing.
func TestSetupMenu_ListsThePartsAndAnEmptyAnswerChangesNothing(t *testing.T) {
	r := newFrontRig(t)
	r.tty, r.input = true, "\n"
	before := snapshotTree(t, r.env.Home)
	out, errOut, err := r.setup(t)
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	for _, p := range parts.Parts() {
		if !strings.Contains(out, p.Name) {
			t.Errorf("the menu does not list %s:\n%s", p.Name, out)
		}
	}
	for _, want := range []string{"  1  Inference proxy", "  2  MCP proxy", "not available yet", "managed by Wingman", "Turn on"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "Turn on") > strings.Index(out, "✓ sandbox") {
		t.Errorf("the menu does not come first:\n%s", out)
	}
	summaryLines(t, out)
	if after := snapshotTree(t, r.env.Home); after != before {
		t.Fatal("an empty answer changed the home folder")
	}
}

// Choosing two states both in the file. Neither has a job on this machine, so each is said to be left as it is,
// the checks still run, and the command does not report success.
func TestSetupMenu_ChoosingTwoStatesBoth(t *testing.T) {
	r := newFrontRig(t)
	r.tty, r.input = true, "1, 2\n"
	out, _, err := r.setup(t)
	if err == nil || !strings.Contains(err.Error(), "inference-proxy") || !strings.Contains(err.Error(), "mcp-proxy") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	s, loadErr := parts.Load(menuConfig(t, r))
	if loadErr != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v %v", loadErr, s.Validate())
	}
	for _, id := range []string{"inference-proxy", "mcp-proxy"} {
		if p, ok := s.Parts[id]; !ok || p.Enabled == nil || !*p.Enabled {
			t.Errorf("%s is not stated as on: %+v", id, s.Parts)
		}
	}
	if !strings.Contains(out, "no job") {
		t.Errorf("it does not say why the services were left:\n%s", out)
	}
	summaryLines(t, out)
}

// A number that is not on the menu is said, nothing is turned on for that answer, and it asks again.
func TestSetupMenu_AWrongNumberAsksAgain(t *testing.T) {
	for _, wrong := range []string{"9", "0", "1 x", "-1"} {
		r := newFrontRig(t)
		r.tty, r.input = true, wrong+"\n\n"
		before := snapshotTree(t, r.env.Home)
		out, errOut, err := r.setup(t)
		if err != nil {
			t.Fatalf("%q: %v\n%s", wrong, err, errOut)
		}
		if strings.Count(out, "Turn on") != 2 || !strings.Contains(out, "is not one of the numbers") {
			t.Errorf("%q:\n%s", wrong, out)
		}
		if after := snapshotTree(t, r.env.Home); after != before {
			t.Errorf("%q changed the home folder", wrong)
		}
	}
}

// Without a terminal nothing is asked and the text is as before; --json gains the parts and a new schema version.
func TestSetupMenu_NoTerminalAndJSON(t *testing.T) {
	r := newFrontRig(t)
	out, _, err := r.setup(t)
	if err != nil || strings.Contains(out, "Turn on") || strings.Contains(out, "Inference proxy") {
		t.Fatalf("%v\n%s", err, out)
	}
	r.tty, r.input = true, "1\n"
	jout, _, err := r.setup(t, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion string `json:"schema_version"`
		Parts         []struct{ ID, Name, About, State, Why string }
		Summary       []json.RawMessage
	}
	if err := json.Unmarshal([]byte(jout), &doc); err != nil {
		t.Fatalf("%v\n%s", err, jout)
	}
	if doc.SchemaVersion != "2" || len(doc.Parts) != len(parts.Parts()) || len(doc.Summary) != 6 {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Parts[1].ID != "inference-proxy" || doc.Parts[1].State != "not set" || doc.Parts[0].State != "on" {
		t.Errorf("parts = %+v", doc.Parts)
	}
	if _, err := os.Stat(menuConfig(t, r)); err == nil {
		if s, _ := parts.Load(menuConfig(t, r)); s.Stated {
			t.Error("--json turned a part on")
		}
	}
}

// Sandboxes turned off in the file: its checks and steps are not run, and that is said.
func TestSetupMenu_SandboxesOffSkipsItsChecks(t *testing.T) {
	r := brokenFront(t)
	if err := os.WriteFile(menuConfig(t, r), []byte("parts:\n  sandboxes:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := r.setup(t, "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if !strings.Contains(out, "Sandboxes is off") || strings.Contains(out, "Running:") || strings.Contains(out, "✓ ") || strings.Contains(errOut, "Next:") {
		t.Fatalf("out:\n%s\nerr:\n%s", out, errOut)
	}
	jout, _, err := r.setup(t, "--yes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if d := decodeSetup(t, jout); len(d.Steps) != 0 || len(d.Summary) != 0 {
		t.Fatalf("doc = %+v", d)
	}
}

// A parts section with a problem is said in every mode, and no part is reported as if the file had been read.
func TestSetupMenu_AProblemInThePartsSectionIsSaid(t *testing.T) {
	r := newFrontRig(t)
	if err := os.WriteFile(menuConfig(t, r), []byte("parts:\n  sandboxes:\n    enabled: false\n  inference-proxy:\n    port: many\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Sandboxes is plainly off, whatever is wrong with another part: its steps are not run.
	out, errOut, err := r.setup(t, "--yes")
	if err != nil || !strings.Contains(errOut, "Next: aimux setup --check") || !strings.Contains(out, "Sandboxes is off") {
		t.Fatalf("%v\nout:\n%s\nerr:\n%s", err, out, errOut)
	}
	jout, _, err := r.setup(t, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Parts        []json.RawMessage `json:"parts"`
		PartsProblem string            `json:"parts_problem"`
	}
	if err := json.Unmarshal([]byte(jout), &doc); err != nil || len(doc.Parts) != 0 || doc.PartsProblem == "" {
		t.Fatalf("%v\n%s", err, jout)
	}
	// On a terminal nothing is offered.
	r.tty, r.input = true, "1\n"
	out, _, _ = r.setup(t)
	if strings.Contains(out, "Turn on") {
		t.Errorf("a part was offered from a file with a problem:\n%s", out)
	}
}

// Services are started through launchd: elsewhere the parts are listed and none is offered.
func TestSetupMenu_NothingIsOfferedWithoutLaunchd(t *testing.T) {
	r := newFrontRig(t)
	r.env.GOOS = "linux"
	r.tty, r.input = true, "1\n"
	before := snapshotTree(t, r.env.Home)
	out, _, _ := r.setup(t)
	if !strings.Contains(out, "Inference proxy") || strings.Contains(out, "Turn on") || strings.Contains(out, "  1  ") {
		t.Fatalf("out:\n%s", out)
	}
	if after := snapshotTree(t, r.env.Home); after != before {
		t.Fatal("a part was turned on")
	}
}

// Sandboxes stated twice is not a plain "off": its checks still run.
func TestSetupMenu_AnAmbiguousSandboxesSettingSkipsNothing(t *testing.T) {
	r := newFrontRig(t)
	if err := os.WriteFile(menuConfig(t, r), []byte("parts:\n  sandboxes:\n    enabled: true\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := r.setup(t)
	if err != nil || strings.Contains(out, "Sandboxes is off") || !strings.Contains(errOut, "Next: aimux setup --check") {
		t.Fatalf("%v\nout:\n%s\nerr:\n%s", err, out, errOut)
	}
	summaryLines(t, out)
}

// A registry already in its newer form (servers.yaml) has no tool-sources.yaml, and needs none written.
func TestSetup_NoRegistryStepWhenServersYamlExists(t *testing.T) {
	r := newFrontRig(t)
	old := toolreg.DefaultPath(r.env.Home)
	if err := toolreg.MigrateSplit(filepath.Dir(old)); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(old)
	if _, err := os.Stat(toolreg.ServersPath(r.env.Home)); err != nil {
		t.Fatalf("the rig has no servers.yaml after the migration: %v", err)
	}
	out, _, err := r.setup(t, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(old); statErr == nil || strings.Contains(out, "tool registry") {
		t.Fatalf("setup wrote or offered the old registry file (%v):\n%s", statErr, out)
	}
}

// aimux browser setup goes through the same function: it writes no old file beside a servers.yaml either.
func TestSetupToolSources_WritesNothingBesideServersYaml(t *testing.T) {
	r := newFrontRig(t)
	old := toolreg.DefaultPath(r.env.Home)
	if err := toolreg.MigrateSplit(filepath.Dir(old)); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(old)
	reg, err := setupToolSources(r.env.Home, func(string, ...any) {})
	if _, statErr := os.Stat(old); err != nil || statErr == nil || len(reg.Sets) != 4 {
		t.Fatalf("err %v, old file %v, %d sets", err, statErr, len(reg.Sets))
	}
}

// An old readonly.json beside a servers.yaml is not waiting for anything: the newer files are what is read.
func TestLegacyRegistryWaiting_NotBesideServersYaml(t *testing.T) {
	r := newFrontRig(t)
	old := toolreg.DefaultPath(r.env.Home)
	if err := os.WriteFile(toolreg.LegacyPath(r.env.Home), []byte(legacyFrontJSON(r.env.Home)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := toolreg.MigrateSplit(filepath.Dir(old)); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(old)
	if _, note, err := readRegistry(r.env.Home); err != nil || note != "" || legacyRegistryWaiting(r.env.Home) {
		t.Fatalf("note %q, err %v", note, err)
	}
	// Without the newer files it is waiting, as before.
	if err := os.Remove(toolreg.ServersPath(r.env.Home)); err != nil {
		t.Fatal(err)
	}
	if !legacyRegistryWaiting(r.env.Home) {
		t.Fatal("an old file with nothing newer is not reported as waiting")
	}
}
