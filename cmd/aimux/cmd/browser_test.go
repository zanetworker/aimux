package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/browserhost"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

const testExtID = "abcdefghijklmnopabcdefghijklmnop"

// fakeRun records every command and answers from a script.
type fakeRun struct {
	noNode bool // `node -p process.execPath` fails (node not installed)
	mu     sync.Mutex
	calls  []runCall
	answer func(argv []string) (string, error) // nil: "", nil
}

type runCall struct {
	env  []string
	argv []string
}

func (f *fakeRun) run(_ context.Context, env []string, argv ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, runCall{append([]string(nil), env...), append([]string(nil), argv...)})
	f.mu.Unlock()
	if hasPrefix(argv, []string{"node", "-p", "process.execPath"}) {
		if f.noNode {
			return "", errors.New("exec: \"node\": executable file not found in $PATH")
		}
		return "/opt/node/22.1.0/bin/node\n", nil
	}
	if f.answer == nil {
		return "", nil
	}
	return f.answer(argv)
}

func (f *fakeRun) find(prefix ...string) []runCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runCall
	for _, c := range f.calls {
		if hasPrefix(c.argv, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func hasPrefix(argv, prefix []string) bool {
	if len(argv) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if argv[i] != p {
			return false
		}
	}
	return true
}

func testEnv(t *testing.T, f *fakeRun) browserEnv {
	t.Helper()
	return browserEnv{
		Home:       t.TempDir(),
		Executable: func() (string, error) { return "/opt/aimux/bin/aimux", nil },
		Run:        f.run,
		Health:     func(context.Context) error { return nil },
		LookPath:   fakeToolLookPath,
		TempRoots:  []string{"/nonexistent-temp-root"},
		Catalog: func(context.Context, mcpproxy.ReadonlyConfig) (*mcpproxy.Catalog, map[string]error) {
			return &mcpproxy.Catalog{Servers: map[string]mcpproxy.CatalogEntry{}}, nil
		},
	}
}

func TestBrowserInstallWritesManifestsAndWrapper(t *testing.T) {
	f := &fakeRun{}
	env := testEnv(t, f)
	if err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(env.Home, ".aimux", "wingman", "host.sh")
	fi, err := os.Stat(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("wrapper mode = %v, want 0700", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(wrapper) //nolint:gosec // G304: path is a test temp or fixed testdata path
	want := "#!/bin/sh\nexport PATH='/opt/node/22.1.0/bin:/opt/tools/bin':\"$PATH\"\nexport AIMUX_WINGMAN_PATH_PINNED=1\nexec '/opt/aimux/bin/aimux' browser host \"$@\"\n"
	if string(data) != want {
		t.Fatalf("wrapper = %q, want %q", data, want)
	}
	wantManifest := map[string]any{
		"name":            "com.zanetworker.wingman",
		"description":     "Wingman reading side panel",
		"path":            wrapper,
		"type":            "stdio",
		"allowed_origins": []any{"chrome-extension://" + testExtID + "/"},
	}
	wb, _ := json.Marshal(wantManifest)
	for _, browser := range []string{"Chrome", "Chrome Beta"} {
		p := filepath.Join(env.Home, "Library", "Application Support", "Google", browser, "NativeMessagingHosts", "com.zanetworker.wingman.json")
		raw, err := os.ReadFile(p) //nolint:gosec // G304: path is a test temp or fixed testdata path
		if err != nil {
			t.Fatalf("%s manifest: %v", browser, err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s manifest json: %v", browser, err)
		}
		gb, _ := json.Marshal(got)
		if string(gb) != string(wb) {
			t.Fatalf("%s manifest = %s, want %s", browser, gb, wb)
		}
	}
	npm := f.find("npm", "install")
	if len(npm) != 1 {
		t.Fatalf("npm calls = %d, want 1", len(npm))
	}
	wantNpm := "npm install --prefix " + filepath.Join(env.Home, ".aimux", "wingman", "adapter") + " @agentclientprotocol/claude-agent-acp@0.101.0"
	if strings.Join(npm[0].argv, " ") != wantNpm {
		t.Fatalf("npm argv = %q", npm[0].argv)
	}
}

func TestBrowserInstallCustomDirsAndExistingAdapter(t *testing.T) {
	f := &fakeRun{}
	env := testEnv(t, f)
	adapter := filepath.Join(env.Home, ".aimux", "wingman", "adapter", "node_modules", ".bin", "claude-agent-acp")
	if err := os.MkdirAll(filepath.Dir(adapter), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // G306: the fake adapter must be executable
		t.Fatal(err)
	}
	a, b := filepath.Join(t.TempDir(), "A"), filepath.Join(t.TempDir(), "B")
	if err := browserInstall(context.Background(), env, testExtID, []string{a, b}, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{a, b} {
		if _, err := os.Stat(filepath.Join(d, "NativeMessagingHosts", "com.zanetworker.wingman.json")); err != nil {
			t.Fatalf("manifest in %s: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(env.Home, "Library")); err == nil {
		t.Fatal("default Chrome dirs written although --chrome-dirs was given")
	}
	if n := len(f.find("npm")); n != 0 {
		t.Fatalf("npm ran %d times although the adapter exists", n)
	}
}

func TestBrowserInstallRequiresExtensionID(t *testing.T) {
	for _, id := range []string{"", "abc", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("q", 32), strings.ToUpper(testExtID), "abcdefghijklmnopabcdefghijklmno/"} {
		f := &fakeRun{}
		env := testEnv(t, f)
		if err := browserInstall(context.Background(), env, id, nil, false, &bytes.Buffer{}); err == nil {
			t.Fatalf("id %q accepted", id)
		}
		if _, err := os.Stat(filepath.Join(env.Home, ".aimux", "wingman", "host.sh")); err == nil {
			t.Fatalf("id %q: wrapper written before validation", id)
		}
		if len(f.calls) != 0 {
			t.Fatalf("id %q: ran %v", id, f.calls)
		}
	}
	if err := validExtensionID(strings.Repeat("p", 32)); err != nil {
		t.Fatalf("boundary id rejected: %v", err)
	}
	cmd := newBrowserCmdWith(testEnv(t, &fakeRun{}))
	cmd.SetArgs([]string{"install"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("install without --extension-id succeeded")
	}
}

// The wrapper must run the binary even when its path has spaces and quotes.
func TestBrowserWrapperQuotesOddPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "ai mux")
	outFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\n{ printf '%s\\n' \"$PATH\" \"$AIMUX_WINGMAN_PATH_PINNED\"; for a; do printf '%s\\n' \"$a\"; done; } > \"$OUT\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // G306: the fake binary must be executable
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "host.sh")
	pinned := []string{"/opt/it's node/bin", "/opt/open shell"}
	if err := writeHostWrapper(wrapper, bin, pinned); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(wrapper, "chrome-extension://x/", "--parent-window=0")           //nolint:gosec // G204: wrapper is the launcher script this test just generated
	c.Env = append(os.Environ(), "OUT="+outFile, "PATH=/usr/bin:/bin:/usr/sbin:/sbin") // what Chrome passes
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, _ := os.ReadFile(outFile) //nolint:gosec // G304: path is a test temp or fixed testdata path
	want := "/opt/it's node/bin:/opt/open shell:/usr/bin:/bin:/usr/sbin:/sbin\n1\nbrowser\nhost\nchrome-extension://x/\n--parent-window=0\n"
	if string(got) != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

// setupScript answers like a fresh machine: no image, no profile, no provider,
// no sandbox; the sandbox is Ready once created.
func setupScript(token string, created *bool) func(argv []string) (string, error) {
	return func(argv []string) (string, error) {
		s := strings.Join(argv, " ")
		switch {
		case strings.HasPrefix(s, "podman image exists"):
			return "", errors.New("exit status 1")
		case strings.HasPrefix(s, "openshell -g local provider list-profiles"):
			return "NAME  TYPE  CATEGORY\nother  provider  AGENT\n", nil
		case strings.HasPrefix(s, "openshell -g local provider list"):
			return "NAME    TYPE              CREDENTIAL_KEYS   CONFIG_KEYS\nvertex  google-vertex-ai  1  3\n", nil
		case strings.HasPrefix(s, "security find-generic-password"):
			return token + "\n", nil
		case strings.HasPrefix(s, "openshell -g local sandbox create"):
			*created = true
			return "", nil
		case strings.HasPrefix(s, "openshell -g local sandbox list"):
			if *created {
				return "NAME          CREATED              PHASE\n\x1b[1mwingman-work\x1b[0m  2026-11-09 10:00:00  \x1b[32mReady\x1b[39m\n", nil
			}
			return "No sandboxes found.\n", nil
		}
		return "", nil
	}
}

func TestBrowserSetupArgv(t *testing.T) {
	const token = "sk-ant-oat01-SECRETSECRET" //nolint:gosec // G101: fake token for the redaction test
	created := false
	f := &fakeRun{answer: setupScript(token, &created)}
	env := testEnv(t, f)
	sandboxDir := filepath.Join(t.TempDir(), "sandbox")
	var out bytes.Buffer
	if err := browserSetup(context.Background(), env, sandboxDir, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	img := "localhost/wingman-sandbox:2.1.319"
	if b := f.find("podman", "build"); len(b) != 1 || strings.Join(b[0].argv, " ") != "podman build -t "+img+" -f "+filepath.Join(sandboxDir, "Containerfile")+" "+sandboxDir {
		t.Fatalf("podman build = %v", b)
	}
	if p := f.find("openshell", "-g", "local", "provider", "profile", "import"); len(p) != 1 || strings.Join(p[0].argv, " ") != "openshell -g local provider profile import -f "+filepath.Join(sandboxDir, "claude-code-oauth.yaml")+" --global" {
		t.Fatalf("profile import = %v", p)
	}
	pc := f.find("openshell", "-g", "local", "provider", "create")
	if len(pc) != 1 {
		t.Fatalf("provider create calls = %d", len(pc))
	}
	if strings.Join(pc[0].argv, " ") != "openshell -g local provider create --name wingman-claude --type claude-code-oauth --credential CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("provider create argv = %q", pc[0].argv)
	}
	if len(pc[0].env) != 1 || pc[0].env[0] != "CLAUDE_CODE_OAUTH_TOKEN="+token {
		t.Fatal("provider create did not get exactly the token in its environment")
	}
	// The work sandbox is serve's to create (aimux-work-readonly, from the
	// compiled profile); setup no longer touches any sandbox.
	if sc := f.find("openshell", "-g", "local", "sandbox"); len(sc) != 0 {
		t.Fatalf("setup touched sandboxes: %v", sc)
	}
	if !strings.Contains(out.String(), "aimux remote serve creates the work sandbox aimux-work-readonly on first use") {
		t.Fatalf("setup does not say who creates the work sandbox:\n%s", out.String())
	}
	for _, c := range f.calls {
		joined := strings.Join(c.argv, " ")
		if strings.Contains(joined, token) {
			t.Fatalf("token in argv: %q", c.argv)
		}
		if strings.Contains(joined, "community") {
			t.Fatalf("community image used: %q", c.argv)
		}
		if c.argv[0] == "openshell" && !hasPrefix(c.argv, []string{"openshell", "-g", "local"}) {
			t.Fatalf("openshell call without -g local: %q", c.argv)
		}
		if c.argv[0] != "openshell" || !hasPrefix(c.argv, []string{"openshell", "-g", "local", "provider", "create"}) {
			if len(c.env) != 0 {
				t.Fatalf("env passed to %q", c.argv)
			}
		}
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("token printed")
	}
	cfg := filepath.Join(env.Home, ".aimux", "wingman", "readonly.json")
	fi, err := os.Stat(cfg)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("readonly.json: %v %v", fi, err)
	}
	loaded, err := mcpproxy.LoadReadonlyConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WikiRoot != mcpproxy.DefaultReadonlyConfig(env.Home).WikiRoot {
		t.Fatalf("wiki root = %s", loaded.WikiRoot)
	}
	if !strings.Contains(out.String(), "wiki root") {
		t.Fatalf("no wiki root warning for a missing wiki: %s", out.String())
	}
}

func TestBrowserSetupSkipsExistingAndLeavesSandboxesAlone(t *testing.T) {
	f := &fakeRun{answer: func(argv []string) (string, error) {
		s := strings.Join(argv, " ")
		switch {
		case strings.HasPrefix(s, "openshell -g local provider list-profiles"):
			return "NAME               TYPE      CATEGORY\nclaude-code-oauth  provider  AGENT\n", nil
		case strings.HasPrefix(s, "openshell -g local provider list"):
			return "NAME            TYPE               CREDENTIAL_KEYS\nwingman-claude  claude-code-oauth  1\n", nil
		case strings.HasPrefix(s, "openshell -g local sandbox list"):
			return "NAME  CREATED  PHASE\nwingman-work  2026-11-09 10:00:00  Ready\n", nil
		}
		return "", nil
	}}
	env := testEnv(t, f)
	if err := browserSetup(context.Background(), env, t.TempDir(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(f.find("podman", "build")) != 0 || len(f.find("openshell", "-g", "local", "provider", "profile")) != 0 ||
		len(f.find("openshell", "-g", "local", "provider", "create")) != 0 || len(f.find("security")) != 0 {
		t.Fatalf("setup redid existing steps: %v", f.calls)
	}
	if sc := f.find("openshell", "-g", "local", "sandbox"); len(sc) != 0 {
		t.Fatalf("setup touched the old wingman-work sandbox: %v", sc)
	}
}

func TestBrowserSetupKeepsExistingReadonlyConfig(t *testing.T) {
	created := false
	f := &fakeRun{answer: setupScript("tok", &created)}
	env := testEnv(t, f)
	cfg := filepath.Join(env.Home, ".aimux", "wingman", "readonly.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	custom := `{"HTTP":{},"WikiRoot":"` + env.Home + `","Allow":{"wiki":["wiki_read"]}}`
	if err := os.WriteFile(cfg, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := browserSetup(context.Background(), env, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg) //nolint:gosec // G304: path is a test temp or fixed testdata path
	if string(got) != custom {
		t.Fatalf("readonly.json overwritten: %s", got)
	}
	if strings.Contains(out.String(), "wiki root") {
		t.Fatalf("warned about an existing wiki root: %s", out.String())
	}
}

func TestBrowserSetupMissingKeychainToken(t *testing.T) {
	created := false
	base := setupScript("", &created)
	f := &fakeRun{answer: func(argv []string) (string, error) {
		if argv[0] == "security" {
			return "", exitCodeErr{44, "security: The specified item could not be found in the keychain."}
		}
		return base(argv)
	}}
	env := testEnv(t, f)
	var out bytes.Buffer
	err := browserSetup(context.Background(), env, t.TempDir(), &out)
	if err == nil {
		t.Fatal("setup succeeded without the Keychain token")
	}
	for _, want := range []string{"claude setup-token", "security add-generic-password", "wingman-claude-oauth"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in instructions: %s", want, out.String())
		}
	}
	if len(f.find("openshell", "-g", "local", "provider", "create")) != 0 || len(f.find("openshell", "-g", "local", "sandbox", "create")) != 0 {
		t.Fatal("setup went on without the token")
	}
}

func TestBrowserSetupStopsWhenGatewayDown(t *testing.T) {
	f := &fakeRun{}
	env := testEnv(t, f)
	env.Health = func(context.Context) error { return errors.New("OpenShell gateway down") }
	err := browserSetup(context.Background(), env, t.TempDir(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("err = %v", err)
	}
	if len(f.find("openshell", "-g", "local", "sandbox")) != 0 {
		t.Fatal("touched sandboxes with the gateway down")
	}
}

const testHostToken = "TOKEN-0123456789abcdef"

type hostFakes struct {
	listenErr error
	buildErr  error
	errs      map[string]error
	served    chan mcpproxy.DaemonOptions
	reject    func(server, tool string, err error)
	pins      mcpproxy.ToolPins
}

func newHostTestEnv(t *testing.T, fk *hostFakes, logw io.Writer) hostEnv {
	t.Helper()
	home := t.TempDir()
	return hostEnv{
		Home: home,
		Log:  log.New(logw, "", 0),
		Listen: func(network, addr string) (net.Listener, error) {
			if network != "tcp" || addr != "127.0.0.1:7390" {
				t.Errorf("listen %s %s", network, addr)
			}
			if fk.listenErr != nil {
				return nil, fk.listenErr
			}
			return net.Listen("tcp", "127.0.0.1:0")
		},
		Build: func(_ context.Context, _ mcpproxy.ReadonlyConfig, pins mcpproxy.ToolPins, reject func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
			fk.reject, fk.pins = reject, pins
			if fk.buildErr != nil {
				return nil, nil, nil, fk.buildErr
			}
			return map[string]*server.MCPServer{}, nil, fk.errs, nil
		},
		Serve: func(ctx context.Context, ln net.Listener, _ map[string]*server.MCPServer, _ *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error {
			fk.served <- opt
			<-ctx.Done()
			_ = ln.Close()
			return nil
		},
	}
}

func TestBrowserHostLogFileModes(t *testing.T) {
	home := t.TempDir()
	f, err := openHostLog(home)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	dir := filepath.Join(home, "Library", "Logs", "aimux", "wingman")
	di, _ := os.Stat(dir)
	fi, _ := os.Stat(filepath.Join(dir, "host.log"))
	if di == nil || fi == nil || di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("modes dir=%v file=%v", di, fi)
	}
}

func TestNewHostToken(t *testing.T) {
	a, err := newHostToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newHostToken()
	if len(a) != 64 || a == b || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("tokens %q %q", a, b)
	}
}

// The host subcommand accepts Chrome's extra arguments.
func TestBrowserHostAcceptsChromeArgs(t *testing.T) {
	cmd := newBrowserCmdWith(testEnv(t, &fakeRun{}))
	host, _, err := cmd.Find([]string{"host"})
	if err != nil {
		t.Fatal(err)
	}
	if host.Args != nil {
		if err := host.Args(host, []string{"chrome-extension://" + testExtID + "/", "--parent-window=0"}); err != nil {
			t.Fatalf("args rejected: %v", err)
		}
	}
	if err := host.ParseFlags([]string{"chrome-extension://x/", "--parent-window=0"}); err != nil {
		t.Fatalf("flags rejected: %v", err)
	}
}

// exitCodeErr stands in for *exec.ExitError in fakes.
type exitCodeErr struct {
	code int
	msg  string
}

func (e exitCodeErr) Error() string { return e.msg }
func (e exitCodeErr) ExitCode() int { return e.code }

func TestBrowserInstallRefusesTempBinary(t *testing.T) {
	for _, exe := range []string{
		filepath.Join(os.TempDir(), "aimux-wingman"),
		"/tmp/aimux-wingman",
		"/private/tmp/aimux-wingman",
		"/Users/x/Library/Caches/go-build/ab/cd/exe/aimux",
		"/var/folders/xy/T/go-build123456/b001/exe/aimux",
	} {
		f := &fakeRun{}
		env := testEnv(t, f)
		env.TempRoots = defaultTempRoots()
		env.Executable = func() (string, error) { return exe, nil }
		err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--allow-temp-binary") {
			t.Fatalf("%s: err = %v", exe, err)
		}
		if _, err := os.Stat(filepath.Join(env.Home, ".aimux", "wingman", "host.sh")); err == nil {
			t.Fatalf("%s: wrapper written", exe)
		}
		if len(f.calls) != 0 {
			t.Fatalf("%s: ran %v", exe, f.calls)
		}
	}
	// The override flag lets tests and dev builds through.
	f := &fakeRun{}
	env := testEnv(t, f)
	env.TempRoots = defaultTempRoots()
	env.Executable = func() (string, error) { return "/tmp/aimux-wingman", nil }
	if err := browserInstall(context.Background(), env, testExtID, nil, true, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(env.Home, ".aimux", "wingman", "host.sh"))
	if !strings.Contains(string(data), "'/tmp/aimux-wingman'") {
		t.Fatalf("wrapper = %q", data)
	}
	// Boundary: a sibling dir sharing the /tmp prefix is not temp.
	if isTempBinary("/tmpfoo/aimux", []string{"/tmp"}) {
		t.Fatal("/tmpfoo counted as temp")
	}
}

func TestBrowserInstallKeepsSymlinkAndPrefersPath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "Cellar", "aimux", "1.2.3", "bin", "aimux")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // G306: the fake binary must be executable
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin", "aimux")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other-aimux")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n# other\n"), 0o700); err != nil { //nolint:gosec // G306: the fake binary must be executable
		t.Fatal(err)
	}
	cases := []struct {
		name, exe, path, want string
	}{
		{"PATH entry for the same binary wins", real, link, link},
		{"symlinked exe is not resolved", link, "", link},
		{"PATH entry for another binary is ignored", real, other, real},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := testEnv(t, &fakeRun{})
			env.Executable = func() (string, error) { return c.exe, nil }
			if c.path != "" {
				env.LookPath = func(string) (string, error) { return c.path, nil }
			}
			if err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(env.Home, ".aimux", "wingman", "host.sh"))
			if want := "exec '" + c.want + "' browser host"; !strings.Contains(string(data), want) {
				t.Fatalf("wrapper = %q, want %q", data, want)
			}
		})
	}
}

func TestBrowserInstallTightensExistingDir(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	dir := filepath.Join(env.Home, ".aimux", "wingman")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: the test needs a loosened dir to prove it is tightened
		t.Fatal(err)
	}
	if err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", fi.Mode().Perm())
	}
}

