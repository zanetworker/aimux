package mcpproxy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func switchAgent(t *testing.T, l *laggyLaunchctl, binary string) Agent {
	t.Helper()
	return Agent{
		Label: "com.aimux.mcp-proxy", PlistPath: filepath.Join(t.TempDir(), "com.aimux.mcp-proxy.plist"),
		Binary: binary, LogDir: "/logs", Args: []string{"mcp", "proxy", "serve", "--port", "7381"},
		UID: 501, Run: l.run, Sleep: func(time.Duration) {},
		Env: map[string]string{"PATH": "/old/path", "HOME": "/h"},
	}
}

// A re-run from another binary rewrites only the program path and restarts
// the job (bootout then bootstrap: kickstart -k would keep the old program).
func TestAgent_SwitchBinary_RewritesTheProgramAndRestarts(t *testing.T) {
	l := &laggyLaunchctl{loaded: true, lag: 3}
	old := switchAgent(t, l, "/old/bin/aimux & co")
	must(t, os.WriteFile(old.PlistPath, old.Plist(), 0o600))

	a := old
	a.Binary = "/new/bin/aimux"
	a.Env = map[string]string{"PATH": "/another/path"} // not written: only the program changes
	was, _, err := a.SwitchBinary()
	must(t, err)
	if was != "/old/bin/aimux & co" {
		t.Fatalf("was = %q, want the old program", was)
	}
	if got := ReadPlistBinary(a.PlistPath); got != "/new/bin/aimux" {
		t.Fatalf("plist program = %q, want /new/bin/aimux", got)
	}
	env, _, err := ReadPlistEnv(a.PlistPath)
	must(t, err)
	if !reflect.DeepEqual(env, old.Env) {
		t.Fatalf("env = %v, want it kept as %v", env, old.Env)
	}
	want := strings.Replace(string(old.Plist()), "/old/bin/aimux &amp; co", "/new/bin/aimux", 1)
	if b, _ := os.ReadFile(a.PlistPath); string(b) != want { // #nosec G304 -- test temp file
		t.Fatalf("plist:\n%s\nwant:\n%s", b, want)
	}
	if !slices.Contains(l.calls, "bootout") || !slices.Contains(l.calls, "bootstrap") || !l.loaded {
		t.Fatalf("calls %v, want a bootout and a bootstrap, loaded after", l.calls)
	}
}

func TestAgent_SwitchBinary_NothingToSwitch(t *testing.T) {
	l := &laggyLaunchctl{loaded: true}
	a := switchAgent(t, l, "/same/aimux")
	// No plist: nothing installed, nothing to switch.
	was, _, err := a.SwitchBinary()
	if err != nil || was != "" || len(l.calls) != 0 {
		t.Fatalf("no plist: was %q err %v calls %v", was, err, l.calls)
	}
	must(t, os.WriteFile(a.PlistPath, a.Plist(), 0o600))
	before, _ := os.ReadFile(a.PlistPath) // #nosec G304 -- test temp file
	was, _, err = a.SwitchBinary()
	after, _ := os.ReadFile(a.PlistPath) // #nosec G304 -- test temp file
	if err != nil || was != "" || len(l.calls) != 0 || string(before) != string(after) {
		t.Fatalf("same binary: was %q err %v calls %v, plist changed %v", was, err, l.calls, string(before) != string(after))
	}
	// An empty Binary (dry run, uninstall) never switches.
	a.Binary = ""
	if was, _, err := a.SwitchBinary(); err != nil || was != "" || len(l.calls) != 0 {
		t.Fatalf("empty binary: was %q err %v calls %v", was, err, l.calls)
	}
}

func TestAgent_SwitchBinary_UnreadablePlistIsAnError(t *testing.T) {
	l := &laggyLaunchctl{loaded: true}
	a := switchAgent(t, l, "/new/aimux")
	must(t, os.WriteFile(a.PlistPath, []byte("<plist><dict><key>Label</key></dict></plist>"), 0o600))
	if _, _, err := a.SwitchBinary(); err == nil {
		t.Fatal("a plist without ProgramArguments was switched silently")
	}
	if len(l.calls) != 0 {
		t.Fatalf("calls %v, want none", l.calls)
	}
}

// The live case: the proxy runs, install is run again from another binary
// with nothing new to move; the plist must follow and the job restart.
func TestInstall_RerunFromAnotherBinaryPointsTheAgentAtIt(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	if ReadPlistBinary(fx.in.Agent.PlistPath) != "/usr/local/bin/aimux" {
		t.Fatal("first install did not write the plist")
	}
	calls := len(fx.lc.calls)
	fx.in.Agent.Binary = "/opt/aimux/bin/aimux"
	must(t, fx.in.Install(nil))
	if got := ReadPlistBinary(fx.in.Agent.PlistPath); got != "/opt/aimux/bin/aimux" {
		t.Fatalf("plist program = %q after a re-run from /opt/aimux/bin/aimux", got)
	}
	later := strings.Join(fx.lc.calls[calls:], ";")
	if !strings.Contains(later, "bootout") || !strings.Contains(later, "bootstrap") {
		t.Fatalf("launchctl %v, want a restart that re-reads the plist", fx.lc.calls[calls:])
	}
	if !strings.Contains(fx.out.String(), "now runs /opt/aimux/bin/aimux") {
		t.Fatalf("output does not say the agent switched:\n%s", fx.out.String())
	}
}

