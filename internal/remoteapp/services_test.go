package remoteapp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/terminput"
)

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

type layoutDriver struct{ terminput.Driver }

func TestBuildLayoutReader(t *testing.T) {
	sessions := func() map[string]int { return nil }
	if r := buildLayoutReader(sessions, nil, io.Discard, plainDriver{}); r != nil {
		t.Fatal("a driver without a layout gave a reader")
	}
	if r := buildLayoutReader(sessions, nil, io.Discard); r != nil {
		t.Fatal("no drivers gave a reader")
	}
	r := buildLayoutReader(sessions, nil, io.Discard, plainDriver{}, layoutDriver{})
	if r == nil || r.Source == nil || r.LastOnMac != nil {
		t.Fatalf("reader %+v: want a source and no LastOnMac without lastFocus", r)
	}
	r = buildLayoutReader(sessions, &terminput.LastFocus{}, io.Discard, layoutDriver{})
	if r == nil || r.LastOnMac == nil {
		t.Fatal("lastFocus given but LastOnMac unset")
	}
}

// remote serve registers exactly one provider today, Claude's, reading
// ~/.claude: the API's provider value stays "claude".
func TestRemoteProviders_OnlyClaude(t *testing.T) {
	home := t.TempDir()
	ps := Providers(home)
	if len(ps) != 1 {
		t.Fatalf("%d providers, want 1", len(ps))
	}
	if name := ps[0].Store.Name(); name != "claude" {
		t.Fatalf("default provider %q, want claude", name)
	}
	live, ok := ps[0].Live.(*discovery.LiveCache)
	if !ok || live.Dir != filepath.Join(home, ".claude", "sessions") {
		t.Fatalf("live %#v, want the registry under %s", ps[0].Live, home)
	}
	file := filepath.Join(home, ".claude", "projects", "-w", "s-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ps[0].Store.SessionFile("s-1"); got != file {
		t.Fatalf("SessionFile = %q, want %q", got, file)
	}
	if got := ps[0].Store.SessionFile("missing"); got != "" {
		t.Fatalf("SessionFile(missing) = %q, want none", got)
	}
}

func (layoutDriver) Layout(context.Context) ([]terminput.PaneRef, error) { return nil, nil }