func TestBrowserInstallAdapterFailureWritesNoManifest(t *testing.T) {
	f := &fakeRun{answer: func(argv []string) (string, error) {
		if argv[0] == "npm" {
			return "", errors.New("npm: network down")
		}
		return "", nil
	}}
	env := testEnv(t, f)
	err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("err = %v", err)
	}
	p := filepath.Join(env.Home, "Library", "Application Support", "Google", "Chrome", "NativeMessagingHosts", "com.zanetworker.wingman.json")
	if _, err := os.Stat(p); err == nil {
		t.Fatal("manifest written although the adapter install failed")
	}
}

func TestBrowserSetupRedactsTokenFromErrors(t *testing.T) {
	const token = "sk-ant-oat01-LEAKYLEAKY" //nolint:gosec // G101: fake token for the redaction test
	created := false
	base := setupScript(token, &created)
	f := &fakeRun{answer: func(argv []string) (string, error) {
		if hasPrefix(argv, []string{"openshell", "-g", "local", "provider", "create"}) {
			return "", errors.New("openshell -g local provider: exit status 1: bad credential " + token)
		}
		return base(argv)
	}}
	env := testEnv(t, f)
	var out bytes.Buffer
	err := browserSetup(context.Background(), env, t.TempDir(), &out)
	if err == nil || !strings.Contains(err.Error(), "wingman-claude") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(out.String(), token) {
		t.Fatalf("token surfaced: %v / %s", err, out.String())
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("no redaction marker: %v", err)
	}
}

