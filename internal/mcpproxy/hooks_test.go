package mcpproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/claudehooks"
)

func TestIsServerAdd(t *testing.T) {
	yes := []string{
		"claude mcp add foo -- npx foo",
		"claude mcp add -s user foo npx foo",
		"claude mcp add --scope=user -e K=v foo -- npx foo",
		`claude mcp add-json foo '{"command":"x"}'`,
		"claude mcp add-from-claude-desktop",
		"cd x && claude mcp add foo npx foo",
		"/opt/bin/claude mcp add foo x",
		"ls; claude mcp add foo x | tee log",
	}
	no := []string{
		"claude mcp list",
		"claude mcp remove foo",
		"claude mcp get foo",
		`echo "claude mcp add foo"`,
		"# claude mcp add foo",
		"git commit -m 'claude mcp add'",
		"claude mcp",
		"claudette mcp add foo x",
		"claude mcp add 'unclosed",
		"",
	}
	for _, c := range yes {
		if !IsServerAdd(c) {
			t.Errorf("IsServerAdd(%q) = false, want true", c)
		}
	}
	for _, c := range no {
		if IsServerAdd(c) {
			t.Errorf("IsServerAdd(%q) = true, want false", c)
		}
	}
}

func TestProjectServers(t *testing.T) {
	dir := t.TempDir()
	w := filepath.Join(dir, "w")
	if err := os.MkdirAll(w, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, ".claude.json")
	body := `{"mcpServers":{"top":{"command":"x"}},"projects":{"` + w + `":{"mcpServers":{
		"local-stdio":{"type":"stdio","command":"npx","args":["a"]},
		"local-http":{"type":"http","url":"http://x"}}}}}`
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, ".mcp.json"), []byte(`{"mcpServers":{"repo-stdio":{"command":"uvx","args":["b"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ProjectServers(cfg, w)
	if err != nil || strings.Join(got, ",") != "local-stdio,repo-stdio" {
		t.Fatalf("ProjectServers = %v, %v", got, err)
	}
	if got, err := ProjectServers(cfg, filepath.Join(dir, "elsewhere")); err != nil || len(got) != 0 {
		t.Fatalf("no project servers: %v, %v", got, err)
	}
	if got, err := ProjectServers(filepath.Join(dir, "missing.json"), w); err != nil || strings.Join(got, ",") != "repo-stdio" {
		t.Fatalf("missing config: %v, %v", got, err)
	}
}

func TestMessages(t *testing.T) {
	cases := map[string]string{
		MovedMessage([]string{"foo"}):                 "foo moved behind the aimux proxy (tools unchanged; it starts on first use). Open sessions keep their direct copy until they restart.",
		MovedMessage([]string{"foo", "bar"}):          "foo, bar moved behind the aimux proxy (tools unchanged; they start on first use). Open sessions keep their direct copy until they restart.",
		FailedMessage([]string{"foo"}, "it broke"):    "foo stays a direct server: it broke. Run aimux mcp proxy install after fixing it.",
		FailedMessage([]string{"foo", "bar"}, "boom"): "foo, bar stay direct servers: boom. Run aimux mcp proxy install after fixing it.",
		ProjectMessage("foo"):                         "foo was added for this project only; the proxy shares user-scope servers. To share it: claude mcp remove foo, then claude mcp add --scope user …",
		CheckMessage([]string{"foo"}):                 "1 MCP server is not behind the aimux proxy: foo. Run aimux mcp proxy install.",
		CheckMessage([]string{"foo", "bar"}):          "2 MCP servers are not behind the aimux proxy: foo, bar. Run aimux mcp proxy install.",
		CheckMessage(nil):                             "",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got  %q\nwant %q", got, want)
		}
	}
}

func BenchmarkIsServerAdd_NoMatch(b *testing.B) {
	cmd := "cd /Users/someone/project && go test ./internal/mcpproxy/ -run TestSomething -count=1 -timeout 120s 2>&1 | tail -20 # a typical Bash call that is not an MCP add"
	for i := 0; i < b.N; i++ {
		if IsServerAdd(cmd) {
			b.Fatal("matched")
		}
	}
}

func proxyHooksState(t *testing.T, settings string) claudehooks.State {
	t.Helper()
	st, err := claudehooks.Status(settings, HookSpec)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestInstall_AddsHooks(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.in.Settings, fx.in.HookBinary = fx.settings, "/bin/aimux"
	must(t, fx.in.Install(nil))
	if st := proxyHooksState(t, fx.settings); len(st.Missing) != 0 ||
		strings.Join(st.Commands, "|") != "/bin/aimux mcp proxy hooks added || true|/bin/aimux mcp proxy hooks check || true" {
		t.Fatalf("hooks after install: %+v", st)
	}
	m, err := fx.in.loadManifest()
	if err != nil || m.Hooks != hooksAdded {
		t.Fatalf("manifest hooks = %q, %v", m.Hooks, err)
	}
	if !strings.Contains(fx.out.String(), "hooks") {
		t.Fatalf("install did not mention the hooks:\n%s", fx.out.String())
	}
	before := fx.read(fx.settings)
	must(t, fx.in.Install(nil)) // the "nothing to move" path
	if fx.read(fx.settings) != before {
		t.Fatal("a re-run changed settings.json")
	}
	must(t, fx.in.Uninstall())
	if got := fx.read(fx.settings); got != fixtureSettings {
		t.Fatalf("settings.json not byte-identical after uninstall:\n%s", got)
	}
}

func TestInstall_HooksAddedByRerunWithNothingToMove(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.in.Settings = fx.settings
	must(t, fx.in.Install(nil)) // an install from before the hooks
	if st := proxyHooksState(t, fx.settings); len(st.Installed) != 0 {
		t.Fatal("hooks added without a hook binary")
	}
	fx.in.HookBinary = "/bin/aimux"
	must(t, fx.in.Install(nil))
	if st := proxyHooksState(t, fx.settings); len(st.Missing) != 0 {
		t.Fatalf("re-run did not add the hooks: %+v", st)
	}
}

func TestUninstall_RemovesOnlyProxyHooks(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	if _, err := claudehooks.Install(fx.settings, claudehooks.Spec{{Event: "PostToolUse", Suffix: " remote hooks notify", Timeout: 10}}, "/bin/aimux", time.Now()); err != nil {
		t.Fatal(err)
	}
	withRemote := fx.read(fx.settings)
	fx.in.Settings, fx.in.HookBinary = fx.settings, "/bin/aimux"
	must(t, fx.in.Install(nil))
	must(t, fx.in.Uninstall())
	if got := fx.read(fx.settings); got != withRemote {
		t.Fatalf("uninstall touched the remote hooks:\n%s\nwant:\n%s", got, withRemote)
	}
}

func TestInstallStatus_Hooks(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.in.Settings, fx.in.HookBinary = fx.settings, "/bin/aimux"
	must(t, fx.in.Install(nil))
	st, err := fx.in.InstallStatus()
	if err != nil || !st.Hooks {
		t.Fatalf("Hooks = %v, %v; want true", st.Hooks, err)
	}
	if _, err := claudehooks.Uninstall(fx.settings, HookSpec, time.Now()); err != nil { // the owner removes them by hand
		t.Fatal(err)
	}
	if st, _ := fx.in.InstallStatus(); st.Hooks {
		t.Fatal("Hooks still true after they were removed")
	}
}

func TestInstall_NoHookBinaryLeavesSettings(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.in.Settings = fx.settings
	must(t, fx.in.Install(nil))
	if strings.Contains(fx.read(fx.settings), "hooks") {
		t.Fatalf("hooks written without a hook binary:\n%s", fx.read(fx.settings))
	}
}

const demoProject = "/Users/x/mcpServers-demo" // fixtureClaude's project with a project-only stdio "slack"

func TestHookAdded_MovesNewServer(t *testing.T) {
	fresh := newInstallFX(t, fixtureClaude, fixtureSettings)
	if r := fresh.in.HookAdded("claude mcp add -s user pypi -- pypi-mcp", "/w"); r != (HookReply{}) {
		t.Fatalf("proxy not installed: reply %+v, want none", r)
	}
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp", "args": ["--x"]}`))
	fx.probed = nil
	if r := fx.in.HookAdded("ls -la", "/w"); r != (HookReply{}) || fx.probed != nil {
		t.Fatalf("not an add: reply %+v, probed %v", r, fx.probed)
	}
	r := fx.in.HookAdded("claude mcp add -s user pypi -- pypi-mcp --x", "/w")
	want := MovedMessage([]string{"pypi"})
	if r.User != want || r.Claude != want {
		t.Fatalf("reply %+v, want %q", r, want)
	}
	if !jsonEqual(fx.entries()["pypi"], httpEntry("pypi")) {
		t.Fatalf("pypi not moved: %s", fx.entries()["pypi"])
	}
}

