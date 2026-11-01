package harness

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/zanetworker/aimux/"

// internalDeps is the internal packages pkg imports, directly or not
// (go list -deps, without tests), pkg itself left out.
func internalDeps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", module+pkg).Output() //nolint:gosec // fixed package paths from this test
	if err != nil {
		t.Fatalf("go list %s: %v", pkg, err)
	}
	var deps []string
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, module) && p != module+pkg {
			deps = append(deps, strings.TrimPrefix(p, module))
		}
	}
	return deps
}

// TestHarnessBoundary enforces the harness boundary rule (.claude/CLAUDE.md).
func TestHarnessBoundary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	if deps := internalDeps(t, "internal/harness"); len(deps) != 0 {
		t.Errorf("internal/harness must not import internal packages; imports %v", deps)
	}
	for _, d := range internalDeps(t, "internal/harness/claude") {
		if d != "internal/harness" {
			t.Errorf("internal/harness/claude may import only internal/harness; imports %s", d)
		}
	}
	for _, d := range internalDeps(t, "internal/terminput") {
		if d == "internal/provider" || strings.HasPrefix(d, "internal/provider/") {
			t.Errorf("internal/terminput must not import %s", d)
		}
	}
	for _, d := range internalDeps(t, "internal/provider") {
		if d == "internal/terminput" || strings.HasPrefix(d, "internal/terminput/") {
			t.Errorf("internal/provider must not import %s", d)
		}
	}
}