func TestRunCommandRedactsEnvValues(t *testing.T) {
	const secret = "s3cr3t-token-value"
	_, err := runCommand(context.Background(), []string{"WINGMAN_T=" + secret}, "sh", "-c", `echo "boom $WINGMAN_T" >&2; exit 3`)
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "boom [redacted]") {
		t.Fatalf("err = %v", err)
	}
	var ec interface{ ExitCode() int }
	if !errors.As(err, &ec) || ec.ExitCode() != 3 {
		t.Fatalf("exit code lost: %v", err)
	}
}

func TestBrowserSetupKeychainOtherFailure(t *testing.T) {
	created := false
	base := setupScript("", &created)
	f := &fakeRun{answer: func(argv []string) (string, error) {
		if argv[0] == "security" {
			return "", exitCodeErr{51, "security: user interaction is not allowed"}
		}
		return base(argv)
	}}
	env := testEnv(t, f)
	var out bytes.Buffer
	err := browserSetup(context.Background(), env, t.TempDir(), &out)
	if err == nil || !strings.Contains(err.Error(), "user interaction is not allowed") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out.String(), "claude setup-token") {
		t.Fatalf("printed setup instructions for a non-missing failure: %s", out.String())
	}
}

// verifyEnv is a browserEnv whose read-only server, sandbox Ensure and
// sandbox exec are fake, with the wiki and jira tools pinned.
type verifyRig struct {
	env     browserEnv
	argv    []string
	stdin   string
	ensured []profile.Compiled
	served  chan mcpproxy.DaemonOptions
	stopped bool
	optMu   sync.Mutex
	opt     mcpproxy.DaemonOptions
	// at exec time: the stdin token's session and tools on the server
	session string
	allow   map[string][]string
}

