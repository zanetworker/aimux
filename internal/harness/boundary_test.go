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

// TestCatalogBoundary: the session catalog is core. It imports net/http
// itself never, and no HTTP layer, terminal input or frontend at any depth
// (.claude/CLAUDE.md, package map). Nor the provider registry, discovery or
// acprun, which bring the cluster clients into `aimux mcp serve`'s session
// tools (cmd's TestSessionMCP_NoClusterDeps).
func TestCatalogBoundary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	out, err := exec.Command("go", "list", "-f", `{{join .Imports " "}}`, module+"internal/catalog").Output()
	if err != nil {
		t.Fatalf("go list internal/catalog: %v", err)
	}
	for _, imp := range strings.Fields(string(out)) {
		if imp == "net/http" {
			t.Error("internal/catalog must not import net/http")
		}
	}
	banned := []string{"internal/remoteapi", "internal/remoteapp", "internal/terminput", "internal/frontend", "internal/tui", "internal/sessionmcp",
		"internal/provider", "internal/discovery", "internal/acprun"}
	for _, d := range internalDeps(t, "internal/catalog") {
		for _, b := range banned {
			if d == b || strings.HasPrefix(d, b+"/") {
				t.Errorf("internal/catalog must not import %s", d)
			}
		}
	}
}

// TestSessionStateBoundary: the session state service is core and harness
// neutral. It imports net/http never, and no HTTP layer, frontend, catalog
// or harness-specific package at any depth; harness readers come in as
// inputs (.claude/CLAUDE.md, package map).
func TestSessionStateBoundary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", module+"internal/sessionstate").Output()
	if err != nil {
		t.Fatalf("go list internal/sessionstate: %v", err)
	}
	for _, imp := range strings.Fields(string(out)) {
		if imp == "net/http" {
			t.Error("internal/sessionstate must not import net/http")
		}
	}
	banned := []string{"internal/remoteapi", "internal/remoteapp", "internal/frontend", "internal/tui", "internal/catalog",
		"internal/provider", "internal/discovery", "internal/waiting", "internal/statusdetect", "internal/terminput", "internal/harness/"}
	for _, d := range internalDeps(t, "internal/sessionstate") {
		for _, b := range banned {
			if d == b || strings.HasPrefix(d, strings.TrimSuffix(b, "/")+"/") {
				t.Errorf("internal/sessionstate must not import %s", d)
			}
		}
	}
}

// TestJSONEditBoundary: the config editor is a leaf. It imports only the
// standard library, so whoever edits a config file (the hooks installer, the
// MCP proxy installer) pulls in nothing else (.claude/CLAUDE.md, package map).
func TestJSONEditBoundary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go tool")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", module+"internal/jsonedit").Output()
	if err != nil {
		t.Fatalf("go list internal/jsonedit: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if p != module+"internal/jsonedit" {
			t.Errorf("internal/jsonedit may import only the standard library; imports %s", p)
		}
	}
	for _, d := range internalDeps(t, "internal/waiting") {
		if d == "internal/mcpproxy" {
			t.Error("internal/waiting must edit settings through internal/jsonedit, not internal/mcpproxy")
		}
	}
}
