package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/controller"
)

// The dashboard's diagnosis is the doctor's, without the finding about the shell: a page is not in one, and the
// environment of the process serving it says nothing about the owner's terminals.
func TestProxyDoctorReport_ForAPage(t *testing.T) {
	port := proxyAnswering(t, "codex")
	deps := doctorDeps{key: testKey, home: t.TempDir(), cliBuild: "v0.14.0-289-gabc", now: time.Now, getenv: func(string) string { return "" }}
	report, err := proxyDoctorReport(context.Background(), port, deps, false)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, f := range report.Findings {
		ids[f.ID] = f.Level
	}
	if _, has := ids["shell"]; has || report.Port != port || report.Verdict != controller.ProxyVerdict(report.Findings) || ids["service"] != controller.FindingOK {
		t.Fatalf("%+v", report)
	}
	withShell, err := proxyDoctorReport(context.Background(), port, deps, true)
	if err != nil || len(withShell.Findings) != len(report.Findings)+1 {
		t.Fatalf("the terminal's report keeps the shell finding: %v %+v", err, withShell)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), envTestKey) {
		t.Fatal("the key is in the report")
	}

	// Nothing answering is a report, not an error: the page shows it with its fix.
	down, err := proxyDoctorReport(context.Background(), 1, deps, false)
	if err != nil || down.Verdict != controller.FindingFail {
		t.Fatalf("%v %+v", err, down)
	}
}

// The engines document a page reads is the one `aimux engine list --json` prints: the engines the file states,
// the roles, and the subagent definitions.
func TestEnginesDocument_FromTheFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        enabled: true\n    models:\n      fast: codex:gpt-6.2-sol\n    roles:\n      subagent: fast\n"
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	// A subagent definition in the folder the dashboard was started in is listed, as the command run there lists it.
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".claude", "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".claude", "agents", "scout.md"), []byte("---\nname: scout\n---\nLook around.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := EnginesReader(home, project)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)
	var got struct {
		Engines []engineRow              `json:"engines"`
		Roles   []controller.RoleRow     `json:"roles"`
		Sub     []controller.SubagentRow `json:"subagents"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range got.Engines {
		names[e.Name] = e.On
	}
	if !names["anthropic"] || len(got.Sub) != 1 || got.Sub[0].Name != "scout" || got.Sub[0].RunsOn != "codex" || !got.Sub[0].FromRole {
		t.Fatalf("%s", raw)
	}
	var subagent controller.RoleRow
	for _, r := range got.Roles {
		if r.Role == "subagent" {
			subagent = r
		}
	}
	if subagent.Entry != "fast" || subagent.Model != "codex:gpt-6.2-sol" || subagent.Engine != "codex" {
		t.Fatalf("the subagent role: %+v in %s", subagent, raw)
	}

	// A config file that cannot be read is an error the page shows, not an empty list.
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte("parts: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnginesReader(home, project)(context.Background()); err == nil {
		t.Fatal("a broken config file gave no error")
	}
}

// The TUI's one reading of the machine takes each part from the function its command prints from, and a part
// that cannot be read says why without taking the others with it.
func TestSystemReader(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        enabled: true\n"
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := SystemReader(home, "v0.14.0-289-gabc", "")(context.Background())
	if snap.At.IsZero() || len(snap.Services.Services) != 4 || snap.Services.CLIBuild != "v0.14.0-289-gabc" {
		t.Fatalf("services: %+v", snap.Services)
	}
	names := map[string]bool{}
	for _, e := range snap.Engines {
		names[e.Name] = e.On
	}
	if snap.EnginesErr != "" || !names["anthropic"] || !names["codex"] || len(snap.Roles) == 0 {
		t.Fatalf("engines: %q %+v %+v", snap.EnginesErr, snap.Engines, snap.Roles)
	}
	// No ledger under this home: an empty meter that says no file was read, not an error.
	if snap.MeterErr != "" || snap.Meter.Files != 0 || len(snap.Meter.Rows) != 0 {
		t.Fatalf("meter: %q %+v", snap.MeterErr, snap.Meter)
	}

	// A config file with a problem fails the engines alone.
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte("parts: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := SystemReader(home, "v0.14.0-289-gabc", "")(context.Background())
	if broken.EnginesErr == "" || len(broken.Services.Services) != 4 || broken.MeterErr != "" {
		t.Fatalf("a broken config file: engines %q, services %d, meter %q", broken.EnginesErr, len(broken.Services.Services), broken.MeterErr)
	}
}