func verifyEnv(t *testing.T, execOut string, listenErr error) *verifyRig {
	t.Helper()
	r := &verifyRig{env: testEnv(t, &fakeRun{}), served: make(chan mcpproxy.DaemonOptions, 1)}
	if err := (toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(r.env.Home)}).Save(toolgateway.Pins{
		"wiki": {"wiki_search": "p1"}, "jira": {"search": "p2"}, "slack": {"history": "p3"}, "google": {"get_doc": "p4"}}); err != nil {
		t.Fatal(err)
	}
	built := false
	r.env.NewToken = func() (string, error) { return testHostToken, nil }
	r.env.Listen = func(network, addr string) (net.Listener, error) {
		if addr != "127.0.0.1:7390" {
			t.Errorf("listen %s", addr)
		}
		if listenErr != nil {
			return nil, listenErr
		}
		return net.Listen("tcp", "127.0.0.1:0")
	}
	r.env.Build = func(context.Context, mcpproxy.ReadonlyConfig, mcpproxy.ToolPins, func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		built = true
		return map[string]*server.MCPServer{}, nil, nil, nil
	}
	r.env.Serve = func(ctx context.Context, ln net.Listener, _ map[string]*server.MCPServer, _ *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error {
		r.optMu.Lock()
		r.opt = opt
		r.optMu.Unlock()
		r.served <- opt
		<-ctx.Done()
		r.stopped = true
		_ = ln.Close()
		return nil
	}
	r.env.Ensure = func(_ context.Context, c profile.Compiled) error {
		r.ensured = append(r.ensured, c)
		return nil
	}
	r.env.Exec = func(_ context.Context, in string, a ...string) (string, error) {
		if !built || len(r.ensured) == 0 {
			t.Error("exec before the server was built or the sandbox ensured")
		}
		r.argv, r.stdin = a, in
		deadline := time.Now().Add(2 * time.Second) // Serve runs in its own goroutine
		for {
			r.optMu.Lock()
			tokens := r.opt.Tokens
			r.optMu.Unlock()
			if tokens != nil || time.Now().After(deadline) {
				if tokens != nil {
					tok, _, _ := strings.Cut(in, "\n")
					r.session, r.allow, _ = tokens.Lookup(tok)
				}
				break
			}
			time.Sleep(time.Millisecond)
		}
		return execOut, nil
	}
	return r
}

