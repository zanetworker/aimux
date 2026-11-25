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

// A dry-run upgrade in a home with no services lists both as not installed, changes nothing and exits 0.
func TestUpgradeDryRunJSON(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(binaryPath, "upgrade", "--dry-run", "--json")
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("upgrade --dry-run --json failed: %v\n%s", err, out)
	}
	var result struct {
		DryRun   bool `json:"dry_run"`
		Services []struct{ ID, Outcome string }
		NotMoved []struct{ ID, Update string } `json:"not_moved"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if !result.DryRun || len(result.Services) != 3 || result.Services[0].ID != "model-proxy" || result.Services[1].ID != "mcp-proxy" || result.Services[2].ID != "collector" {
		t.Fatalf("unexpected services: %s", out)
	}
	for _, svc := range result.Services {
		if svc.Outcome != "not installed" {
			t.Errorf("%s: outcome %q, want not installed", svc.ID, svc.Outcome)
		}
	}
	if len(result.NotMoved) != 1 || result.NotMoved[0].Update == "" {
		t.Errorf("not_moved should name the one service upgrade leaves alone: %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".aimux", "services")); !os.IsNotExist(err) {
		t.Errorf("a dry run wrote to the home folder: %v", err)
	}
}

func TestUpgradeUnknownService(t *testing.T) {
	out, err := exec.Command(binaryPath, "upgrade", "nope", "--dry-run").CombinedOutput()
	if err == nil {
		t.Fatal("upgrade with an unknown service should fail")
	}
	if !strings.Contains(string(out), `unknown service "nope"`) || !strings.Contains(string(out), "model-proxy, mcp-proxy, collector") {
		t.Errorf("error should list the valid names: %s", out)
	}
}

// With no ledger yet, aimux cost answers with an empty, complete report and exits 0.
func TestCostJSON(t *testing.T) {
	cmd := exec.Command(binaryPath, "cost", "--json")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cost --json failed: %v\n%s", err, out)
	}
	var report struct {
		Rows       []json.RawMessage `json:"rows"`
		Incomplete bool              `json:"incomplete"`
		Total      struct {
			Requests int `json:"requests"`
		} `json:"total"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if report.Rows == nil || len(report.Rows) != 0 || report.Incomplete || report.Total.Requests != 0 {
		t.Fatalf("unexpected report: %s", out)
	}
}

func TestCostBadBy(t *testing.T) {
	out, err := exec.Command(binaryPath, "cost", "--by", "week").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "session, model or day") {
		t.Fatalf("cost --by week: %v\n%s", err, out)
	}
}

// setup --check on a home with no parts section says aimux manages nothing, as JSON too, and exits 0; with a
// section it cannot use, it lists the problems and exits non-zero without changing anything.
func TestSetupCheckJSON(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(binaryPath, "setup", "--check", "--json")
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("setup --check --json failed: %v\n%s", err, out)
	}
	var report struct {
		Stated  bool              `json:"stated"`
		Changes bool              `json:"changes"`
		Plan    []json.RawMessage `json:"plan"`
	}
	if err := json.Unmarshal(out, &report); err != nil || report.Stated || report.Changes || report.Plan == nil || len(report.Plan) != 0 {
		t.Fatalf("unexpected report (%v): %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, ".aimux", "config.yaml")
	if err := os.WriteFile(config, []byte("parts:\n  inference-proxy:\n    port: 70000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(binaryPath, "setup", "--check")
	cmd.Env = append(os.Environ(), "HOME="+home)
	text, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(text), "line 3") || !strings.Contains(string(text), "enabled") {
		t.Fatalf("setup --check with a broken section: %v\n%s", err, text)
	}
	if after, _ := os.ReadFile(config); string(after) != "parts:\n  inference-proxy:\n    port: 70000\n" { // #nosec G304 -- the test's own file
		t.Error("a check changed the config file")
	}
	both := exec.Command(binaryPath, "setup", "--check", "--apply")
	both.Env = append(os.Environ(), "HOME="+home)
	if text, err := both.CombinedOutput(); err == nil || !strings.Contains(string(text), "give one") {
		t.Fatalf("setup --check --apply: %v\n%s", err, text)
	}
}
