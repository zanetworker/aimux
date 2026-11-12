package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/terminput"
)

func TestResolveRemoteSettings_Defaults(t *testing.T) {
	s, err := resolveRemoteSettings(config.RemoteAccessConfig{AllowedLogins: []string{"me@example.com"}}, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.port != 7382 || s.adapter != defaultClaudeAdapter || s.maxRuns != 2 || s.approvalTimeout != 10*time.Minute {
		t.Fatalf("defaults %+v", s)
	}
}

func TestResolveRemoteSettings_FlagsOverrideConfig(t *testing.T) {
	cfg := config.RemoteAccessConfig{Port: 7400, AllowedLogins: []string{"a@x"}, MaxRuns: 3, ApprovalTimeout: "2m", Adapter: "my-acp --x"}
	s, err := resolveRemoteSettings(cfg, 7500, []string{"b@x"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.port != 7500 || len(s.allowed) != 1 || s.allowed[0] != "b@x" || s.maxRuns != 3 || s.approvalTimeout != 2*time.Minute || s.adapter != "my-acp --x" {
		t.Fatalf("got %+v", s)
	}
}

func TestResolveRemoteSettings_Errors(t *testing.T) {
	if _, err := resolveRemoteSettings(config.RemoteAccessConfig{}, 0, nil, false); err == nil || !strings.Contains(err.Error(), "allowed_logins") {
		t.Fatalf("empty allowlist: %v, want an error naming remote_access.allowed_logins", err)
	}
	bad := config.RemoteAccessConfig{AllowedLogins: []string{"a@x"}, ApprovalTimeout: "soon"}
	if _, err := resolveRemoteSettings(bad, 0, nil, false); err == nil || !strings.Contains(err.Error(), "approval_timeout") {
		t.Fatalf("bad duration: %v", err)
	}
}

// A config file that does not parse must stop the server with that error,
// not run it on defaults the user never chose.
func TestRemoteServe_MalformedConfigIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte("remote_access: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newRemoteServeCmd()
	c.SetArgs([]string{"--allow", "you@example.com", "--port", "0"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("got %v, want an error naming the config file", err)
	}
}

type checkDriver struct {
	panes   map[string]bool
	err     error
	located []string
}

func (d *checkDriver) Name() string { return "fake" }

func (d *checkDriver) Locate(_ context.Context, t terminput.Target) (terminput.Pane, bool, error) {
	d.located = append(d.located, t.TTY)
	return terminput.Pane{Driver: "fake", ID: t.TTY}, d.panes[t.TTY], d.err
}

func (d *checkDriver) Type(context.Context, terminput.Pane, string) error {
	return errors.New("check must not send")
}

func (d *checkDriver) Key(context.Context, terminput.Pane, terminput.Key) error {
	return errors.New("check must not press keys")
}

func (d *checkDriver) Screen(context.Context, terminput.Pane) (string, error) {
	return "", errors.New("check must not read screens")
}

func checkFixture(probe error, drv *checkDriver) terminalCheck {
	ttys := map[int]string{10: "/dev/ttys001", 11: "", 12: "/dev/ttys002"}
	return terminalCheck{
		Enabled: true,
		Sessions: []terminalSession{
			{ID: "0123456789abcdef-aaaa", PID: 10},
			{ID: "fedcba9876543210-bbbb", PID: 11},
			{ID: "5555666677778888-cccc", PID: 12},
		},
		TTY:    func(pid int) (string, error) { return ttys[pid], nil },
		Probe:  func(context.Context) error { return probe },
		Driver: drv,
	}
}

func TestTerminalInputCheck_ReportsEachSession(t *testing.T) {
	drv := &checkDriver{panes: map[string]bool{"/dev/ttys001": true}}
	var out strings.Builder
	if err := runTerminalInputCheck(context.Background(), &out, checkFixture(nil, drv)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"Terminal input: on\n",
		"iTerm2 Automation: allowed\n",
		"Open Claude sessions: 3 (one row per session, with the pid aimux types into)\n",
		"  01234567  pid 10      /dev/ttys001  reachable\n",
		"  fedcba98  pid 11      -             not in a terminal\n",
		"  55556666  pid 12      /dev/ttys002  not in iTerm2\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "0123456789abcdef") {
		t.Errorf("full session id printed:\n%s", got)
	}
	if strings.Join(drv.located, ",") != "/dev/ttys001,/dev/ttys002" {
		t.Errorf("located %v", drv.located)
	}
}

func TestTerminalInputCheck_DisabledStillProbes(t *testing.T) {
	probed := false
	c := checkFixture(nil, &checkDriver{})
	c.Enabled, c.Sessions = false, nil
	c.Probe = func(context.Context) error { probed = true; return nil }
	var out strings.Builder
	if err := runTerminalInputCheck(context.Background(), &out, c); err != nil {
		t.Fatal(err)
	}
	if !probed {
		t.Fatal("probe not run while disabled: macOS should still ask for Automation")
	}
	if got := out.String(); !strings.Contains(got, "Terminal input: off (set remote_access.terminal_input.enabled: true") || !strings.Contains(got, "Open Claude sessions: 0 (") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestTerminalInputCheck_AutomationDenied(t *testing.T) {
	drv := &checkDriver{}
	var out strings.Builder
	if err := runTerminalInputCheck(context.Background(), &out, checkFixture(terminput.ErrAutomationDenied, drv)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "iTerm2 Automation: denied") || !strings.Contains(got, "Privacy & Security > Automation") {
		t.Fatalf("output:\n%s", got)
	}
	if !strings.Contains(got, "  01234567  pid 10      /dev/ttys001  unknown (Automation denied)\n") || len(drv.located) != 0 {
		t.Fatalf("sessions should not be located when denied: %v\n%s", drv.located, got)
	}
}

func TestTerminalInputCheck_ProbeAndLocateErrors(t *testing.T) {
	drv := &checkDriver{err: errors.New("helper exited")}
	var out strings.Builder
	if err := runTerminalInputCheck(context.Background(), &out, checkFixture(errors.New("helper exited"), drv)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "iTerm2 Automation: error: helper exited\n") || !strings.Contains(got, "  01234567  pid 10      /dev/ttys001  error: helper exited\n") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestAimuxAncestor(t *testing.T) {
	parents := map[int]int{30: 20, 20: 10, 10: 1, 31: 21, 21: 1}
	comms := map[int]string{30: "claude", 20: "node", 10: "/usr/local/bin/aimux", 31: "claude", 21: "-zsh"}
	parent := func(p int) int { return parents[p] }
	comm := func(p int) string { return comms[p] }
	if !aimuxAncestor(30, parent, comm) {
		t.Error("claude under aimux not owned")
	}
	if aimuxAncestor(31, parent, comm) {
		t.Error("claude under a shell owned")
	}
	loop := func(p int) int { return p } // a cycle must end
	if aimuxAncestor(31, loop, comm) {
		t.Error("cycle")
	}
}

func TestOpenTerminalSessions(t *testing.T) {
	dir := t.TempDir()
	me := os.Getpid()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.json", fmt.Sprintf(`{"pid":%d,"sessionId":"live-mine"}`, me))
	write("b.json", `{"pid":999999999,"sessionId":"dead"}`)
	write("c.json", fmt.Sprintf(`{"pid":%d,"sessionId":"owned"}`, os.Getppid()))
	got := openTerminalSessions(discovery.NewLiveCache(dir, 0), func(pid int) bool { return pid == os.Getppid() })
	if len(got) != 1 || got[0].ID != "live-mine" || got[0].PID != me {
		t.Fatalf("got %+v, want only the live, not aimux-owned session", got)
	}
}

// fixedLive is a harness.LiveSessions with fixed holders.
type fixedLive []harness.Holder

func (f fixedLive) Holders(harness.Mode) ([]harness.Holder, error) { return f, nil }

func TestOpenTerminalSessions_OneRowPerSessionWithTheServersPID(t *testing.T) {
	got := openTerminalSessions(fixedLive{
		{PID: 9, SessionID: "shared"},
		{PID: 5, SessionID: "shared"},
		{PID: 7, SessionID: "other"},
	}, func(int) bool { return false })
	want := []terminalSession{{ID: "other", PID: 7}, {ID: "shared", PID: 9}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v: one row per session, the highest pid as the server types into", got, want)
	}
}

func TestTerminalInputCheck_ErrorsCutToOneLine(t *testing.T) {
	long := "execution error: " + strings.Repeat("x", 300) + "\nsecond line"
	drv := &checkDriver{err: errors.New("first\nsecond")}
	var out strings.Builder
	if err := runTerminalInputCheck(context.Background(), &out, checkFixture(errors.New(long), drv)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "second") {
		t.Fatalf("later lines printed:\n%s", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "iTerm2 Automation: error: ") && len(l) != len("iTerm2 Automation: error: ")+200 {
			t.Fatalf("automation error not cut to 200 chars: %d", len(l))
		}
	}
	if !strings.Contains(got, "  01234567  pid 10      /dev/ttys001  error: first\n") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestTerminalInputCheck_NotOnMacOS(t *testing.T) {
	old := terminalGOOS
	t.Cleanup(func() { terminalGOOS = old })
	terminalGOOS = "linux"
	t.Setenv("HOME", t.TempDir()) // nothing at the Mac is read
	cmd := newRemoteTerminalInputCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("check on linux: %v, want exit 0", err)
	}
	if got := out.String(); !strings.Contains(got, "needs macOS and iTerm2") {
		t.Fatalf("output %q", got)
	}
}

// runRemote runs `aimux remote <args>` with HOME at a temp dir and returns
// what it printed.
func runRemote(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HOME", home)
	c := newRemoteCmd()
	var out strings.Builder
	c.SetOut(&out)
	c.SetErr(io.Discard)
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), err
}

var pairCodeRE = regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}\b`)

func TestRemotePair_PrintsACodeAndStoresOnlyItsHash(t *testing.T) {
	home := t.TempDir()
	out, err := runRemote(t, home, "pair", "--name", "phone")
	if err != nil {
		t.Fatal(err)
	}
	code := pairCodeRE.FindString(out)
	if code == "" {
		t.Fatalf("no XXXX-XXXX code in %q", out)
	}
	if !strings.Contains(out, "phone") || !strings.Contains(out, "5 minutes") {
		t.Fatalf("output %q should name the device and the 5 minute limit", out)
	}
	b, err := os.ReadFile(filepath.Join(home, ".aimux", "remote-pairing.json")) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), code) || strings.Contains(string(b), strings.ReplaceAll(code, "-", "")) {
		t.Fatalf("the pairing file holds the code: %s", b)
	}
	hash, err := devicelogin.ParseCode(code)
	if err != nil {
		t.Fatal(err)
	}
	store := devicelogin.Open(filepath.Join(home, ".aimux"))
	name, ok, err := store.TakePending(hash, time.Now())
	if !ok || err != nil || name != "phone" {
		t.Fatalf("the printed code does not take the pending pairing: %q %v %v", name, ok, err)
	}
}

func TestRemotePair_Expiry(t *testing.T) {
	home := t.TempDir()
	out, err := runRemote(t, home, "pair", "--name", "phone")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := devicelogin.ParseCode(pairCodeRE.FindString(out))
	if _, ok, _ := devicelogin.Open(filepath.Join(home, ".aimux")).TakePending(hash, time.Now().Add(5*time.Minute+time.Second)); ok {
		t.Fatal("the code still worked after 5 minutes")
	}
}

func TestRemotePair_Refuses(t *testing.T) {
	home := t.TempDir()
	if err := devicelogin.Open(filepath.Join(home, ".aimux")).Add(pairedDevice("phone")); err != nil {
		t.Fatal(err)
	}
	for what, args := range map[string][]string{
		"no name":       {"pair"},
		"empty name":    {"pair", "--name", ""},
		"33 characters": {"pair", "--name", strings.Repeat("x", 33)},
		"name in use":   {"pair", "--name", "phone"},
	} {
		out, err := runRemote(t, home, args...)
		if err == nil {
			t.Errorf("%s: pair succeeded", what)
		}
		if pairCodeRE.MatchString(out) {
			t.Errorf("%s: printed a code anyway: %q", what, out)
		}
	}
}

func pairedDevice(name string) devicelogin.Device {
	return devicelogin.Device{ID: "id-" + name, Name: name, Kind: devicelogin.KindSecureEnclave, PublicKey: "pk", Login: "me@example.com",
		Created: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
}

func TestRemoteDevices_ListsNameKindCreatedLastSeen(t *testing.T) {
	home := t.TempDir()
	store := devicelogin.Open(filepath.Join(home, ".aimux"))
	web := pairedDevice("browser")
	web.Kind = devicelogin.KindWebAuthn
	for _, d := range []devicelogin.Device{pairedDevice("phone"), web} {
		if err := store.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	seen := time.Date(2026, 10, 28, 11, 30, 0, 0, time.UTC)
	store.Seen("id-phone", seen)
	out, err := runRemote(t, home, "devices")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and 2 rows, got %q", out)
	}
	for _, h := range []string{"NAME", "KIND", "CREATED", "LAST SEEN"} {
		if !strings.Contains(lines[0], h) {
			t.Errorf("header %q lacks %s", lines[0], h)
		}
	}
	created := pairedDevice("phone").Created.Local().Format("2006-01-02 15:04")
	if f := strings.Fields(lines[1]); len(f) != 6 || f[0] != "phone" || f[1] != "secure-enclave" || f[2]+" "+f[3] != created || f[4]+" "+f[5] != seen.Local().Format("2006-01-02 15:04") {
		t.Errorf("phone row %q", lines[1])
	}
	if f := strings.Fields(lines[2]); len(f) != 5 || f[0] != "browser" || f[1] != "webauthn" || f[4] != "-" {
		t.Errorf("browser row %q (never seen shows -)", lines[2])
	}
}

func TestRemoteDevices_NoneSaysHowToPair(t *testing.T) {
	out, err := runRemote(t, t.TempDir(), "devices")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "aimux remote pair --name") {
		t.Fatalf("output %q", out)
	}
}

func TestRemoteDevices_CorruptFileIsAnError(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aimux", "remote-paired-devices.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runRemote(t, home, "devices"); err == nil || !strings.Contains(err.Error(), "remote-paired-devices.json") {
		t.Fatalf("got %v, want an error naming the file", err)
	}
}

func TestRemoteRevoke(t *testing.T) {
	home := t.TempDir()
	store := devicelogin.Open(filepath.Join(home, ".aimux"))
	for _, n := range []string{"phone", "browser"} {
		if err := store.Add(pairedDevice(n)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runRemote(t, home, "revoke", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "phone") {
		t.Fatalf("output %q", out)
	}
	left, _ := store.Devices()
	if len(left) != 1 || left[0].Name != "browser" {
		t.Fatalf("left %+v", left)
	}
	_, err = runRemote(t, home, "revoke", "phone")
	if err == nil || !strings.Contains(err.Error(), `"phone"`) || !strings.Contains(err.Error(), "aimux remote devices") {
		t.Fatalf("unknown name: %v, want an error naming it and pointing at aimux remote devices", err)
	}
	if _, err := runRemote(t, home, "revoke"); err == nil {
		t.Fatal("revoke without a name succeeded")
	}
}

// Push registrations live in remote-devices.json; pairing must never touch it.
func TestRemotePairAndRevoke_LeaveThePushStoreAlone(t *testing.T) {
	home := t.TempDir()
	pushPath := filepath.Join(home, ".aimux", "remote-devices.json")
	ps := &push.DeviceStore{Path: pushPath}
	if err := ps.Add(push.Device{Token: strings.Repeat("ab", 32), Environment: push.EnvSandbox, Login: "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pushPath) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runRemote(t, home, "pair", "--name", "phone"); err != nil {
		t.Fatal(err)
	}
	store := devicelogin.Open(filepath.Join(home, ".aimux"))
	if err := store.Add(pairedDevice("phone")); err != nil {
		t.Fatal(err)
	}
	store.Seen("id-phone", time.Now())
	if _, err := runRemote(t, home, "devices"); err != nil {
		t.Fatal(err)
	}
	if _, err := runRemote(t, home, "revoke", "phone"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(pushPath) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("push store changed:\nbefore %s\nafter  %s", before, after)
	}
	if ds, err := ps.List(); err != nil || len(ds) != 1 {
		t.Fatalf("push devices %+v, %v", ds, err)
	}
}

func TestRemotePair_NoticeWhenDeviceLoginIsOff(t *testing.T) {
	for _, tt := range []struct {
		config string
		notice bool
	}{{"", false}, {"remote_access:\n  device_login: false\n", true}} {
		home := t.TempDir()
		if tt.config != "" {
			if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte(tt.config), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out, err := runRemote(t, home, "pair", "--name", "phone")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out, "device_login"); got != tt.notice {
			t.Errorf("config %q: notice shown = %v, want %v; output %q", tt.config, got, tt.notice, out)
		}
		hash, _ := devicelogin.ParseCode(pairCodeRE.FindString(out))
		if _, ok, _ := devicelogin.Open(filepath.Join(home, ".aimux")).TakePending(hash, time.Now()); !ok {
			t.Errorf("config %q: the code was not written", tt.config)
		}
	}
}

func TestRemoteRevoke_WritesTheAuditLine(t *testing.T) {
	home := t.TempDir()
	store := devicelogin.Open(filepath.Join(home, ".aimux"))
	d := pairedDevice("phone")
	if err := store.Add(d); err != nil {
		t.Fatal(err)
	}
	if _, err := runRemote(t, home, "revoke", "phone"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".aimux", "logs", "remote-audit.log")
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("audit log %v %v, want mode 0600", fi, err)
	}
	raw, _ := os.ReadFile(path) // #nosec G304 -- test temp dir
	var e devicelogin.AuditEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("audit %q: %v", raw, err)
	}
	if e.Action != devicelogin.ActionRevoke || e.Device != "phone" || e.Login != d.Login || e.Result != "ok" || e.Time.IsZero() {
		t.Fatalf("audit line %+v", e)
	}
	if strings.Contains(string(raw), d.PublicKey) || strings.Contains(string(raw), d.ID) {
		t.Fatalf("audit line holds the key or id: %s", raw)
	}
}

// --local-only serves only local sockets, so nobody need be allowed on the
// tailnet; other settings still apply.
func TestServe_LocalOnlySettingsNeedNoAllow(t *testing.T) {
	s, err := resolveRemoteSettings(config.RemoteAccessConfig{ApprovalTimeout: "2m"}, 0, nil, true)
	if err != nil {
		t.Fatalf("local-only without allowed logins: %v", err)
	}
	if s.approvalTimeout != 2*time.Minute || len(s.allowed) != 0 {
		t.Fatalf("got %+v", s)
	}
	if _, err := resolveRemoteSettings(config.RemoteAccessConfig{ApprovalTimeout: "soon"}, 0, nil, true); err == nil {
		t.Fatal("a bad duration passed under --local-only")
	}
}

// remote serve reads the clients file (clientscope) from the HOME it runs
// under: a malformed one stops it, naming the file.
func TestServe_MalformedClientsFileIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "aimux")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clients.yaml"), []byte("clients: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newRemoteServeCmd()
	c.SetArgs([]string{"--local-only"})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "clients.yaml") {
		t.Fatalf("got %v, want an error naming clients.yaml", err)
	}
	for _, f := range []string{"local-only", "unsafe-no-device-login"} {
		if c.Flags().Lookup(f) == nil {
			t.Errorf("no --%s flag", f)
		}
	}
}