// verify compiles work-readonly, ensures aimux-work-readonly (the sandbox
// work turns run in), and runs node checks there with an issued session
// token on stdin: never curl (the compiled policy allows node and claude
// only), never the token on argv.
func TestBrowserVerifyPasses(t *testing.T) {
	r := verifyEnv(t, "internet 000\nauth 200\nnoauth 401\nhostport 403\nhostgw 000\n", nil)
	var out bytes.Buffer
	if err := browserVerify(context.Background(), r.env, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	opt := <-r.served
	if opt.Tokens == nil || opt.Status != nil || opt.Reload != nil {
		t.Fatal("server not started with a session token table only")
	}
	if len(r.ensured) != 1 || r.ensured[0].Sandbox != "aimux-work-readonly" || r.ensured[0].Profile != "work-readonly" {
		t.Fatalf("ensured %+v, want aimux-work-readonly", r.ensured)
	}
	want := []string{"openshell", "-g", "local", "sandbox", "exec", "--no-login-shell", "--no-tty", "aimux-work-readonly", "--",
		"/usr/bin/env", "NODE_USE_ENV_PROXY=1", "/usr/bin/node", "-e"}
	if !hasPrefix(r.argv, want) || len(r.argv) != len(want)+1 {
		t.Fatalf("argv = %q", r.argv)
	}
	script := r.argv[len(r.argv)-1]
	for _, w := range []string{"fetch(", "AbortSignal.timeout", "https://example.com", "http://127.0.0.1:7381/",
		"http://host.containers.internal:7381/", "http://host.containers.internal:7390/mcp/google"} {
		if !strings.Contains(script, w) {
			t.Errorf("node script lacks %q", w)
		}
	}
	if strings.Contains(strings.Join(r.argv, " "), "curl") {
		t.Fatal("curl is not allowed by the compiled policy")
	}
	tok, _, _ := strings.Cut(r.stdin, "\n")
	if r.session == "" || len(r.allow["jira"]) == 0 || len(r.allow["google"]) == 0 {
		t.Fatalf("stdin token not an issued session token for the pinned tools: %q %v", r.session, r.allow)
	}
	for _, a := range r.argv {
		if strings.Contains(a, tok) {
			t.Fatal("token in argv")
		}
	}
	if strings.Count(out.String(), "PASS") != 5 || strings.Contains(out.String(), "FAIL") || strings.Contains(out.String(), tok) {
		t.Fatalf("out = %s", out.String())
	}
	if !r.stopped {
		t.Fatal("server still running after verify")
	}
	if _, _, ok := opt.Tokens.Lookup(tok); ok {
		t.Fatal("test token still valid after verify")
	}
}

func TestBrowserVerifyFailures(t *testing.T) {
	cases := map[string]string{
		"internet open":   "internet 200\nauth 200\nnoauth 401\nhostport 000\nhostgw 000\n",
		"token rejected":  "internet 000\nauth 401\nnoauth 401\nhostport 000\nhostgw 000\n",
		"no auth needed":  "internet 000\nauth 200\nnoauth 200\nhostport 000\nhostgw 000\n",
		"host port open":  "internet 000\nauth 200\nnoauth 401\nhostport 200\nhostgw 000\n",
		"host gw open":    "internet 000\nauth 200\nnoauth 401\nhostport 000\nhostgw 200\n",
		"missing results": "internet 000\n",
	}
	for name, execOut := range cases {
		t.Run(name, func(t *testing.T) {
			r := verifyEnv(t, execOut, nil)
			var out bytes.Buffer
			if err := browserVerify(context.Background(), r.env, &out); err == nil {
				t.Fatalf("passed: %s", out.String())
			}
			if !strings.Contains(out.String(), "FAIL") {
				t.Fatalf("no FAIL line: %s", out.String())
			}
			if !r.stopped {
				t.Fatal("server not stopped")
			}
		})
	}
}

// With the OpenShell gateway down, verify says so and touches nothing.
func TestBrowserVerifyGatewayDown(t *testing.T) {
	r := verifyEnv(t, "", nil)
	r.env.Health = func(context.Context) error { return errors.New("connection refused") }
	err := browserVerify(context.Background(), r.env, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "OpenShell gateway") {
		t.Fatalf("err = %v, want the gateway named", err)
	}
	if len(r.ensured) != 0 || r.argv != nil {
		t.Fatal("ensured or ran checks with the gateway down")
	}
}

// Without pinned tools there is no gateway endpoint to check.
func TestBrowserVerifyNeedsPins(t *testing.T) {
	r := verifyEnv(t, "", nil)
	if err := os.Remove(toolgateway.DefaultPinsPath(r.env.Home)); err != nil {
		t.Fatal(err)
	}
	err := browserVerify(context.Background(), r.env, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "aimux browser setup") {
		t.Fatalf("err = %v, want setup named", err)
	}
	if r.argv != nil {
		t.Fatal("ran checks without pins")
	}
}

func TestBrowserVerifyRefusesBoundPort(t *testing.T) {
	inUse := &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
	r := verifyEnv(t, "", inUse)
	err := browserVerify(context.Background(), r.env, &bytes.Buffer{})
	// remote serve owns :7390 now: name it and how to run verify around it.
	for _, want := range []string{"aimux remote serve", "stop", "aimux browser verify", "start"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
	if r.argv != nil {
		t.Fatal("ran checks with the port bound")
	}
}

// fakeToolLookPath finds the work-lane tools in /opt/tools/bin and nothing else.
func fakeToolLookPath(name string) (string, error) {
	switch name {
	case "openshell", "openshell-gateway", "podman", "claude":
		return "/opt/tools/bin/" + name, nil
	}
	return "", errors.New(name + ": not in PATH")
}

func TestBrowserInstallPinsToolDirs(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	env.LookPath = func(name string) (string, error) {
		switch name {
		case "openshell":
			return "/Users/o/go/bin/openshell", nil
		case "openshell-gateway", "podman":
			return "/opt/homebrew/bin/" + name, nil
		case "claude":
			return "/Users/o/.local/bin/claude", nil
		}
		return "", errors.New("not found")
	}
	if err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(env.Home, ".aimux", "wingman", "host.sh"))
	want := "export PATH='/opt/node/22.1.0/bin:/Users/o/go/bin:/opt/homebrew/bin:/Users/o/.local/bin':\"$PATH\"\n"
	if !strings.Contains(string(data), want) {
		t.Fatalf("wrapper = %q, want line %q", data, want)
	}
	// Optional tools may be missing: podman, the gateway and claude.
	env = testEnv(t, &fakeRun{})
	env.LookPath = func(name string) (string, error) {
		if name == "openshell" {
			return "/Users/o/go/bin/openshell", nil
		}
		return "", errors.New("not found")
	}
	var out bytes.Buffer
	if err := browserInstall(context.Background(), env, testExtID, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(env.Home, ".aimux", "wingman", "host.sh"))
	if !strings.Contains(string(data), "export PATH='/opt/node/22.1.0/bin:/Users/o/go/bin':") {
		t.Fatalf("wrapper = %q", data)
	}
	if !strings.Contains(out.String(), "podman") {
		t.Fatalf("no warning for missing podman: %s", out.String())
	}
}

func TestBrowserInstallNeedsNodeAndOpenshell(t *testing.T) {
	f := &fakeRun{noNode: true}
	env := testEnv(t, f)
	err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "node") {
		t.Fatalf("missing node: err = %v", err)
	}
	if len(f.find("npm")) != 0 {
		t.Fatal("ran npm without node")
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".aimux", "wingman", "host.sh")); err == nil {
		t.Fatal("wrapper written without node")
	}

	env = testEnv(t, &fakeRun{})
	env.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	err = browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "openshell") {
		t.Fatalf("missing openshell: err = %v", err)
	}

	// Empty execPath output counts as missing.
	env = testEnv(t, &fakeRun{})
	env.Run = func(_ context.Context, _ []string, argv ...string) (string, error) { return "\n", nil }
	if err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{}); err == nil {
		t.Fatal("empty node path accepted")
	}
}

