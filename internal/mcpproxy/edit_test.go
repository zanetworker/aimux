package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestEditServer_SavesGoodEdit verifies that a valid edit is saved and
// triggers a reload.
func TestEditServer_SavesGoodEdit(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	// Record reload calls
	var reloadCalled bool
	fx.reloadFn = func() (ReloadResult, error) {
		reloadCalled = true
		return ReloadResult{Changed: []string{"slack"}}, nil
	}

	// Change command from "cat" to "echo"
	fx.editorFn = func(path string) error {
		data, err := os.ReadFile(path) // #nosec G304 -- test reading the temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Command = "echo"
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0600)
	}

	// Probe succeeds
	fx.probeFn = func(cfg ServerConfig) error {
		if cfg.Command != "echo" {
			return errors.New("probe got wrong config")
		}
		return nil
	}

	if err := EditServer(fx.ctx, fx.opts); err != nil {
		t.Fatalf("EditServer() = %v, want nil", err)
	}

	// Verify the definition was saved
	servers, _, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	if got := servers["slack"].Command; got != "echo" {
		t.Errorf("saved command = %q, want %q", got, "echo")
	}

	// Verify reload was called
	if !reloadCalled {
		t.Error("reload was not called")
	}
}

// TestEditServer_InvalidJSON refuses to save invalid JSON.
func TestEditServer_InvalidJSON(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	fx.editorFn = func(path string) error {
		return os.WriteFile(path, []byte("not json"), 0600)
	}

	err := EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("EditServer() = %v, want invalid JSON error", err)
	}

	// Verify nothing was saved
	servers, _, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	if got := servers["slack"].Command; got != "cat" {
		t.Errorf("command changed to %q, want unchanged %q", got, "cat")
	}
}

// TestEditServer_BadCommand refuses when the command doesn't resolve.
func TestEditServer_BadCommand(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	fx.editorFn = func(path string) error {
		data, err := os.ReadFile(path) // #nosec G304 -- test reading the temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Command = "/nonexistent/command"
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0600)
	}

	err := EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "resolve") {
		t.Errorf("EditServer() = %v, want command resolution error", err)
	}

	// Verify nothing was saved
	servers, _, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	if got := servers["slack"].Command; got != "cat" {
		t.Errorf("command changed to %q, want unchanged %q", got, "cat")
	}
}

// TestEditServer_FailingProbe refuses when the server fails to probe.
func TestEditServer_FailingProbe(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	fx.editorFn = func(path string) error {
		data, err := os.ReadFile(path) // #nosec G304 -- test reading the temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Args = []string{"--bad-flag"}
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0600)
	}

	fx.probeFn = func(cfg ServerConfig) error {
		return errors.New("server failed to start")
	}

	err := EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "probe") {
		t.Errorf("EditServer() = %v, want probe error", err)
	}

	// Verify nothing was saved
	servers, _, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	if len(servers["slack"].Args) != 0 {
		t.Errorf("args changed to %v, want unchanged []", servers["slack"].Args)
	}
}

// TestEditServer_EditorExitNonZero refuses when the editor exits non-zero.
func TestEditServer_EditorExitNonZero(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	fx.editorFn = func(path string) error {
		return errors.New("editor exited with code 1")
	}

	err := EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "editor") {
		t.Errorf("EditServer() = %v, want editor error", err)
	}

	// Verify nothing was saved
	servers, _, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	if got := servers["slack"].Command; got != "cat" {
		t.Errorf("command changed to %q, want unchanged %q", got, "cat")
	}
}

// TestEditServer_UnknownServer reports error for unknown server.
func TestEditServer_UnknownServer(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)
	fx.opts.Server = "nosuch"

	err := EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "install") {
		t.Errorf("EditServer() = %v, want error naming install", err)
	}
}

// TestEditServer_NotMovedServer reports error for stdio server not behind proxy.
func TestEditServer_NotMovedServer(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	// Remove slack from manifest
	m, err := LoadManifest(fx.manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest() = %v", err)
	}
	delete(m.Servers, "slack")
	if err := saveManifest(fx.manifestPath, m); err != nil {
		t.Fatalf("saveManifest() = %v", err)
	}

	err = EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "install") {
		t.Errorf("EditServer() = %v, want error naming install", err)
	}
}

