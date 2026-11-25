package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/zanetworker/aimux/internal/parts"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// A hand-written job file, as the model proxy's and the collector's are.
const upgradeTestPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.aimux.model-proxy</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/me/go/bin/aimux</string>
        <string>model</string>
        <string>proxy</string>
        <string>serve</string>
    </array>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`

// launchctlLog answers launchctl as a loaded job would and keeps the commands it was given.
type launchctlLog struct{ calls []string }

func (l *launchctlLog) run(name string, args ...string) ([]byte, error) {
	l.calls = append(l.calls, name+" "+strings.Join(args, " "))
	if len(args) > 0 && args[0] == "print" {
		// After a bootout the job is gone until the next bootstrap.
		for i := len(l.calls) - 2; i >= 0; i-- {
			if strings.Contains(l.calls[i], " bootstrap ") {
				break
			}
			if strings.Contains(l.calls[i], " bootout ") {
				return []byte("Could not find service"), errors.New("exit status 113")
			}
		}
		return []byte("state = running\n\tpid = 42\n"), nil
	}
	return nil, nil
}

func testUpgradeJob(t *testing.T, plist string) (launchdJob, *launchctlLog, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "com.aimux.model-proxy.plist")
	if plist != "" {
		if err := os.WriteFile(path, []byte(plist), 0o644); err != nil { // #nosec G306 -- launchd reads job files 0644
			t.Fatal(err)
		}
	}
	log := &launchctlLog{}
	return launchdJob{agent: mcpproxy.Agent{Label: "com.aimux.model-proxy", PlistPath: path, UID: 501, Run: log.run}}, log, path
}

func TestLaunchdJob_ProgramReadsTheJobFile(t *testing.T) {
	job, _, _ := testUpgradeJob(t, upgradeTestPlist)
	if got, err := job.Program(); err != nil || got != "/Users/me/go/bin/aimux" {
		t.Fatalf("Program() = %q, %v", got, err)
	}

	missing, _, _ := testUpgradeJob(t, "")
	if got, err := missing.Program(); err != nil || got != "" {
		t.Fatalf("Program() without a job file = %q, %v; want empty and no error", got, err)
	}

	broken, _, _ := testUpgradeJob(t, "<plist><dict></dict></plist>")
	if _, err := broken.Program(); err == nil || !strings.Contains(err.Error(), "names no program") {
		t.Fatalf("Program() on a job file without a program: %v", err)
	}
}

// Pointing a job changes only the program in its file and reloads it, so launchd reads the new path.
func TestLaunchdJob_PointRewritesOnlyTheProgramAndReloads(t *testing.T) {
	job, log, path := testUpgradeJob(t, upgradeTestPlist)
	own := "/Users/me/.aimux/services/model-proxy/aimux"

	if err := job.Point(own); err != nil {
		t.Fatalf("Point: %v", err)
	}

	got, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(upgradeTestPlist, "/Users/me/go/bin/aimux", own, 1); string(got) != want {
		t.Fatalf("job file after Point:\n%s\nwant:\n%s", got, want)
	}
	joined := strings.Join(log.calls, "\n")
	bootout := strings.Index(joined, "launchctl bootout gui/501/com.aimux.model-proxy")
	bootstrap := strings.Index(joined, "launchctl bootstrap gui/501 "+path)
	if bootout < 0 || bootstrap < bootout {
		t.Fatalf("the job was not reloaded from its file (bootout, then bootstrap):\n%s", joined)
	}
}

// A restart reloads the job from its file, so launchd runs what the file says.
func TestLaunchdJob_RestartReloadsTheJob(t *testing.T) {
	job, log, path := testUpgradeJob(t, upgradeTestPlist)
	if err := job.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	joined := strings.Join(log.calls, "\n")
	bootout := strings.Index(joined, "launchctl bootout gui/501/com.aimux.model-proxy")
	bootstrap := strings.Index(joined, "launchctl bootstrap gui/501 "+path)
	start := strings.Index(joined, "launchctl kickstart gui/501/com.aimux.model-proxy")
	if bootout < 0 || bootstrap < bootout || start < bootstrap || strings.Contains(joined, "kickstart -k") {
		t.Fatalf("want a bootout, a bootstrap, then a start that kills nothing:\n%s", joined)
	}
	if got, _ := os.ReadFile(path); string(got) != upgradeTestPlist { // #nosec G304 -- test temp file
		t.Fatal("a restart changed the job file")
	}
}

// What launchd has loaded wins over the job file: after a move interrupted between the two, they differ.
func TestLaunchdJob_ProgramIsWhatLaunchdLoaded(t *testing.T) {
	job, _, _ := testUpgradeJob(t, upgradeTestPlist)
	job.agent.Run = func(string, ...string) ([]byte, error) {
		return []byte("gui/501/com.aimux.model-proxy = {\n\tstate = running\n\tprogram = /old/place/aimux\n\targuments = {\n"), nil
	}
	if got, err := job.Program(); err != nil || got != "/old/place/aimux" {
		t.Fatalf("Program() = %q, %v; want the loaded program", got, err)
	}
}

// Pointing a job at the program its file already names still reloads it, and a job with a Program key is refused.
func TestLaunchdJob_PointReloadsAndRefusesAProgramKey(t *testing.T) {
	job, log, _ := testUpgradeJob(t, upgradeTestPlist)
	if err := job.Point("/Users/me/go/bin/aimux"); err != nil {
		t.Fatalf("Point at the program already named: %v", err)
	}
	if joined := strings.Join(log.calls, "\n"); !strings.Contains(joined, " bootstrap ") {
		t.Fatalf("the job was not reloaded:\n%s", joined)
	}

	withProgram := strings.Replace(upgradeTestPlist, "<key>ProgramArguments</key>", "<key>Program</key>\n    <string>/elsewhere/aimux</string>\n    <key>ProgramArguments</key>", 1)
	job, log, path := testUpgradeJob(t, withProgram)
	err := job.Point("/Users/me/.aimux/services/model-proxy/aimux")
	if err == nil || !strings.Contains(err.Error(), "sets Program") {
		t.Fatalf("Point on a job with a Program key: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != withProgram || len(log.calls) != 0 { // #nosec G304 -- test temp file
		t.Fatal("a refused job was changed or reloaded")
	}
}

func TestUpgradeTargets_SelectsByName(t *testing.T) {
	all, err := upgradeTargets(nil)
	if err != nil || strings.Join(all, ",") != "model-proxy,mcp-proxy,collector" {
		t.Fatalf("no names: %v, %v; want the three services in their fixed order", all, err)
	}
	one, err := upgradeTargets([]string{"collector"})
	if err != nil || strings.Join(one, ",") != "collector" {
		t.Fatalf("one name: %v, %v", one, err)
	}
	ordered, err := upgradeTargets([]string{"collector", "model-proxy", "collector"})
	if err != nil || strings.Join(ordered, ",") != "model-proxy,collector" {
		t.Fatalf("names out of order and repeated: %v, %v", ordered, err)
	}

	_, err = upgradeTargets([]string{"modelproxy"})
	if err == nil || !strings.Contains(err.Error(), `unknown service "modelproxy"`) || !strings.Contains(err.Error(), "model-proxy, mcp-proxy, collector") {
		t.Fatalf("unknown name: %v; want the valid names listed", err)
	}
	// The one service upgrade does not move yet says how it is updated instead.
	_, err = upgradeTargets([]string{"tool-gateway"})
	if err == nil || !strings.Contains(err.Error(), "aimux mcp proxy gateway install") {
		t.Fatalf("tool-gateway: %v; want its own command named", err)
	}
}

func upgradeTestResults() []controller.UpgradeResult {
	return []controller.UpgradeResult{
		{ID: "model-proxy", Name: "model proxy", Outcome: controller.UpgradeDone, From: "v0.14.0-194-gabc1234", To: "v0.14.0-196-gdef5678",
			MovedFrom: "/Users/me/go/bin/aimux"},
		{ID: "collector", Name: "collector", Outcome: controller.UpgradeCurrent, From: "v0.14.0-196-gdef5678", To: "v0.14.0-196-gdef5678"},
	}
}

func TestWriteUpgrade_Table(t *testing.T) {
	var out bytes.Buffer
	err := writeUpgrade(&out, "v0.14.0-196-gdef5678", "/Users/me/.aimux/services", upgradeTestResults(), nil, false, false)
	if err != nil {
		t.Fatalf("a successful upgrade returned %v", err)
	}
	want := `aimux upgrade to build 196

