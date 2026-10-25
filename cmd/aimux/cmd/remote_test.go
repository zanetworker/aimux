package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi"
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