func rawTool(name, propType string) json.RawMessage {
	return json.RawMessage(`{"name":"` + name + `","description":"d","inputSchema":{"type":"object","properties":{"q":{"type":"` + propType + `"}}}}`)
}

func catalogFake(tools map[string][]json.RawMessage, down map[string]error) func(context.Context, mcpproxy.ReadonlyConfig) (*mcpproxy.Catalog, map[string]error) {
	return func(context.Context, mcpproxy.ReadonlyConfig) (*mcpproxy.Catalog, map[string]error) {
		cat := &mcpproxy.Catalog{Servers: map[string]mcpproxy.CatalogEntry{}}
		for s, ts := range tools {
			cat.Set(s, ts, time.Unix(0, 0))
		}
		return cat, down
	}
}

func runSetupForPins(t *testing.T, env browserEnv, out *bytes.Buffer) {
	t.Helper()
	token := "sk-ant-oat01-SECRETSECRET" //nolint:gosec // G101: fake token for the redaction test
	created := false
	f := &fakeRun{answer: setupScript(token, &created)}
	env.Run = f.run
	if err := browserSetup(context.Background(), env, filepath.Join(t.TempDir(), "sandbox"), out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

// Setup pins exactly the tools the allowlist names, and a backend that is
// down is skipped with a warning that names it.
func TestBrowserSetupPinsAllowedToolsAndWarnsOnDownBackend(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	env.Catalog = catalogFake(map[string][]json.RawMessage{
		"wiki":  {rawTool("wiki_search", "string"), rawTool("wiki_read", "string"), rawTool("wiki_write", "string")},
		"slack": {rawTool("search_messages", "string"), rawTool("post_message", "string")},
	}, map[string]error{"jira": errors.New("connection refused")})
	var out bytes.Buffer
	runSetupForPins(t, env, &out)

	store := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(env.Home)}
	pins, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want, err := toolgateway.PinTool(rawTool("wiki_search", "string"))
	if err != nil {
		t.Fatal(err)
	}
	if pins["wiki"]["wiki_search"] != want || pins["wiki"]["wiki_read"] == "" {
		t.Fatalf("wiki pins = %v", pins["wiki"])
	}
	if _, ok := pins["wiki"]["wiki_write"]; ok {
		t.Error("pinned a tool the allowlist does not name")
	}
	if _, ok := pins["slack"]["post_message"]; ok || pins["slack"]["search_messages"] == "" {
		t.Errorf("slack pins = %v", pins["slack"])
	}
	if len(pins["jira"]) != 0 {
		t.Errorf("down backend pinned: %v", pins["jira"])
	}
	fi, err := os.Stat(store.Path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pins file: %v %v", fi, err)
	}
	for _, w := range []string{"wiki/wiki_search", "slack/search_messages", "jira", "connection refused"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
}

// Rerunning setup while a backend is down must not drop its earlier pins.
func TestBrowserSetupKeepsEarlierPinsOfDownBackend(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	store := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(env.Home)}
	if err := store.Save(toolgateway.Pins{"jira": {"search_issues_jql": "sha256:old"}}); err != nil {
		t.Fatal(err)
	}
	env.Catalog = catalogFake(map[string][]json.RawMessage{"wiki": {rawTool("wiki_search", "string")}},
		map[string]error{"jira": errors.New("down")})
	runSetupForPins(t, env, &bytes.Buffer{})
	pins, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if pins["jira"]["search_issues_jql"] != "sha256:old" {
		t.Fatalf("jira pins = %v", pins["jira"])
	}
}

// Rerunning setup re-pins a live backend: a changed tool takes its new
// pin and a tool that left the backend is dropped.
func TestBrowserSetupRepinsLiveBackend(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	store := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(env.Home)}
	if err := store.Save(toolgateway.Pins{"wiki": {"wiki_search": "sha256:old", "wiki_read": "sha256:gone"}}); err != nil {
		t.Fatal(err)
	}
	env.Catalog = catalogFake(map[string][]json.RawMessage{"wiki": {rawTool("wiki_search", "number")}}, nil)
	runSetupForPins(t, env, &bytes.Buffer{})
	pins, _ := store.Load()
	want, _ := toolgateway.PinTool(rawTool("wiki_search", "number"))
	if pins["wiki"]["wiki_search"] != want {
		t.Errorf("wiki_search = %q, want the new pin", pins["wiki"]["wiki_search"])
	}
	if _, ok := pins["wiki"]["wiki_read"]; ok {
		t.Error("a tool the backend no longer lists kept its pin")
	}
}