SERVICE      RESULT           BUILD
model proxy  upgraded         194 -> 196
collector    already current  196

NOTES
model proxy: its launchd job ran /Users/me/go/bin/aimux; it now runs /Users/me/.aimux/services/model-proxy/aimux
tool gateway: not moved by upgrade yet; update it with: aimux mcp proxy gateway install
`
	if out.String() != want {
		t.Fatalf("table:\n%s\nwant:\n%s", out.String(), want)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if len(line) > 120 {
			t.Fatalf("line over 120 columns: %q", line)
		}
	}
}

// A dry run uses the same rows in the future tense and says nothing was changed.
func TestWriteUpgrade_DryRun(t *testing.T) {
	var out bytes.Buffer
	if err := writeUpgrade(&out, "v0.14.0-196-gdef5678", "/s", upgradeTestResults(), nil, true, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"aimux upgrade to build 196 (dry run, nothing was changed)", "would upgrade", "would now run /s/model-proxy/aimux"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry run output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "  upgraded") {
		t.Fatalf("a dry run reported an upgrade as done:\n%s", got)
	}
}

// A failure is an error for the exit code, and its row says whether the previous program is back.
func TestWriteUpgrade_FailureIsAnError(t *testing.T) {
	results := []controller.UpgradeResult{
		{ID: "model-proxy", Name: "model proxy", Outcome: controller.UpgradeFailed, From: "v0.14.0-194-gabc1234", To: "v0.14.0-196-gdef5678",
			Problem: "model proxy did not come up on build v0.14.0-196-gdef5678 within 20s (not running)", Restored: true},
		{ID: "collector", Name: "collector", Outcome: controller.UpgradeFailed, To: "v0.14.0-196-gdef5678", Problem: "make the service's folder: permission denied; nothing was changed"},
		{ID: "x", Name: "x", Outcome: controller.UpgradeNotInstalled, To: "v0.14.0-196-gdef5678"},
	}
	var out bytes.Buffer
	err := writeUpgrade(&out, "v0.14.0-196-gdef5678", "/s", results, nil, false, false)
	if err == nil || err.Error() != "2 services were not upgraded" {
		t.Fatalf("error %v, want the count of failed services", err)
	}
	got := out.String()
	for _, want := range []string{
		"model proxy  failed         194",
		"model proxy: model proxy did not come up on build v0.14.0-196-gdef5678 within 20s (not running); the previous program runs again",
		"collector: make the service's folder: permission denied; nothing was changed",
		"x            not installed  -",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestWriteUpgrade_JSON(t *testing.T) {
	var out bytes.Buffer
	if err := writeUpgrade(&out, "v0.14.0-196-gdef5678", "/s", upgradeTestResults(), nil, true, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Build    string                          `json:"build"`
		DryRun   bool                            `json:"dry_run"`
		Services []controller.UpgradeResult      `json:"services"`
		NotMoved []struct{ Name, Update string } `json:"not_moved"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if got.Build != "v0.14.0-196-gdef5678" || !got.DryRun || len(got.Services) != 2 || got.Services[0] != upgradeTestResults()[0] {
		t.Fatalf("JSON %+v", got)
	}
	if len(got.NotMoved) != 1 || got.NotMoved[0].Name != "tool gateway" || got.NotMoved[0].Update != "aimux mcp proxy gateway install" {
		t.Fatalf("not_moved %+v", got.NotMoved)
	}
}

