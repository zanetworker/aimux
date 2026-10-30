package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/terminput"
)

func TestResolveRemoteSettings_Defaults(t *testing.T) {
	s, err := resolveRemoteSettings(config.RemoteAccessConfig{AllowedLogins: []string{"me@example.com"}}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.port != 7382 || s.adapter != defaultClaudeAdapter || s.maxRuns != 2 || s.approvalTimeout != 10*time.Minute {
		t.Fatalf("defaults %+v", s)
	}
}

func TestResolveRemoteSettings_FlagsOverrideConfig(t *testing.T) {
	cfg := config.RemoteAccessConfig{Port: 7400, AllowedLogins: []string{"a@x"}, MaxRuns: 3, ApprovalTimeout: "2m", Adapter: "my-acp --x"}
	s, err := resolveRemoteSettings(cfg, 7500, []string{"b@x"})
	if err != nil {
		t.Fatal(err)
	}
	if s.port != 7500 || len(s.allowed) != 1 || s.allowed[0] != "b@x" || s.maxRuns != 3 || s.approvalTimeout != 2*time.Minute || s.adapter != "my-acp --x" {
		t.Fatalf("got %+v", s)
	}
}

func TestResolveRemoteSettings_Errors(t *testing.T) {
	if _, err := resolveRemoteSettings(config.RemoteAccessConfig{}, 0, nil); err == nil || !strings.Contains(err.Error(), "allowed_logins") {
		t.Fatalf("empty allowlist: %v, want an error naming remote_access.allowed_logins", err)
	}
	bad := config.RemoteAccessConfig{AllowedLogins: []string{"a@x"}, ApprovalTimeout: "soon"}
	if _, err := resolveRemoteSettings(bad, 0, nil); err == nil || !strings.Contains(err.Error(), "approval_timeout") {
		t.Fatalf("bad duration: %v", err)
	}
}

func TestTailscaleServeHint(t *testing.T) {
	h := tailscaleServeHint(7382, []string{"mac.tail1.ts.net"})
	if !strings.Contains(h, "tailscale serve --bg --https=8443 http://127.0.0.1:7382") {
		t.Fatalf("hint %q", h)
	}
	if !strings.Contains(h, "https://mac.tail1.ts.net:8443/app/") {
		t.Fatalf("hint %q does not say where the web client is", h)
	}
	if h := tailscaleServeHint(7382, nil); strings.Contains(h, "/app/") {
		t.Fatalf("hint %q names a web client URL with no host", h)
	}
}

func TestResolveTrustedHosts(t *testing.T) {
	detected := func() (string, error) { return "mac.tail1.ts.net.", nil }
	failing := func() (string, error) { return "", errors.New("tailscale not running") }

	if got, err := resolveTrustedHosts(nil, detected); err != nil || len(got) != 1 || got[0] != "mac.tail1.ts.net" {
		t.Fatalf("auto-detect: %v %v", got, err)
	}
	if got, err := resolveTrustedHosts([]string{"a.ts.net", "b.ts.net"}, failing); err != nil || len(got) != 2 {
		t.Fatalf("configured names must not need detection: %v %v", got, err)
	}
	if _, err := resolveTrustedHosts(nil, failing); err == nil || !strings.Contains(err.Error(), "hostnames") {
		t.Fatalf("no names at all: %v, want an error naming remote_access.hostnames", err)
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

func writeAPNsKey(t *testing.T, path string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPush_OffByDefault(t *testing.T) {
	p, err := buildPush(config.PushConfig{}, []string{"mac.ts.net"}, nil, t.TempDir(), func(string) string { return "" })
	if err != nil || len(p.notifiers) != 0 || p.devices != nil {
		t.Fatalf("%+v %v, want nothing on", p, err)
	}
}

func TestBuildPush_Ntfy(t *testing.T) {
	env := map[string]string{"NTFY_TOKEN": "tk_1"}
	p, err := buildPush(config.PushConfig{Ntfy: config.NtfyConfig{Topic: "aimux-k3x9", TokenEnv: "NTFY_TOKEN"}}, //nolint:gosec // an environment variable name, not a credential
		[]string{"mac.ts.net"}, nil, t.TempDir(), func(k string) string { return env[k] })
	if err != nil || len(p.notifiers) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	n, ok := p.notifiers[0].(*push.Ntfy)
	if !ok || n.Server != "https://ntfy.sh" || n.Topic != "aimux-k3x9" || n.Token != "tk_1" {
		t.Fatalf("ntfy %+v", p.notifiers[0])
	}
	if p.clickURL != "https://mac.ts.net:8443/app/" || p.devices != nil {
		t.Fatalf("click %q devices %v", p.clickURL, p.devices)
	}
	// A token variable that is not set would fail every publish later: refuse now.
	_, err = buildPush(config.PushConfig{Ntfy: config.NtfyConfig{Topic: "t", TokenEnv: "UNSET"}}, nil, nil, t.TempDir(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "UNSET") {
		t.Fatalf("unset token variable: %v", err)
	}
}

func TestBuildPush_APNs(t *testing.T) {
	home := t.TempDir()
	keyFile := filepath.Join(home, ".aimux", "AuthKey_ABC.p8")
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAPNsKey(t, keyFile)
	cfg := config.APNsConfig{KeyFile: "~/.aimux/AuthKey_ABC.p8", KeyID: "ABC", TeamID: "TEAM", Topic: "me.example.aimux"}
	p, err := buildPush(config.PushConfig{APNs: cfg}, nil, []string{"me@example.com"}, home, func(string) string { return "" })
	if err != nil || len(p.notifiers) != 1 || p.devices == nil {
		t.Fatalf("%+v %v", p, err)
	}
	if a, ok := p.notifiers[0].(*push.APNs); !ok || a.KeyID != "ABC" || a.TeamID != "TEAM" || a.Topic != "me.example.aimux" || a.Devices != p.devices ||
		fmt.Sprint(a.AllowedLogins) != "[me@example.com]" {
		t.Fatalf("apns %+v", p.notifiers[0])
	}
	if p.devices.Path != filepath.Join(home, ".aimux", "remote-devices.json") {
		t.Fatalf("devices at %s", p.devices.Path)
	}

	partial := cfg
	partial.TeamID = ""
	if _, err := buildPush(config.PushConfig{APNs: partial}, nil, nil, home, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("missing team_id: %v", err)
	}
	missing := cfg
	missing.KeyFile = filepath.Join(home, "nope.p8")
	if _, err := buildPush(config.PushConfig{APNs: missing}, nil, nil, home, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "nope.p8") {
		t.Fatalf("missing key file: %v", err)
	}
}

type recordingNotifier struct {
	got chan push.Message
	err error
}

func (r *recordingNotifier) Notify(_ context.Context, m push.Message) error {
	r.got <- m
	return r.err
}

func TestApprovalHooks_SendsTheWordedMessageWithALink(t *testing.T) {
	rec := &recordingNotifier{got: make(chan push.Message, 1), err: errors.New("relay down")}
	var errOut strings.Builder
	exp := time.Now().Add(10 * time.Minute)
	notify, _ := approvalHooks(pushSetup{notifiers: []push.Notifier{rec}, clickURL: "https://mac.ts.net:8443/app/"},
		func(_ context.Context, id string) string {
			if id != "s/1" {
				t.Errorf("title asked for %q", id)
			}
			return "Fix the cache"
		}, &errOut)
	notify(remoteapi.ApprovalNotice{ApprovalID: "ap1", Session: "s/1", Title: "Bash", Expires: exp})
	m := <-rec.got
	if m.Body != "Fix the cache: Bash" || m.ApprovalID != "ap1" || m.SessionID != "s/1" || !m.Expires.Equal(exp) ||
		m.Link != "https://mac.ts.net:8443/app/#/s/s%2F1" {
		t.Fatalf("message %+v", m)
	}
	if !strings.Contains(errOut.String(), "relay down") {
		t.Fatalf("a failed notification must be logged, got %q", errOut.String())
	}
}

func TestApprovalHooks_OffWithoutNotifiers(t *testing.T) {
	if asked, resolved := approvalHooks(pushSetup{}, nil, io.Discard); asked != nil || resolved != nil {
		t.Fatal("hooks with nothing to send to")
	}
}

type retractingNotifier struct {
	recordingNotifier
	retracted chan push.Message
}

func (r *retractingNotifier) Retract(_ context.Context, m push.Message) error {
	r.retracted <- m
	return errors.New("old ntfy server")
}

func TestApprovalHooks_ResolvedTakesTheNotificationBack(t *testing.T) {
	rec := &retractingNotifier{recordingNotifier{got: make(chan push.Message, 1)}, make(chan push.Message, 1)}
	var errOut strings.Builder
	asked, resolved := approvalHooks(pushSetup{notifiers: []push.Notifier{rec}}, func(context.Context, string) string { return "" }, &errOut)
	asked(remoteapi.ApprovalNotice{ApprovalID: "ap1", Session: "s1", Title: "Bash"})
	<-rec.got
	resolved(remoteapi.ApprovalNotice{ApprovalID: "ap1", Session: "s1", Outcome: remoteapi.OutcomeAllowed})
	if m := <-rec.retracted; m.ApprovalID != "ap1" || m.SessionID != "s1" {
		t.Fatalf("retracted %+v", m)
	}
	if !strings.Contains(errOut.String(), "old ntfy server") {
		t.Fatalf("a failed retraction must be logged, got %q", errOut.String())
	}
}

func TestApprovalHooks_SlowTitleLookupStillNotifies(t *testing.T) {
	rec := &recordingNotifier{got: make(chan push.Message, 1)}
	stuck := make(chan struct{})
	defer close(stuck)
	asked, _ := approvalHooks(pushSetup{notifiers: []push.Notifier{rec}}, func(context.Context, string) string {
		<-stuck // a session read that ignores its context, as the real one does
		return "Late title"
	}, io.Discard)
	start := time.Now()
	go asked(remoteapi.ApprovalNotice{ApprovalID: "ap1", Session: "s1", Title: "Bash"})
	select {
	case m := <-rec.got:
		if m.Body != "Bash" {
			t.Fatalf("body %q", m.Body)
		}
		if d := time.Since(start); d > titleLookupTimeout+2*time.Second {
			t.Fatalf("notified after %v", d)
		}
	case <-time.After(titleLookupTimeout + 3*time.Second):
		t.Fatal("a slow title lookup used up the delivery time")
	}
}

func TestPushSummary(t *testing.T) {
	both := config.PushConfig{Ntfy: config.NtfyConfig{Topic: "k3x9"}, APNs: config.APNsConfig{Topic: "me.example.aimux"}}
	if got := pushSummary(both); got != "ntfy topic k3x9, APNs for me.example.aimux" {
		t.Fatalf("%q", got)
	}
	if got := pushSummary(config.PushConfig{}); got != "" {
		t.Fatalf("nothing on: %q", got)
	}
}

// orderNotifier records sends and retractions in order.
type orderNotifier struct {
	mu  sync.Mutex
	log []string
}

func (o *orderNotifier) Notify(_ context.Context, m push.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.log = append(o.log, "send "+m.ApprovalID)
	return nil
}

func (o *orderNotifier) Retract(_ context.Context, m push.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.log = append(o.log, "retract "+m.ApprovalID)
	return nil
}

func (o *orderNotifier) events() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.log...)
}

// An approval answered while its title is still being read: the alert must
// not land after it was taken back (seen live with a large session).
func TestApprovalHooks_AnsweredDuringTheTitleLookupLeavesNoAlert(t *testing.T) {
	o := &orderNotifier{}
	reading := make(chan struct{})
	finish := make(chan struct{})
	asked, resolved := approvalHooks(pushSetup{notifiers: []push.Notifier{o}}, func(context.Context, string) string {
		close(reading)
		<-finish
		return "Big session"
	}, io.Discard)
	n := remoteapi.ApprovalNotice{ApprovalID: "ap1", Session: "s1", Title: "Bash"}
	askedDone := make(chan struct{})
	go func() { asked(n); close(askedDone) }()
	<-reading
	resolvedDone := make(chan struct{})
	go func() { resolved(n); close(resolvedDone) }()
	time.Sleep(50 * time.Millisecond) // resolved is now waiting on the lookup
	close(finish)
	<-askedDone
	<-resolvedDone
	if got := o.events(); len(got) != 0 {
		t.Fatalf("events %v, want none: the approval was settled before the alert went out", got)
	}
}

func TestApprovalHooks_ResolvedBeforeAskedRunsSendsNothing(t *testing.T) {
	o := &orderNotifier{}
	asked, resolved := approvalHooks(pushSetup{notifiers: []push.Notifier{o}}, func(context.Context, string) string { return "" }, io.Discard)
	n := remoteapi.ApprovalNotice{ApprovalID: "ap1", Session: "s1", Title: "Bash"}
	done := make(chan struct{})
	go func() { resolved(n); close(done) }() // the goroutines may run in either order
	time.Sleep(20 * time.Millisecond)
	asked(n)
	<-done
	if got := o.events(); len(got) != 0 {
		t.Fatalf("events %v, want none", got)
	}
}

func TestApprovalHooks_SentThenResolvedIsTakenBackAfterTheSend(t *testing.T) {
	o := &orderNotifier{}
	asked, resolved := approvalHooks(pushSetup{notifiers: []push.Notifier{o}}, func(context.Context, string) string { return "" }, io.Discard)
	n := remoteapi.ApprovalNotice{ApprovalID: "ap1", Session: "s1", Title: "Bash"}
	asked(n)
	resolved(n)
	if got := o.events(); fmt.Sprint(got) != "[send ap1 retract ap1]" {
		t.Fatalf("events %v", got)
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
		"Open Claude sessions: 3\n",
		"  01234567  /dev/ttys001  reachable\n",
		"  fedcba98  -             not in a terminal\n",
		"  55556666  /dev/ttys002  not in iTerm2\n",
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
	if got := out.String(); !strings.Contains(got, "Terminal input: off (set remote_access.terminal_input.enabled: true") || !strings.Contains(got, "Open Claude sessions: 0\n") {
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
	if !strings.Contains(got, "  01234567  /dev/ttys001  unknown (Automation denied)\n") || len(drv.located) != 0 {
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
	if !strings.Contains(got, "iTerm2 Automation: error: helper exited\n") || !strings.Contains(got, "  01234567  /dev/ttys001  error: helper exited\n") {
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
	got := openTerminalSessions(dir, func(pid int) bool { return pid == os.Getppid() })
	if len(got) != 1 || got[0].ID != "live-mine" || got[0].PID != me {
		t.Fatalf("got %+v, want only the live, not aimux-owned session", got)
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
	if !strings.Contains(got, "  01234567  /dev/ttys001  error: first\n") {
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

func TestBuildDeviceLogin_On(t *testing.T) {
	home := t.TempDir()
	var out strings.Builder
	dl, closeLog, err := buildDeviceLogin(true, home, &out)
	if err != nil || dl == nil {
		t.Fatalf("buildDeviceLogin(on) = %v, %v", dl, err)
	}
	defer closeLog()
	fi, err := os.Stat(filepath.Join(home, ".aimux", "logs", "remote-audit.log"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("audit log: %v %v, want mode 0600", fi, err)
	}
	if !strings.Contains(out.String(), "aimux remote pair") {
		t.Fatalf("banner %q does not say how to pair", out.String())
	}
}

func TestBuildDeviceLogin_OffWarns(t *testing.T) {
	home := t.TempDir()
	var out strings.Builder
	dl, closeLog, err := buildDeviceLogin(false, home, &out)
	if err != nil || dl != nil {
		t.Fatalf("buildDeviceLogin(off) = %v, %v; want nil", dl, err)
	}
	closeLog()
	if !strings.Contains(out.String(), "Warning") || !strings.Contains(out.String(), "device_login") {
		t.Fatalf("no warning: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".aimux", "logs", "remote-audit.log")); !os.IsNotExist(err) {
		t.Fatalf("audit log opened with device login off: %v", err)
	}
}

func TestBuildDeviceLogin_CorruptDevicesFileIsAnError(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".aimux")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "remote-paired-devices.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildDeviceLogin(true, home, io.Discard); err == nil || !strings.Contains(err.Error(), "remote-paired-devices.json") {
		t.Fatalf("corrupt devices file: %v, want an error naming it", err)
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