// A restart that fails puts the old program back, so the next install sees
// the mismatch and tries again instead of taking the switch as done.
func TestAgent_SwitchBinary_FailedRestartKeepsItRetryable(t *testing.T) {
	l := &laggyLaunchctl{loaded: true, lag: 1_000_000}
	a := switchAgent(t, l, "/old/aimux")
	must(t, os.WriteFile(a.PlistPath, a.Plist(), 0o600))
	before, _ := os.ReadFile(a.PlistPath) // #nosec G304 -- test temp file
	a.Binary = "/new/aimux"
	if _, _, err := a.SwitchBinary(); err == nil {
		t.Fatal("a restart that never succeeds was not reported")
	}
	if after, _ := os.ReadFile(a.PlistPath); string(after) != string(before) { // #nosec G304 -- test temp file
		t.Fatalf("plist not put back after the failed restart:\n%s", after)
	}
}

// A new binary whose daemon never answers is switched back, so the proxy
// runs the old one again and the next install retries the switch.
func TestInstall_SwitchToABinaryThatNeverAnswersIsUndone(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.in.Agent.Binary = "/broken/aimux"
	fx.daemonDown = true
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "runs /usr/local/bin/aimux again") {
		t.Fatalf("err = %v, want the switch reported as undone", err)
	}
	if got := ReadPlistBinary(fx.in.Agent.PlistPath); got != "/usr/local/bin/aimux" {
		t.Fatalf("plist program = %q, want the old binary back", got)
	}
	if !fx.lc.isLoaded() {
		t.Fatal("the agent is not loaded after switching back")
	}
}

// flakyBootstrap fails the first bootstrap only: the new plist's load fails,
// loading the old one back works.
type flakyBootstrap struct {
	loaded bool
	failed bool
}

func (f *flakyBootstrap) run(_ string, args ...string) ([]byte, error) {
	switch args[0] {
	case "print":
		if f.loaded {
			return []byte("state = running"), nil
		}
		return []byte("Could not find service"), errors.New("exit status 113")
	case "bootout":
		f.loaded = false
		return nil, nil
	case "bootstrap":
		if !f.failed {
			f.failed = true
			return []byte("Bootstrap failed: 37: Operation already in progress"), errors.New("exit status 37")
		}
		f.loaded = true
		return nil, nil
	}
	return nil, errors.New("unexpected launchctl " + args[0])
}

func TestAgent_SwitchBinary_FailedBootstrapLoadsTheOldPlistAgain(t *testing.T) {
	f := &flakyBootstrap{loaded: true}
	a := switchAgent(t, &laggyLaunchctl{}, "/old/aimux")
	a.Run = f.run
	must(t, os.WriteFile(a.PlistPath, a.Plist(), 0o600))
	a.Binary = "/new/aimux"
	_, _, err := a.SwitchBinary()
	if err == nil || !strings.Contains(err.Error(), "runs /old/aimux again") {
		t.Fatalf("err = %v, want the failure and the old binary running again", err)
	}
	if !f.loaded || ReadPlistBinary(a.PlistPath) != "/old/aimux" {
		t.Fatalf("loaded %v, plist program %q: want the old plist loaded again", f.loaded, ReadPlistBinary(a.PlistPath))
	}
}

// A plist whose program reads as "" is still switched, and reported so,
// so the caller waits for the daemon and rolls back like any switch.
func TestAgent_SwitchBinary_EmptyOldProgramStillSwitched(t *testing.T) {
	l := &laggyLaunchctl{loaded: true}
	a := switchAgent(t, l, "")
	must(t, os.WriteFile(a.PlistPath, a.Plist(), 0o600)) // ProgramArguments[0] is ""
	a.Binary = "/new/aimux"
	was, switched, err := a.SwitchBinary()
	must(t, err)
	if was != "" || !switched || ReadPlistBinary(a.PlistPath) != "/new/aimux" {
		t.Fatalf("was %q switched %v program %q", was, switched, ReadPlistBinary(a.PlistPath))
	}
}

// Undoing a switch writes the plist back readable by launchd (0644), as
// every plist write does.
func TestInstall_UndoneSwitchKeepsThePlistMode(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.in.Agent.Binary = "/broken/aimux"
	fx.daemonDown = true
	if err := fx.in.Install(nil); err == nil {
		t.Fatal("want the switch undone with an error")
	}
	info, err := os.Stat(fx.in.Agent.PlistPath)
	must(t, err)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("plist mode %o after the rollback, want 644", info.Mode().Perm())
	}
}