func TestHookAdded_FailedMoveNamesTheServer(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp"}`))
	fx.probeFailing["pypi"] = true
	before := fx.read(fx.claude)
	r := fx.in.HookAdded("claude mcp add pypi -- pypi-mcp", "/w")
	if !strings.HasPrefix(r.User, "pypi stays a direct server: ") || !strings.Contains(r.User, "--skip") || r.Claude != r.User {
		t.Fatalf("reply %+v", r)
	}
	if fx.read(fx.claude) != before {
		t.Fatal("a failed move changed ~/.claude.json")
	}
}

func TestHookAdded_ProjectScoped(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.probed = nil
	r := fx.in.HookAdded("claude mcp add slack -- nested", demoProject)
	if want := ProjectMessage("slack"); r.User != want || r.Claude != want || fx.probed != nil {
		t.Fatalf("reply %+v (probed %v), want %q and no install", r, fx.probed, want)
	}
	if r := fx.in.HookAdded("claude mcp add other -- x", demoProject); r != (HookReply{}) {
		t.Fatalf("an older project server was named for another add: %+v", r)
	}
}

func TestHookCheck(t *testing.T) {
	fresh := newInstallFX(t, fixtureClaude, fixtureSettings)
	if r := fresh.in.HookCheck("/w"); r != (HookReply{}) {
		t.Fatalf("not installed: %+v", r)
	}
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	if r := fx.in.HookCheck("/w"); r != (HookReply{}) {
		t.Fatalf("all moved: %+v", r)
	}
	fx.setEntry("b", json.RawMessage(`{"command": "b-mcp"}`))
	fx.setEntry("a", json.RawMessage(`{"command": "a-mcp"}`))
	r := fx.in.HookCheck("/w")
	if want := CheckMessage([]string{"a", "b"}); r.User != want || r.Claude != want {
		t.Fatalf("reply %+v, want %q", r, want)
	}
	if r := fx.in.HookCheck(demoProject); !strings.Contains(r.Claude, "slack") || strings.Contains(r.User, "slack") {
		t.Fatalf("project-only slack should reach Claude, not the owner's line: %+v", r)
	}
}

