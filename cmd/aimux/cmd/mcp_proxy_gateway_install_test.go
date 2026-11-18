package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// slowLaunchd is launchd as it behaves: bootout returns before the job is
// unloaded, and a bootstrap in that window fails with exit status 5.
type slowLaunchd struct {
	loaded, unloading bool
	lag               int
	calls             []string
}

func (l *slowLaunchd) run(_ string, args ...string) ([]byte, error) {
	l.calls = append(l.calls, args[0])
	if l.unloading {
		if l.lag--; l.lag <= 0 {
			l.unloading, l.loaded = false, false
		}
	}
	switch args[0] {
	case "print":
		if l.loaded {
			return []byte("state = running"), nil
		}
		return []byte("Could not find service"), errors.New("exit status 113")
	case "bootout":
		if !l.loaded {
			return []byte("No such process"), errors.New("exit status 3")
		}
		l.unloading = true
		return nil, nil
	case "bootstrap":
		if l.loaded {
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
		}
		l.loaded = true
		return nil, nil
	}
	return nil, errors.New("unexpected launchctl " + args[0])
}

// A re-run from another binary points the job at it and restarts it, even
// while launchd still unloads the old one.
func TestInstallGatewayAgent_SwitchesBinaryAndRestarts(t *testing.T) {
	home := t.TempDir()
	l := &slowLaunchd{loaded: true, lag: 3}
	a := gatewayAgent(home)
	a.Run, a.Sleep = l.run, func(time.Duration) {}
	a.Binary = "/old/aimux"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Dir(a.PlistPath), 0o700))
	must(os.WriteFile(a.PlistPath, a.Plist(), 0o600))

	a.Binary = "/new/aimux"
	var out bytes.Buffer
	must(installGatewayAgent(a, &out))
	if got := mcpproxy.ReadPlistBinary(a.PlistPath); got != "/new/aimux" {
		t.Fatalf("plist program %q, want /new/aimux", got)
	}
	if !l.loaded {
		t.Fatalf("job not loaded after install (calls %v)", l.calls)
	}
	if !strings.Contains(out.String(), "was /old/aimux") {
		t.Fatalf("output does not name the old binary: %s", out.String())
	}

	// Same binary again: installed, no switch reported.
	out.Reset()
	must(installGatewayAgent(a, &out))
	if strings.Contains(out.String(), "was ") || !strings.Contains(out.String(), "Installed com.aimux.tool-gateway") {
		t.Fatalf("output: %s", out.String())
	}
}
