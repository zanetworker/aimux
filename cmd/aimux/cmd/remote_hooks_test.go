package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/waiting"
)

func runHooks(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	c := newRemoteHooksCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetIn(strings.NewReader(stdin))
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), err
}

func hooksHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	old, oldLook := hooksExecutable, hooksLookPath
	hooksExecutable = func() (string, error) { return "/opt/test/aimux", nil }
	hooksLookPath = func(string) (string, error) { return "", errors.New("not on PATH") }
	t.Cleanup(func() { hooksExecutable, hooksLookPath = old, oldLook })
	return home
}

func TestRemoteHooksInstallStatusUninstall(t *testing.T) {
	home := hooksHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")

	out, err := runHooks(t, "", "status")
	if err != nil || !strings.Contains(out, "not installed") {
		t.Fatalf("status before: %q, %v", out, err)
	}

	out, err = runHooks(t, "", "install")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, settings) || !strings.Contains(out, "Notification") || !strings.Contains(out, "/opt/test/aimux remote hooks notify") {
		t.Fatalf("install output: %q", out)
	}
	data, err := os.ReadFile(settings) //nolint:gosec // test temp dir
	if err != nil || !strings.Contains(string(data), "/opt/test/aimux remote hooks notify") {
		t.Fatalf("settings: %s, %v", data, err)
	}

	out, err = runHooks(t, "", "install")
	if err != nil || !strings.Contains(out, "already installed") {
		t.Fatalf("second install: %q, %v", out, err)
	}

	out, err = runHooks(t, "", "status")
	if err != nil || !strings.Contains(out, "installed") || strings.Contains(out, "not installed") {
		t.Fatalf("status after: %q, %v", out, err)
	}

	out, err = runHooks(t, "", "uninstall")
	if err != nil || !strings.Contains(out, "Removed") || !strings.Contains(out, ".aimux-bak-") {
		t.Fatalf("uninstall: %q, %v", out, err)
	}
	if st, _ := waiting.Status(settings); len(st.Installed) != 0 {
		t.Fatalf("hooks left after uninstall: %+v", st)
	}
}

func TestRemoteHooksStatusNamesAMovedBinary(t *testing.T) {
	home := hooksHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if _, err := waiting.Install(settings, "/old/aimux remote hooks notify", time.Now()); err != nil {
		t.Fatal(err)
	}
	out, _ := runHooks(t, "", "status")
	if !strings.Contains(out, "/old/aimux") || !strings.Contains(out, "install") || !strings.Contains(out, "does not exist") {
		t.Fatalf("status should name the missing old path and say to reinstall: %q", out)
	}
}

func TestRemoteHooksInstallRefusesBrokenSettings(t *testing.T) {
	home := hooksHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(settings), 0o700)
	_ = os.WriteFile(settings, []byte("{broken"), 0o600)
	if _, err := runHooks(t, "", "install"); err == nil {
		t.Fatal("install over broken settings succeeded")
	}
}

func TestRemoteHooksNotifyWritesAndClears(t *testing.T) {
	home := hooksHome(t)
	in := `{"session_id":"s1","hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`
	out, err := runHooks(t, in, "notify")
	if err != nil || out != "" {
		t.Fatalf("notify: %q, %v", out, err)
	}
	if _, ok := waiting.Read(waiting.Dir(home), "s1"); !ok {
		t.Fatal("no marker written")
	}
	if _, err := runHooks(t, `{"session_id":"s1","hook_event_name":"Stop"}`, "notify"); err != nil {
		t.Fatal(err)
	}
	if _, ok := waiting.Read(waiting.Dir(home), "s1"); ok {
		t.Fatal("marker not cleared")
	}
}

func TestRemoteHooksNotifyNeverFails(t *testing.T) {
	home := hooksHome(t)
	secret := "some prompt text"
	out, err := runHooks(t, `{"session_id":"../`+secret+`","hook_event_name":"Notification","notification_type":"permission_prompt","message":"`+secret+`"}`, "notify")
	if err != nil || out != "" {
		t.Fatalf("notify on bad input: %q, %v (want silent success)", out, err)
	}
	log, err := os.ReadFile(filepath.Join(home, ".aimux", "logs", "hooks.log")) //nolint:gosec // test temp dir
	if err != nil || len(log) == 0 {
		t.Fatalf("no log line: %v", err)
	}
	if strings.Contains(string(log), secret) {
		t.Fatalf("log leaks hook text: %s", log)
	}
}

func TestRemoteHooksNotifyIsHidden(t *testing.T) {
	c := newRemoteHooksCmd()
	for _, sub := range c.Commands() {
		if sub.Name() == "notify" && !sub.Hidden {
			t.Fatal("notify should be hidden")
		}
	}
}