// The read-only server loads the stored pins and hands them to Build; with
// none stored it says so and passes pins that expose nothing.
func TestBrowserHostReadonlyLoadsPins(t *testing.T) {
	var logBuf syncBuffer
	fk := &hostFakes{served: make(chan mcpproxy.DaemonOptions, 1)}
	env := newHostTestEnv(t, fk, &logBuf)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ro := &readonlyServer{env: env, token: testHostToken}
	stop, err := ro.start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	<-fk.served
	stop()
	if !strings.Contains(logBuf.String(), "no pinned tools: run aimux browser setup") {
		t.Fatalf("log lacks the no-pins hint:\n%s", logBuf.String())
	}
	if fk.pins == nil {
		t.Fatal("Build got nil pins; an empty store must still fail closed through real pins")
	}

	pin := toolgateway.Pins{"wiki": {"wiki_search": "sha256:x"}}
	if err := (toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(env.Home)}).Save(pin); err != nil {
		t.Fatal(err)
	}
	var logBuf2 syncBuffer
	ro.env.Log = log.New(&logBuf2, "", 0)
	fk.pins = nil
	stop, err = ro.start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	<-fk.served
	stop()
	if strings.Contains(logBuf2.String(), "no pinned tools") {
		t.Errorf("hint logged although pins exist:\n%s", logBuf2.String())
	}
	got, ok := fk.pins.(toolgateway.Pins)
	if !ok || got["wiki"]["wiki_search"] != "sha256:x" {
		t.Fatalf("Build got pins %#v", fk.pins)
	}
}

// H6: the host serves only the pinned extension; any other caller gets one
// native-messaging error frame and the host exits without reaching aimux.
func TestHost_RefusesOtherOrigin(t *testing.T) {
	pinned := "chrome-extension://" + testExtID + "/"
	cases := map[string]struct {
		args []string
		pin  bool
	}{
		"other extension": {[]string{"chrome-extension://" + strings.Repeat("b", 32) + "/"}, true},
		"no origin":       {nil, true},
		"no trailing /":   {[]string{strings.TrimSuffix(pinned, "/")}, true},
		"nothing pinned":  {[]string{pinned}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t, &fakeRun{})
			if c.pin {
				pinExtension(t, env.Home, testExtID)
			}
			reached := false
			relay := &browserhost.Relay{Socket: filepath.Join(t.TempDir(), "none.sock"),
				Start: func(context.Context) error { reached = true; return errors.New("unexpected start") },
				Dial:  func(context.Context) (net.Conn, error) { reached = true; return nil, errors.New("unexpected dial") }}
			var out bytes.Buffer
			in := strings.NewReader("")
			err := runBrowserHostWith(context.Background(), env, log.New(io.Discard, "", 0), c.args, in, &out, relayOf(relay))
			if err == nil {
				t.Fatal("host served a caller that is not the pinned extension")
			}
			msg, rerr := browserhost.ReadMessage(&out)
			var e browserhost.Event
			if rerr != nil || json.Unmarshal(msg, &e) != nil || e.Type != "error" || !strings.Contains(e.Error, "extension") {
				t.Fatalf("frame = %q (%v)", msg, rerr)
			}
			if reached {
				t.Fatal("the relay reached aimux")
			}
		})
	}

	// The pinned origin is served: a panel that closes at once is a clean exit.
	env := testEnv(t, &fakeRun{})
	pinExtension(t, env.Home, testExtID)
	relay := &browserhost.Relay{Socket: filepath.Join(t.TempDir(), "none.sock")}
	var out bytes.Buffer
	if err := runBrowserHostWith(context.Background(), env, log.New(io.Discard, "", 0), []string{pinned}, strings.NewReader(""), &out, relayOf(relay)); err != nil {
		t.Fatalf("pinned origin refused: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected output %q", out.String())
	}
}

