package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
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
