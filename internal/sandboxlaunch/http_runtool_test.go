package sandboxlaunch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zanetworker/aimux/internal/httpsource"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// An http-openapi call runs through the real SourceTools and
// cliBackend.RunTool: RunTool takes only an absolute binary, so the call
// reaches openshell as /usr/bin/curl -q (no .curlrc) and succeeds.
func TestHTTPSourceCall_ThroughRunTool(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "httpsource", "testdata", "mixed.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(t.TempDir(), "github.yaml")
	if err := os.WriteFile(spec, b, 0o600); err != nil { // #nosec G703 -- a path under t.TempDir
		t.Fatal(err)
	}
	src := toolreg.Source{Type: toolreg.TypeHTTPOpenAPI, Spec: spec, BaseURL: "https://api.github.com"}
	f := toolsRunner("wingman-claude\n")
	f.respond = func(base func([]string) (string, error)) func([]string) (string, error) {
		return func(argv []string) (string, error) {
			if argv[3]+" "+argv[4] == "sandbox exec" {
				return `[{"number":1}]`, nil
			}
			return base(argv)
		}
	}(f.respond)
	ts := NewToolSandbox(f.run, t.TempDir(), func(func(string) bool) (profile.Compiled, map[string]error, error) {
		return profile.Compiled{Sandbox: profile.RunnerSandbox, Image: profile.RunnerImage, PolicyDigest: "sha256:r", PolicyYAML: []byte("version: 1\n")}, nil, nil
	})
	ts.b.pollEvery = 0
	backend := mcpproxy.NewHTTPSourceBackend("github", src, ts.For("github"))
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "github_issues_list_for_repo", Arguments: map[string]any{"owner": "cli", "repo": "cli"}}}
	res, err := backend.CallTool(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("call = %+v, %v", res, err)
	}
	if tc, ok := res.Content[0].(mcp.TextContent); !ok || tc.Text != `[{"number":1}]` {
		t.Fatalf("result = %+v", res.Content)
	}
	if httpsource.CurlBinary != "/usr/bin/curl" {
		t.Fatalf("CurlBinary = %q", httpsource.CurlBinary)
	}
	exec := f.call("sandbox exec")
	i := slices.Index(exec, "--")
	if i < 0 || i+2 >= len(exec) || exec[i+1] != "/usr/bin/curl" || exec[i+2] != "-q" {
		t.Fatalf("exec argv = %q, want /usr/bin/curl -q after --", exec)
	}
}