// TestEditServer_NoChanges succeeds when no changes are made.
func TestEditServer_NoChanges(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	fx.editorFn = func(path string) error {
		// Don't change anything
		return nil
	}

	// Should succeed but not reload
	var reloadCalled bool
	fx.reloadFn = func() (ReloadResult, error) {
		reloadCalled = true
		return ReloadResult{}, nil
	}

	if err := EditServer(fx.ctx, fx.opts); err != nil {
		t.Errorf("EditServer() = %v, want nil (nothing to do is not a failure)", err)
	}
	if !strings.Contains(fx.out.String(), "unchanged") {
		t.Errorf("output %q, want a note that the definition is unchanged", fx.out.String())
	}

	if reloadCalled {
		t.Error("reload was called when nothing changed")
	}
}

// editFixture holds the test setup for edit tests.
type editFixture struct {
	ctx          context.Context
	opts         EditOptions
	serversPath  string
	manifestPath string
	editorFn     func(string) error
	probeFn      func(ServerConfig) error
	probeCtxFn   func(context.Context, ServerConfig) ([]json.RawMessage, error)
	probeTools   []json.RawMessage
	catalogPath  string
	out          bytes.Buffer
	reloadFn     func() (ReloadResult, error)
}

// setupEditFixture creates a test environment with one moved server.
func setupEditFixture(t *testing.T, home string) *editFixture {
	t.Helper()

	serversPath := filepath.Join(home, "mcp-servers.json")
	manifestPath := filepath.Join(home, "mcp-proxy-install.json")
	catalogPath := filepath.Join(home, "mcp-catalog.json")
	cat := &Catalog{}
	cat.Set("slack", toolJSON("post"), time.Unix(0, 0))
	cat.Set("other", toolJSON("other_tool"), time.Unix(0, 0))
	if err := cat.Save(catalogPath); err != nil {
		t.Fatal(err)
	}

	// Set up a server file with one server
	servers := map[string]ServerConfig{
		"slack": {Command: "cat"},
	}
	resolved := []string{"slack"}
	if err := SaveServerFile(serversPath, servers, resolved); err != nil {
		t.Fatalf("SaveServerFile() = %v", err)
	}

	// Set up a manifest with one moved server
	m := &Manifest{
		Version: manifestVersion,
		Port:    4567,
		Servers: map[string]MovedEntry{
			"slack": {
				Original: `{"type":"stdio","command":"cat"}`,
				Written:  `{"type":"http","url":"http://127.0.0.1:4567/mcp/slack"}`,
			},
		},
	}
	if err := saveManifest(manifestPath, m); err != nil {
		t.Fatalf("saveManifest() = %v", err)
	}

	fx := &editFixture{
		ctx:          context.Background(),
		serversPath:  serversPath,
		manifestPath: manifestPath,
		catalogPath:  catalogPath,
		probeTools:   toolJSON("post"),
	}

	fx.opts = EditOptions{
		Server:       "slack",
		ServersPath:  serversPath,
		ManifestPath: manifestPath,
		CatalogPath:  catalogPath,
		Out:          &fx.out,
		Port:         4567,
		Editor: func(path string) error {
			if fx.editorFn != nil {
				return fx.editorFn(path)
			}
			return nil
		},
		Probe: func(ctx context.Context, cfg ServerConfig) ([]json.RawMessage, error) {
			if fx.probeCtxFn != nil {
				return fx.probeCtxFn(ctx, cfg)
			}
			if fx.probeFn != nil {
				if err := fx.probeFn(cfg); err != nil {
					return nil, err
				}
			}
			return fx.probeTools, nil
		},
		Reload: func() (ReloadResult, error) {
			if fx.reloadFn != nil {
				return fx.reloadFn()
			}
			return ReloadResult{}, nil
		},
		Timeout: 5 * time.Second,
	}

	return fx
}