// A service asked for by name that has no launchd job is a failure; in an upgrade of everything it is only skipped.
func TestWriteUpgrade_NamedServiceNotInstalledIsAnError(t *testing.T) {
	results := []controller.UpgradeResult{{ID: "collector", Name: "collector", Outcome: controller.UpgradeNotInstalled, To: "v1"}}
	var out bytes.Buffer
	if err := writeUpgrade(&out, "v1", "/s", results, nil, false, false); err != nil {
		t.Fatalf("an upgrade of everything failed on a service that is not installed: %v", err)
	}
	out.Reset()
	err := writeUpgrade(&out, "v1", "/s", results, []string{"collector"}, false, false)
	if err == nil || err.Error() != "1 service was not upgraded" {
		t.Fatalf("error %v, want a failure for the named service", err)
	}
	if !strings.Contains(out.String(), "collector: it has no launchd job, so there is nothing to upgrade") {
		t.Fatalf("output does not say why:\n%s", out.String())
	}
}

// The upgrade takes the promote script's lock for the model proxy, and is refused while it is held.
func TestHoldPromotionLock(t *testing.T) {
	home := t.TempDir()
	lock := filepath.Join(home, "go", "bin", ".aimux-model-proxy-promotion")
	release, err := holdPromotionLock(home)
	if err != nil {
		t.Fatalf("taking the free lock: %v", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("the promote script's lock folder was not made: %v", err)
	}
	if _, err := holdPromotionLock(home); err == nil || !strings.Contains(err.Error(), "in progress") || !strings.Contains(err.Error(), lock) {
		t.Fatalf("taking a held lock: %v; want a refusal naming the lock", err)
	}
	release()
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lock was not released: %v", err)
	}
	if release, err = holdPromotionLock(home); err != nil {
		t.Fatalf("taking the lock again after release: %v", err)
	}
	release()
}

