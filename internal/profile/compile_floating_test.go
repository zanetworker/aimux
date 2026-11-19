package profile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/toolreg"
)

// A set whose server runs a floating command (npx without a version) is
// never granted, even with pinned tools; the other sets still are.
func TestCompile_FloatingServerInSetDropped(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"servers.yaml": "version: 1\nservers:\n" +
			"  jira:\n    url: http://127.0.0.1:8001/mcp\n" +
			"  slack:\n    command: [npx, -y, slack-mcp]\n    url: http://127.0.0.1:7381/mcp\n",
		"tool-sets.yaml": "version: 1\nsets:\n" +
			"  jira:readonly:\n    server: jira\n    tools: [search]\n" +
			"  slack:readonly:\n    server: slack\n    tools: [search_messages]\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := toolreg.Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if e := reg.SetErrors["slack:readonly"]; e == nil || !strings.Contains(e.Error(), toolreg.FloatingReason) {
		t.Fatalf("slack set error = %v", e)
	}
	pins := toolreg.PinTable{"jira": {"search": {Hash: "a"}}, "slack": {"search_messages": {Hash: "b"}}}
	c, err := CompileAt(workReadonly(t), toolreg.NewSnapshot(reg, pins, nil), testGW)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Grant.Scope(), map[string][]string{"jira": {"search"}}) {
		t.Fatalf("grant = %v, want only jira", c.Grant.Scope())
	}
}