// setArgs is an editor that sets the definition's args.
func setArgs(args ...string) func(string) error {
	return func(path string) error {
		data, err := os.ReadFile(path) // #nosec G304 -- the test's temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Args = args
		out, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0o600)
	}
}

// The probed tools are saved for the server before the reload, so the
// reload serves a tool the new definition added; other entries are kept.
func TestEditServer_SavesProbedToolsBeforeReload(t *testing.T) {
	fx := setupEditFixture(t, t.TempDir())
	fx.editorFn = setArgs("--v2")
	fx.probeTools = toolJSON("post", "new_tool")
	var atReload *Catalog
	fx.reloadFn = func() (ReloadResult, error) {
		cat, err := LoadCatalog(fx.catalogPath)
		atReload = cat
		return ReloadResult{Changed: []string{"slack"}}, err
	}
	if err := EditServer(fx.ctx, fx.opts); err != nil {
		t.Fatal(err)
	}
	if atReload == nil || len(atReload.Servers["slack"].Tools) != 2 || len(atReload.Servers["other"].Tools) != 1 {
		t.Fatalf("catalog at reload = %+v, want slack's two probed tools and other kept", atReload)
	}
}

func TestEditServer_ReloadFailureSaysSavedTakesEffectLater(t *testing.T) {
	fx := setupEditFixture(t, t.TempDir())
	fx.editorFn = setArgs("--v2")
	fx.reloadFn = func() (ReloadResult, error) { return ReloadResult{}, errors.New("connection refused") }
	err := EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "saved; takes effect when the proxy next starts") {
		t.Fatalf("err = %v", err)
	}
	servers, _, _ := LoadServerFile(fx.serversPath)
	if len(servers["slack"].Args) != 1 {
		t.Fatal("the definition was not saved")
	}
}

func TestEditServer_UnknownFieldRejected(t *testing.T) {
	fx := setupEditFixture(t, t.TempDir())
	fx.editorFn = func(path string) error {
		return os.WriteFile(path, []byte(`{"command": "cat", "envv": {"A": "1"}}`), 0o600)
	}
	err := EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "envv") || !strings.Contains(err.Error(), "command, args and env") {
		t.Fatalf("err = %v, want the unknown field named with the allowed ones", err)
	}
	servers, _, _ := LoadServerFile(fx.serversPath)
	if servers["slack"].Command != "cat" || servers["slack"].Env != nil {
		t.Fatal("a definition with an unknown field was saved")
	}
}

// SIGTERM while the probe runs stops the probe; the temp copy (env values)
// is still removed and nothing is saved.
func TestEditServer_SignalDuringProbeCleansUp(t *testing.T) {
	fx := setupEditFixture(t, t.TempDir())
	var temp string
	fx.editorFn = func(path string) error {
		temp = path
		return setArgs("--v2")(path)
	}
	fx.probeCtxFn = func(ctx context.Context, cfg ServerConfig) ([]json.RawMessage, error) {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("the signal did not stop the probe")
		}
	}
	err := EditServer(fx.ctx, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatalf("temp file left at %s", temp)
	}
	if servers, _, _ := LoadServerFile(fx.serversPath); len(servers["slack"].Args) != 0 {
		t.Fatal("saved although interrupted")
	}
}

func TestEditServer_TakesTheInstallLock(t *testing.T) {
	fx := setupEditFixture(t, t.TempDir())
	unlock, err := lockInstall(fx.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fx.editorFn = func(string) error { t.Fatal("the editor opened while install holds the lock"); return nil }
	if err := EditServer(fx.ctx, fx.opts); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("err = %v", err)
	}
}