// Once aimux upgrade has moved the MCP proxy to its own copy, a later install keeps its job there instead of
// pointing it back at the CLI that ran the install.
func TestProxyAgentBinary(t *testing.T) {
	cli, own := "/Users/me/go/bin/aimux", "/Users/me/.aimux/services/mcp-proxy/aimux"
	job, _, path := testUpgradeJob(t, upgradeTestPlist) // runs /Users/me/go/bin/aimux
	if got := proxyAgentBinary(path, own, cli); got != cli {
		t.Fatalf("a job on the CLI: %q, want the CLI that runs the install", got)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(upgradeTestPlist, cli, own, 1)), 0o644); err != nil { // #nosec G306 -- launchd reads job files 0644
		t.Fatal(err)
	}
	if got := proxyAgentBinary(path, own, cli); got != own {
		t.Fatalf("a job already on its own copy: %q, want it kept there", got)
	}
	missing := filepath.Join(t.TempDir(), "none.plist")
	if got := proxyAgentBinary(missing, own, cli); got != cli {
		t.Fatalf("no job yet: %q, want the CLI", got)
	}
	_ = job
}

// aimux upgrade waits on a service's status probe. A managed service the registry has and status does not probe
// would be chosen for upgrade with nothing to wait on.
func TestEveryManagedServiceHasAStatusProbe(t *testing.T) {
	probes := map[string]bool{}
	for _, probe := range defaultStatusProbes(statusEnv{}) {
		probes[probe.Name] = true
	}
	for _, service := range parts.Services() {
		if service.Managed && !probes[service.Name] {
			t.Errorf("the registry's %s has no status probe named %q", service.ID, service.Name)
		}
	}
	if len(upgradeServices) != 3 || len(notMoved) != 1 || notMoved[0].ID != "tool-gateway" {
		t.Errorf("upgrade moves %d services and leaves %+v; want three moved and the tool gateway left", len(upgradeServices), notMoved)
	}
}
