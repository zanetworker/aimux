//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "aimux-e2e-*")
	if err != nil {
		panic("cannot create temp dir: " + err.Error())
	}
	defer os.RemoveAll(tmpDir)

	binaryPath = filepath.Join(tmpDir, "aimux")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "../../cmd/aimux")
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		panic("cannot build aimux: " + err.Error())
	}

	os.Exit(m.Run())
}

func TestVersion(t *testing.T) {
	out, err := exec.Command(binaryPath, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("version failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "aimux") {
		t.Errorf("expected 'aimux' in version output, got: %s", out)
	}
}

func TestVersionJSON(t *testing.T) {
	out, err := exec.Command(binaryPath, "version", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("version --json failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("version --json output is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := result["version"]; !ok {
		t.Error("expected 'version' field in JSON output")
	}
	if _, ok := result["go"]; !ok {
		t.Error("expected 'go' field in JSON output")
	}
}

func TestAgentsJSON(t *testing.T) {
	out, err := exec.Command(binaryPath, "agents", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("agents --json failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("agents --json is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := result["agents"]; !ok {
		t.Error("expected 'agents' field in JSON output")
	}
	if _, ok := result["count"]; !ok {
		t.Error("expected 'count' field in JSON output")
	}
}

func TestSessionsList(t *testing.T) {
	out, err := exec.Command(binaryPath, "sessions", "--list", "--json", "--limit", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("sessions --list --json failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("sessions --list --json is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := result["sessions"]; !ok {
		t.Error("expected 'sessions' field in JSON output")
	}
}

func TestSpawnDryRun(t *testing.T) {
	out, err := exec.Command(binaryPath, "spawn", "claude", "--dry-run", "--json", "--dir", "/tmp").CombinedOutput()
	if err != nil {
		t.Fatalf("spawn --dry-run failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("spawn --dry-run --json is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := result["provider"]; !ok {
		t.Error("expected 'provider' field in dry-run output")
	}
	if result["dry_run"] != true {
		t.Error("expected 'dry_run' to be true")
	}
}

func TestAgentContext(t *testing.T) {
	out, err := exec.Command(binaryPath, "agent-context").CombinedOutput()
	if err != nil {
		t.Fatalf("agent-context failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("agent-context output is not valid JSON: %v\n%s", err, out)
	}
	if _, ok := result["commands"]; !ok {
		t.Error("expected 'commands' field in agent-context output")
	}
	if _, ok := result["schema_version"]; !ok {
		t.Error("expected 'schema_version' field in agent-context output")
	}
}

func TestKillNoArgs(t *testing.T) {
	err := exec.Command(binaryPath, "kill").Run()
	if err == nil {
		t.Error("kill without args should fail")
	}
}

func TestExportNoArgs(t *testing.T) {
	err := exec.Command(binaryPath, "export").Run()
	if err == nil {
		t.Error("export without args should fail")
	}
}

func TestAgentsSortFlag(t *testing.T) {
	out, err := exec.Command(binaryPath, "agents", "--json", "--sort", "name").CombinedOutput()
	if err != nil {
		t.Fatalf("agents --sort name failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
}

func TestAgentsFilterFlag(t *testing.T) {
	out, err := exec.Command(binaryPath, "agents", "--json", "--filter", "nonexistent").CombinedOutput()
	if err != nil {
		t.Fatalf("agents --filter failed: %v\n%s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
}

func TestUnknownCommand(t *testing.T) {
	cmd := exec.Command(binaryPath, "nonexistent-cmd")
	err := cmd.Run()
	if err == nil {
		t.Error("expected non-zero exit code for unknown command")
	}
}