// The probe runs the server as launchd will: PATH and HOME as the plist
// sets them plus the definition's env, nothing else from this shell.
func TestProbeServerDirect_CleanEnvironment(t *testing.T) {
	t.Setenv("AIMUX_EDIT_SHELL_ONLY", "x")
	base := map[string]string{"PATH": "/usr/bin:/bin", "HOME": t.TempDir()}
	cfg := ServerConfig{Command: os.Args[0], Env: map[string]string{testServerEnv: "1", testRequireEnv: "AIMUX_EDIT_SHELL_ONLY"}}
	if _, err := ProbeServerDirect(context.Background(), cfg, 10*time.Second, base); err == nil {
		t.Fatal("a server relying on a shell-only variable passed the probe")
	}
	cfg.Env["AIMUX_EDIT_SHELL_ONLY"] = "x"
	tools, err := ProbeServerDirect(context.Background(), cfg, 10*time.Second, base)
	if err != nil || len(tools) == 0 {
		t.Fatalf("tools %d, err %v; want the probe to pass with the variable in the definition", len(tools), err)
	}
}

func TestProbeServerDirect_ZeroToolsAllowed(t *testing.T) {
	cfg := ServerConfig{Command: os.Args[0], Env: map[string]string{testNoToolsEnv: "1"}}
	tools, err := ProbeServerDirect(context.Background(), cfg, 10*time.Second, map[string]string{"PATH": "/usr/bin:/bin"})
	if err != nil || tools == nil || len(tools) != 0 {
		t.Fatalf("tools %#v, err %v; want a server with no tools accepted as an empty list", tools, err)
	}
}

// A new definition whose server lists no tools replaces the old entry with
// an empty one: the reload must not keep advertising the old tools.
func TestEditServer_NoToolsRecordsAnEmptyList(t *testing.T) {
	fx := setupEditFixture(t, t.TempDir())
	fx.editorFn = setArgs("--v2")
	fx.probeTools = nil
	must(t, EditServer(fx.ctx, fx.opts))
	cat, err := LoadCatalog(fx.catalogPath)
	must(t, err)
	e, ok := cat.Servers["slack"]
	if !ok || len(e.Tools) != 0 {
		t.Fatalf("slack entry %+v (present %v), want an empty tool list", e, ok)
	}
}

