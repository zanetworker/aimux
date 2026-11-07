package remoteapp

import (
	"os/exec"
	"strings"
	"testing"
)

// remoteapp is a core package: it composes remote serve for cmd, so it
// must not import cmd, a frontend (internal/frontend) or a client
// (clients/, whose web handler comes in through Options.Client).
func TestRemoteappImportsNoCmdOrFrontend(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	const module = "github.com/zanetworker/aimux/"
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", module+"internal/remoteapp").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		rel, ok := strings.CutPrefix(p, module)
		if !ok {
			continue
		}
		for _, banned := range []string{"cmd/", "internal/frontend/", "clients/", "internal/tui/"} {
			if strings.HasPrefix(rel+"/", banned) {
				t.Errorf("internal/remoteapp imports %s", rel)
			}
		}
	}
}