func TestWaitingAlertsPushAndRetract(t *testing.T) {
	rec := &retractingNotifier{recordingNotifier{got: make(chan push.Message, 2)}, make(chan push.Message, 2)}
	var errOut strings.Builder
	since := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	alerts := waitingAlerts(pushSetup{notifiers: []push.Notifier{rec}, clickURL: "https://mac.ts.net:8443/app/"},
		func(context.Context, string) string { return "Fix the cache" }, &errOut)
	e := waiting.Entry{SessionID: "s1", Waiting: waiting.Waiting{Kind: waiting.KindPermission, Message: "Claude needs your permission to use Bash", Since: since}}
	alerts([]waiting.Entry{e}, nil)
	m := <-rec.got
	if m.Title != "Approval needed" || m.Body != "Fix the cache: Claude needs your permission to use Bash" || m.SessionID != "s1" ||
		m.Link != "https://mac.ts.net:8443/app/#/s/s1" || m.ApprovalID == "" || len(m.ApprovalID) > 64 {
		t.Fatalf("message %+v", m)
	}
	alerts(nil, []waiting.Entry{e})
	if r := <-rec.retracted; r.ApprovalID != m.ApprovalID || r.SessionID != "s1" {
		t.Fatalf("retracted %+v, want id %q", r, m.ApprovalID)
	}
	if !strings.Contains(errOut.String(), "old ntfy server") {
		t.Fatalf("a failed retraction must be logged: %q", errOut.String())
	}
	if waitingAlerts(pushSetup{}, nil, &errOut) != nil {
		t.Fatal("alerts with nothing to send to")
	}
}

func TestHooksSummary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if got := hooksSummary(p); !strings.Contains(got, "aimux remote hooks install") {
		t.Fatalf("not installed: %q", got)
	}
	_, _ = waiting.Install(p, "/x/aimux remote hooks notify", time.Now())
	if got := hooksSummary(p); !strings.Contains(got, "on") || strings.Contains(got, "install") {
		t.Fatalf("installed: %q", got)
	}
	_ = os.WriteFile(p, []byte("{broken"), 0o600)
	if got := hooksSummary(p); !strings.Contains(got, "cannot read") {
		t.Fatalf("broken: %q", got)
	}
}

func TestRemoteHooksInstallRefusesATemporaryBuild(t *testing.T) {
	home := hooksHome(t)
	cache := t.TempDir()
	t.Setenv("GOCACHE", cache)
	for _, bin := range []string{
		filepath.Join(os.TempDir(), "go-build1234", "b001", "exe", "aimux"),
		filepath.Join(cache, "ab", "aimux"),
	} {
		hooksExecutable = func() (string, error) { return bin, nil }
		out, err := runHooks(t, "", "install")
		if err == nil || !strings.Contains(err.Error(), "temporary") {
			t.Fatalf("install from %s: %q, %v (want a refusal)", bin, out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err == nil {
		t.Fatal("a refused install wrote the settings")
	}
}

func TestRemoteHooksInstallPrefersThePathOnPATH(t *testing.T) {
	home := hooksHome(t)
	old := hooksForbiddenDirs
	hooksForbiddenDirs = func() []string { return nil } // the test's files live in the temp dir
	t.Cleanup(func() { hooksForbiddenDirs = old })
	real := filepath.Join(t.TempDir(), "aimux")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // test temp dir
		t.Fatal(err)
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "aimux")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	hooksExecutable = func() (string, error) { return real, nil }
	hooksLookPath = func(string) (string, error) { return link, nil }
	if _, err := runHooks(t, "", "install"); err != nil {
		t.Fatal(err)
	}
	st, _ := waiting.Status(filepath.Join(home, ".claude", "settings.json"))
	if len(st.Commands) != 1 || st.Commands[0] != waiting.HookCommand(link) {
		t.Fatalf("commands = %v, want the PATH entry %s", st.Commands, link)
	}

	// A different aimux on PATH is not this binary: keep the running one.
	other := filepath.Join(t.TempDir(), "aimux")
	_ = os.WriteFile(other, []byte("#!/bin/sh\n"), 0o700) //nolint:gosec // test temp dir
	hooksLookPath = func(string) (string, error) { return other, nil }
	if _, err := runHooks(t, "", "install"); err != nil {
		t.Fatal(err)
	}
	st, _ = waiting.Status(filepath.Join(home, ".claude", "settings.json"))
	if len(st.Commands) != 1 || st.Commands[0] != waiting.HookCommand(real) {
		t.Fatalf("commands = %v, want the running binary %s", st.Commands, real)
	}
}