// binDir is a directory holding an executable link named name to the test
// binary, which acts as the test MCP server.
func binDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(os.Args[0], filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLookPathIn(t *testing.T) {
	dir := binDir(t, "only-here")
	if got, err := lookPathIn("only-here", "/nonexistent:"+dir); err != nil || got != filepath.Join(dir, "only-here") {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := lookPathIn("only-here", t.TempDir()); err == nil {
		t.Fatal("found a command that is not on the given PATH")
	}
	if got, err := lookPathIn("/bin/sh", ""); err != nil || got != "/bin/sh" {
		t.Fatalf("absolute path: %q, %v", got, err)
	}
	notExec := filepath.Join(t.TempDir(), "plain")
	must(t, os.WriteFile(notExec, []byte("x"), 0o600))
	if _, err := lookPathIn("plain", filepath.Dir(notExec)); err == nil {
		t.Fatal("a non-executable file was accepted")
	}
}

// The probe resolves a bare command on the base PATH (the proxy's), not on
// this process's PATH.
func TestProbeServerDirect_ResolvesOnTheBasePath(t *testing.T) {
	cfg := ServerConfig{Command: "aimux-test-mcp", Env: map[string]string{testServerEnv: "1"}}
	onBase := binDir(t, "aimux-test-mcp")
	if _, err := ProbeServerDirect(context.Background(), cfg, 10*time.Second, map[string]string{"PATH": onBase}); err != nil {
		t.Fatalf("a command on the base PATH failed: %v", err)
	}
	t.Setenv("PATH", binDir(t, "aimux-test-mcp")+":"+os.Getenv("PATH"))
	_, err := ProbeServerDirect(context.Background(), cfg, 10*time.Second, map[string]string{"PATH": t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "aimux-test-mcp") {
		t.Fatalf("err = %v, want a command found only on this process's PATH refused", err)
	}
}

func TestEditServer_ValidatesTheCommandOnTheBasePath(t *testing.T) {
	fx := setupEditFixture(t, t.TempDir())
	fx.opts.BaseEnv = map[string]string{"PATH": t.TempDir()} // no "echo" there
	fx.editorFn = func(path string) error {
		return os.WriteFile(path, []byte(`{"command": "echo"}`), 0o600)
	}
	if err := EditServer(fx.ctx, fx.opts); err == nil || !strings.Contains(err.Error(), "echo") {
		t.Fatalf("err = %v, want echo refused on the proxy's PATH", err)
	}
}

func TestEditorCommand(t *testing.T) {
	cases := []struct {
		editor string
		want   []string
	}{
		{"", []string{"vi", "/tmp/f.json"}},
		{"code --wait", []string{"code", "--wait", "/tmp/f.json"}},
		{"vim -n", []string{"vim", "-n", "/tmp/f.json"}},
		{`"/Applications/My Editor/bin/ed" -w`, []string{"/Applications/My Editor/bin/ed", "-w", "/tmp/f.json"}},
		{`'/opt/my ed' --flag="a b"`, []string{"/opt/my ed", "--flag=a b", "/tmp/f.json"}},
		{`/opt/my\ ed`, []string{"/opt/my ed", "/tmp/f.json"}},
	}
	for _, c := range cases {
		got, err := editorCommand(c.editor, "/tmp/f.json")
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("editorCommand(%q) = %q, %v; want %q", c.editor, got, err, c.want)
		}
	}
	if _, err := editorCommand(`"unterminated`, "/tmp/f.json"); err == nil {
		t.Error("an unterminated quote was accepted")
	}
}

// listErrorConfig is the test server whose tools/list answers a JSON-RPC
// error with code (or a malformed result), after advertising tools.
func listErrorConfig(code string, malformed bool) ServerConfig {
	env := map[string]string{testListErrEnv: "1", testListCodeEnv: code, testEchoEnv: "unauthorized"}
	if malformed {
		env[testListMalformedEnv] = "1"
	}
	return ServerConfig{Command: os.Args[0], Env: env}
}

// Only "method not found" (or no tools capability) means "no tools": any
// other listing failure fails the probe.
func TestProbeServerDirect_OnlyMethodNotFoundMeansNoTools(t *testing.T) {
	base := map[string]string{"PATH": "/usr/bin:/bin"}
	tools, err := ProbeServerDirect(context.Background(), listErrorConfig("-32601", false), 10*time.Second, base)
	if err != nil || tools == nil || len(tools) != 0 {
		t.Fatalf("-32601: tools %#v, err %v; want an empty list", tools, err)
	}
	if _, err := ProbeServerDirect(context.Background(), listErrorConfig("-32000", false), 10*time.Second, base); err == nil {
		t.Fatal("an auth error (-32000) was taken as no tools")
	}
	if _, err := ProbeServerDirect(context.Background(), listErrorConfig("-32603", true), 10*time.Second, base); err == nil {
		t.Fatal("a malformed tools/list result was taken as no tools")
	}
}

func TestEditServer_ListingErrorKeepsDefinitionAndCatalog(t *testing.T) {
	for name, cfg := range map[string]ServerConfig{"auth": listErrorConfig("-32000", false), "malformed": listErrorConfig("-32603", true)} {
		t.Run(name, func(t *testing.T) {
			fx := setupEditFixture(t, t.TempDir())
			catBefore, err := os.ReadFile(fx.catalogPath)
			must(t, err)
			fx.editorFn = func(path string) error {
				b, err := json.Marshal(cfg)
				if err != nil {
					return err
				}
				return os.WriteFile(path, b, 0o600)
			}
			fx.probeCtxFn = func(ctx context.Context, c ServerConfig) ([]json.RawMessage, error) {
				return ProbeServerDirect(ctx, c, 10*time.Second, nil)
			}
			if err := EditServer(fx.ctx, fx.opts); err == nil {
				t.Fatal("edit saved a definition whose server failed to list its tools")
			}
			servers, _, _ := LoadServerFile(fx.serversPath)
			catAfter, _ := os.ReadFile(fx.catalogPath)
			if servers["slack"].Command != "cat" || string(catAfter) != string(catBefore) {
				t.Fatal("the definition or the catalog changed")
			}
		})
	}
}