func TestHookCheck_NamesDriftedServers(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	// a stale session wrote slack's old stdio entry back: it runs direct again
	fx.setEntry("slack", json.RawMessage(`{"command": "npx", "args": ["-y", "slack-mcp"]}`))
	r := fx.in.HookCheck("/w")
	if !strings.Contains(r.User, "slack") || !strings.Contains(r.User, DriftFix) || r.Claude != r.User {
		t.Fatalf("drifted slack not named with the fix: %+v", r)
	}
}

func TestHookAdded_ProjectAddExplainedWhenOthersMove(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp"}`)) // added in a terminal earlier, still direct
	r := fx.in.HookAdded("claude mcp add slack -- nested", demoProject)
	if !strings.Contains(r.User, MovedMessage([]string{"pypi"})) || !strings.Contains(r.User, ProjectMessage("slack")) {
		t.Fatalf("reply %+v, want the move and the project explanation", r)
	}
}

func TestHookAdded_SkipsTheOpenSessionsScan(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.in.Sessions = func() ([]string, error) { t.Fatal("the hook ran the open-sessions scan"); return nil, nil }
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp"}`))
	if r := fx.in.HookAdded("claude mcp add pypi -- pypi-mcp", "/w"); r.User != MovedMessage([]string{"pypi"}) {
		t.Fatalf("reply %+v", r)
	}
}

func TestIsServerAdd_HeredocBodyIsNotACommand(t *testing.T) {
	no := []string{
		"cat > setup.md <<'EOF'\nRun this:\nclaude mcp add foo -- npx foo\nEOF",
		"cat > s.sh <<EOF\nclaude mcp add foo x\nEOF\necho done",
		"cat <<-END > x\n\tclaude mcp add foo x\n\tEND",
		"python3 - <<\"PY\"\nprint('claude mcp add foo')\nclaude mcp add foo x\nPY",
	}
	for _, c := range no {
		if IsServerAdd(c) {
			t.Errorf("IsServerAdd(%q) = true; a heredoc body is data, not a command", c)
		}
	}
	yes := []string{
		"cat > x <<EOF\nhello\nEOF\nclaude mcp add foo -- npx foo", // a real add after the heredoc ends
		"claude mcp add-json foo \"$(cat <<EOF\n{\\\"command\\\":\\\"x\\\"}\nEOF\n)\"",
		"grep x <<< 'here string'; claude mcp add foo x",
	}
	for _, c := range yes {
		if !IsServerAdd(c) {
			t.Errorf("IsServerAdd(%q) = false, want true", c)
		}
	}
}

func TestIsServerAdd_LineContinuation(t *testing.T) {
	for _, c := range []string{
		"claude mcp \\\nadd --scope user foo -- npx foo",
		"claude \\\n  mcp add foo \\\n  -- npx foo",
	} {
		if !IsServerAdd(c) {
			t.Errorf("IsServerAdd(%q) = false; a backslash-newline joins the lines", c)
		}
	}
}