// InstallChecked undoes an install whose bootstrap fails, as it does one
// whose daemon never answers: the old plist is back and loaded.
func TestAgent_InstallChecked_FailedBootstrapRollsBack(t *testing.T) {
	f := &flakyBootstrap{loaded: true}
	a := switchAgent(t, &laggyLaunchctl{}, "/old/aimux")
	a.Run = f.run
	must(t, os.WriteFile(a.PlistPath, a.Plist(), 0o644)) //nolint:gosec // a plist is 0644
	a.Binary = "/new/aimux"
	_, err := a.InstallChecked(func(time.Time) Readiness { return Ready })
	if err == nil || !strings.Contains(err.Error(), "runs /old/aimux again") {
		t.Fatalf("err %v, want the failed bootstrap undone", err)
	}
	if ReadPlistBinary(a.PlistPath) != "/old/aimux" || !f.loaded {
		t.Fatalf("plist %q loaded %v", ReadPlistBinary(a.PlistPath), f.loaded)
	}
}

func TestAgent_InstallChecked_ReadyAndFirstInstall(t *testing.T) {
	l := &laggyLaunchctl{}
	a := switchAgent(t, l, "/new/aimux")
	was, err := a.InstallChecked(func(time.Time) Readiness { return Ready })
	if err != nil || was != "" || ReadPlistBinary(a.PlistPath) != "/new/aimux" {
		t.Fatalf("was %q err %v", was, err)
	}
	must(t, os.Remove(a.PlistPath))
	if _, err := a.InstallChecked(func(time.Time) Readiness { return NotReady }); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("err %v, want a first install that never answers removed", err)
	}
	if _, serr := os.Stat(a.PlistPath); !os.IsNotExist(serr) {
		t.Fatalf("plist left behind: %v", serr)
	}
}

// An install that fails before replacing the plist (here its folder does
// not exist, which fails for root too) changed nothing, so nothing is
// rolled back: a loaded job whose plist is missing keeps running.
func TestAgent_InstallChecked_NothingChangedNothingUndone(t *testing.T) {
	l := &laggyLaunchctl{loaded: true}
	a := switchAgent(t, l, "/new/aimux")
	a.PlistPath = filepath.Join(t.TempDir(), "missing", "com.aimux.mcp-proxy.plist")
	if _, err := a.InstallChecked(func(time.Time) Readiness { return Ready }); err == nil {
		t.Fatal("an install into a read-only folder succeeded")
	}
	if len(l.calls) != 0 || !l.loaded {
		t.Fatalf("launchctl %v, loaded %v: want the job left alone", l.calls, l.loaded)
	}
}

func TestAgent_PID(t *testing.T) {
	f := newFakeRun()
	a := Agent{Label: "com.aimux.tool-gateway", UID: 501, Run: f.run}
	f.setOutput("launchctl print gui/501/com.aimux.tool-gateway", []byte("gui/501/com.aimux.tool-gateway = {\n\tstate = running\n\tpid = 4242\n}"), nil)
	if pid, err := a.PID(); err != nil || pid != 4242 {
		t.Fatalf("pid %d err %v", pid, err)
	}
	f.setOutput("launchctl print gui/501/com.aimux.tool-gateway", []byte("state = waiting\n"), nil)
	if pid, err := a.PID(); err != nil || pid != 0 {
		t.Fatalf("not running: pid %d err %v, want 0", pid, err)
	}
}

// A rollback check that could not tell (no time left, an older daemon that
// cannot be identified) is reported as unverified, not as a failure.
func TestAgent_InstallChecked_UnverifiedRollback(t *testing.T) {
	l := &laggyLaunchctl{loaded: true}
	a := switchAgent(t, l, "/old/aimux")
	must(t, os.WriteFile(a.PlistPath, a.Plist(), 0o644)) //nolint:gosec // a plist is 0644
	a.Binary = "/new/aimux"
	checks := []Readiness{NotReady, ReadyUnknown}
	_, err := a.InstallChecked(func(time.Time) Readiness { r := checks[0]; checks = checks[1:]; return r })
	if err == nil || !strings.Contains(err.Error(), "not verified") || strings.Contains(err.Error(), "does not answer either") {
		t.Fatalf("err %v", err)
	}
}

// launchdDown answers every launchctl call with an error: launchd cannot be
// reached at all.
type launchdDown struct{}

func (launchdDown) run(string, ...string) ([]byte, error) {
	return []byte("Bootstrap failed: 125: Domain does not support specified action"), errors.New("exit status 125")
}

// A first install launchd refuses is removed from disk even though launchd
// cannot unload it either, so no LaunchAgent loads at a later login.
func TestAgent_InstallChecked_FirstInstallPlistRemovedWhenLaunchdFails(t *testing.T) {
	a := switchAgent(t, &laggyLaunchctl{}, "/new/aimux")
	a.Run = launchdDown{}.run
	_, err := a.InstallChecked(func(time.Time) Readiness { return Ready })
	if err == nil {
		t.Fatal("want an error")
	}
	if _, serr := os.Stat(a.PlistPath); !os.IsNotExist(serr) {
		t.Fatalf("the failed first install left its plist: %v (err %v)", serr, err)
	}
}
