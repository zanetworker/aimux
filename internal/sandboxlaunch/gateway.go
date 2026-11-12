// Package sandboxlaunch creates, updates and runs the OpenShell sandbox for a
// compiled profile, and archives its transcripts on the host.
package sandboxlaunch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Gateway is one OpenShell gateway registered with the openshell CLI.
type Gateway struct{ Name, Endpoint, AuthMode string }

// Connect checks the gateway answers with a supported version and returns
// the backend for it.
func Connect(ctx context.Context, g Gateway) (Backend, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return connect(ctx, g, ExecRunner(), home)
}

func connect(ctx context.Context, g Gateway, run Runner, home string) (Backend, error) {
	if !gatewayNameRE.MatchString(g.Name) {
		return nil, fmt.Errorf("invalid gateway name %q", g.Name)
	}
	b := NewCLIBackend(run, filepath.Join(home, ".aimux", "sandboxes")).(*cliBackend)
	b.gateway = g.Name
	out, err := b.output(ctx, nil, "status")
	if err != nil {
		return nil, fmt.Errorf("OpenShell gateway %s (%s) is not reachable: %w", g.Name, g.Endpoint, err)
	}
	version := ""
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Version:"); ok {
			version = strings.TrimSpace(v)
		}
	}
	if version == "" {
		return nil, fmt.Errorf("OpenShell gateway %s reported no version", g.Name)
	}
	if major, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), "."); major != "0" {
		return nil, fmt.Errorf("unsupported OpenShell gateway version %s", version)
	}
	return b, nil
}

// gatewayNameRE allows the names the openshell CLI registers (host names too).
var gatewayNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