// pinExtension writes the extension id install pins.
func pinExtension(t *testing.T, home, id string) {
	t.Helper()
	p := filepath.Join(wingmanDir(home), "extension-id")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// install pins the extension id (0600) where the host checks the caller.
func TestBrowserInstallPinsExtensionID(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	if err := browserInstall(context.Background(), env, testExtID, []string{t.TempDir()}, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(extensionIDFile(env.Home))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pin file: %v %v", fi, err)
	}
	if err := checkOrigin(env.Home, []string{"chrome-extension://" + testExtID + "/"}); err != nil {
		t.Fatalf("pinned origin refused after install: %v", err)
	}
}

// An install from before the pin file is read from its Chrome manifest.
func TestPinnedExtensionIDFallsBackToManifest(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	if err := browserInstall(context.Background(), env, testExtID, nil, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(extensionIDFile(env.Home)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if id, err := pinnedExtensionID(env.Home); err != nil || id != testExtID {
		t.Fatalf("id = %q, %v", id, err)
	}
}

// The relay starts `aimux remote serve --local-only` from the running
// binary, in its own session, cwd /, stdin closed and output to the log.
func TestServeCommandIsDetached(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	env.Executable = func() (string, error) { return "/opt/bin/aimux", nil }
	env.LookPath = func(string) (string, error) { return "/usr/local/bin/aimux", nil } // never used
	logf, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logf.Close() }()
	cmd, err := serveCommand(env, logf)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/opt/bin/aimux" || strings.Join(cmd.Args[1:], " ") != "remote serve --local-only" {
		t.Fatalf("command = %s %q", cmd.Path, cmd.Args)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("serve not in its own session")
	}
	// Chrome's pipes must not be inherited: stdin is /dev/null, output the log.
	if cmd.Stdin != nil || cmd.Stdout != logf || cmd.Stderr != logf || len(cmd.ExtraFiles) != 0 || cmd.Dir != "/" {
		t.Fatalf("stdio/dir: in=%v out=%v err=%v dir=%q", cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Dir)
	}

	env.Executable = func() (string, error) { return "", errors.New("no executable") }
	if _, err := serveCommand(env, logf); err == nil {
		t.Fatal("no error without an executable")
	}
}

func TestWingmanSocket(t *testing.T) {
	home := t.TempDir()
	if sock, err := wingmanSocket(home); err != nil || !strings.HasSuffix(sock, filepath.Join(".aimux", "clients", "wingman.sock")) {
		t.Fatalf("default socket = %q, %v", sock, err)
	}
	cfg := filepath.Join(home, ".config", "aimux", "clients.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("clients:\n  wingman:\n    socket: /tmp/w.sock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sock, err := wingmanSocket(home); err != nil || sock != "/tmp/w.sock" {
		t.Fatalf("configured socket = %q, %v", sock, err)
	}
	if err := os.WriteFile(cfg, []byte("clients:\n  wingman:\n    socket: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wingmanSocket(home); err == nil {
		t.Fatal("no error for a wingman client without a socket")
	}
}

// SIGTERM (Chrome closing the panel) ends the host cleanly.
func TestBrowserHostSIGTERMExitsCleanly(t *testing.T) {
	dir, err := os.MkdirTemp("", "wh")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "w.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sessions":[]}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	env := testEnv(t, &fakeRun{})
	pinExtension(t, env.Home, testExtID)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- runBrowserHostWith(context.Background(), env, log.New(io.Discard, "", 0), []string{"chrome-extension://" + testExtID + "/"},
			inR, outW, relayOf(&browserhost.Relay{Socket: sock}))
		_ = outW.Close()
	}()
	b, _ := json.Marshal(browserhost.Request{ID: "h1", Type: "history.list"})
	if err := browserhost.WriteMessage(inW, b); err != nil {
		t.Fatal(err)
	}
	if msg, err := browserhost.ReadMessage(outR); err != nil || !strings.Contains(string(msg), `"id":"h1"`) {
		t.Fatalf("reply %q %v", msg, err) // the host runs, so its signal handler is installed
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("host after SIGTERM = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host ignored SIGTERM")
	}
	_ = inW.Close()
}

// setup writes the clients file with the wingman defaults when absent (0600,
// loads back as the defaults) and never touches an existing one.
func TestBrowserSetupWritesDefaultClientsFile(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	runSetupForPins(t, env, &bytes.Buffer{})
	p := filepath.Join(env.Home, ".config", "aimux", "clients.yaml")
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("clients file: %v %v", fi, err)
	}
	got, err := clientscope.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	def := clientscope.Defaults().Clients["wingman"]
	w := got.Clients["wingman"]
	if strings.Join(w.Profiles, ",") != strings.Join(def.Profiles, ",") || w.MaxSessions != def.MaxSessions || w.TurnsPerMinute != def.TurnsPerMinute || w.Socket != def.Socket {
		t.Fatalf("wingman = %+v, want %+v", w, def)
	}

	if err := os.WriteFile(p, []byte("clients:\n  wingman:\n    max_sessions: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSetupForPins(t, env, &bytes.Buffer{})
	if b, _ := os.ReadFile(p); string(b) != "clients:\n  wingman:\n    max_sessions: 1\n" { //nolint:gosec // G304: path is a test temp or fixed testdata path
		t.Fatalf("existing file changed: %q", b)
	}
}

// relayOf is a relay builder that always returns r.
func relayOf(r *browserhost.Relay) func() (*browserhost.Relay, error) {
	return func() (*browserhost.Relay, error) { return r, nil }
}

// The origin is checked before clients.yaml is read, and a clients.yaml the
// host cannot use answers the pinned panel with a frame naming the reason.
func TestHost_BadClientsFileAnswersWithFrame(t *testing.T) {
	env := testEnv(t, &fakeRun{})
	pinExtension(t, env.Home, testExtID)
	cfg := filepath.Join(env.Home, ".config", "aimux", "clients.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("clients: [not, a, map\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	built := 0
	newRelay := func() (*browserhost.Relay, error) {
		built++
		sock, err := wingmanSocket(env.Home)
		if err != nil {
			return nil, err
		}
		return &browserhost.Relay{Socket: sock}, nil
	}
	frame := func(args []string) browserhost.Event {
		t.Helper()
		var out bytes.Buffer
		if err := runBrowserHostWith(context.Background(), env, log.New(io.Discard, "", 0), args, strings.NewReader(""), &out, newRelay); err == nil {
			t.Fatal("host ran with an unusable clients.yaml")
		}
		msg, err := browserhost.ReadMessage(&out)
		var e browserhost.Event
		if err != nil || json.Unmarshal(msg, &e) != nil || e.Type != "error" {
			t.Fatalf("frame = %q (%v)", msg, err)
		}
		return e
	}
	if e := frame([]string{"chrome-extension://" + testExtID + "/"}); !strings.Contains(e.Error, "clients.yaml") {
		t.Fatalf("pinned panel error = %q, want the clients.yaml reason", e.Error)
	}
	built = 0
	if e := frame([]string{"chrome-extension://" + strings.Repeat("b", 32) + "/"}); !strings.Contains(e.Error, "extension") || built != 0 {
		t.Fatalf("other origin: error %q, relay built %d times (want the origin refusal first)", e.Error, built)
	}
}
