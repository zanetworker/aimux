package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/browserhost"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

const (
	wingmanHostName     = "com.zanetworker.wingman"
	wingmanImage        = "localhost/wingman-sandbox:2.1.319"
	wingmanOldSandbox   = "wingman-work" // created by earlier builds; no longer used
	wingmanProvider     = "wingman-claude"
	wingmanProfile      = "claude-code-oauth"
	wingmanKeychainItem = "wingman-claude-oauth"
	wingmanAdapterPkg   = "@agentclientprotocol/claude-agent-acp@0.101.0"
	wingmanReadonlyAddr = toolgateway.ReadonlyAddr
	wingmanMCPURL       = toolgateway.ReadonlyURL
	tokenEnv            = "CLAUDE_CODE_OAUTH_TOKEN" //nolint:gosec // G101: the env var name, not a credential
)

// browserEnv holds what install, setup and verify touch outside the process.
type browserEnv struct {
	Home       string
	Executable func() (string, error)
	LookPath   func(file string) (string, error)
	TempRoots  []string // a binary under these is refused by install
	// Run runs argv with env added to the environment and returns stdout.
	Run    func(ctx context.Context, env []string, argv ...string) (string, error)
	Health func(ctx context.Context) error

	// verify: the sandbox exec (stdin fed to the command) and the read-only server.
	Exec     func(ctx context.Context, stdin string, argv ...string) (string, error)
	NewToken func() (string, error)
	Listen   func(network, addr string) (net.Listener, error)
	Build    func(ctx context.Context, cfg mcpproxy.ReadonlyConfig, pins mcpproxy.ToolPins, reject func(server, tool string, err error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error)
	Serve    func(ctx context.Context, ln net.Listener, servers map[string]*server.MCPServer, pool *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error
	// Catalog lists the read-only backends' live tools, for pinning in setup.
	Catalog func(ctx context.Context, cfg mcpproxy.ReadonlyConfig) (*mcpproxy.Catalog, map[string]error)
	// Ensure makes sure a compiled profile's sandbox exists with its policy
	// (verify; the launcher serve uses).
	Ensure func(ctx context.Context, c profile.Compiled) error
}

func defaultBrowserEnv() browserEnv {
	home, _ := os.UserHomeDir()
	var once sync.Once
	return browserEnv{
		Home: home,
		// Symlinks are kept on purpose: a Homebrew link outlives an upgrade,
		// the Cellar path it points at does not.
		Executable: func() (string, error) {
			p, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.Abs(p)
		},
		LookPath:  exec.LookPath,
		TempRoots: defaultTempRoots(),
		Run:       runCommand,
		Exec:      runWithStdin,
		NewToken:  newHostToken,
		Listen:    net.Listen,
		Build:     mcpproxy.BuildReadonly,
		Serve:     mcpproxy.Serve,
		Catalog:   mcpproxy.ReadonlyCatalog,
		Health:    func(ctx context.Context) error { return profilerun.GatewayHealth(ctx, &once) },
		Ensure: func(ctx context.Context, c profile.Compiled) error {
			l := &sandboxlaunch.Launcher{Backend: sandboxlaunch.NewCLIBackend(sandboxlaunch.ExecRunner(), filepath.Join(home, ".aimux", "sandboxes"))}
			return l.Ensure(ctx, c)
		},
	}
}

// runCommand runs argv and returns its stdout. The error names the command
// and the tail of its stderr with every env value redacted (env may hold a
// token the command echoes back); it wraps the *exec.ExitError.
func runCommand(ctx context.Context, env []string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- fixed commands built here
	var secrets []string
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
		for _, kv := range env {
			if _, v, ok := strings.Cut(kv, "="); ok {
				secrets = append(secrets, v)
			}
		}
	}
	return runCaptured(cmd, argv, secrets)
}

// runWithStdin runs argv with stdin and returns its stdout.
func runWithStdin(ctx context.Context, stdin string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- fixed commands built here
	cmd.Stdin = strings.NewReader(stdin)
	return runCaptured(cmd, argv, strings.Fields(stdin))
}

func runCaptured(cmd *exec.Cmd, argv, secrets []string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		name := strings.Join(argv[:min(len(argv), 4)], " ")
		tail := clipText(redact(strings.TrimSpace(stderr.String()), secrets...), 400)
		return stdout.String(), fmt.Errorf("%s: %w: %s", name, err, tail)
	}
	return stdout.String(), nil
}

// redact replaces every non-empty secret in s with [redacted].
func redact(s string, secrets ...string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

// defaultTempRoots are directories whose binaries vanish (temp builds).
func defaultTempRoots() []string {
	roots := []string{filepath.Clean(os.TempDir()), "/tmp", "/private/tmp"}
	if r, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		roots = append(roots, filepath.Clean(r))
	}
	return roots
}

// isTempBinary reports whether p is under a temp root or a go-build dir
// (go run), where it will not survive.
func isTempBinary(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, strings.TrimRight(r, "/")+"/") {
			return true
		}
	}
	for _, el := range strings.Split(p, string(filepath.Separator)) {
		if strings.HasPrefix(el, "go-build") {
			return true
		}
	}
	return false
}

// aimuxPath picks the path the wrapper execs: the PATH entry when it is this
// same binary (it survives upgrades), else the running binary's own path.
// Symlinks are not resolved. A temp binary is refused unless allowTemp.
func aimuxPath(env browserEnv, allowTemp bool) (string, error) {
	exe, err := env.Executable()
	if err != nil {
		return "", fmt.Errorf("find the aimux binary: %w", err)
	}
	if lp, err := env.LookPath("aimux"); err == nil {
		if abs, err := filepath.Abs(lp); err == nil && sameFile(abs, exe) {
			exe = abs
		}
	}
	if !allowTemp && isTempBinary(exe, env.TempRoots) {
		return "", fmt.Errorf("aimux runs from %s, a temporary build that will disappear; install aimux (go install, brew) and rerun from there, or pass --allow-temp-binary", exe)
	}
	return exe, nil
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

func wingmanDir(home string) string { return filepath.Join(home, ".aimux", "wingman") }

func newBrowserCmd() *cobra.Command { return newBrowserCmdWith(defaultBrowserEnv()) }

func newBrowserCmdWith(env browserEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "browser",
		Short: "Wingman browser side panel: native messaging host and sandbox setup",
	}

	var extID string
	var chromeDirs []string
	var allowTemp bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Register the Wingman native messaging host with Chrome and Chrome Beta",
		RunE: func(c *cobra.Command, _ []string) error {
			return browserInstall(c.Context(), env, extID, chromeDirs, allowTemp, c.OutOrStdout())
		},
	}
	install.Flags().BoolVar(&allowTemp, "allow-temp-binary", false, "Allow the wrapper to point at a temporary build (go run, /tmp); for tests")
	install.Flags().StringVar(&extID, "extension-id", "", "Wingman extension id (32 letters a-p), from chrome://extensions")
	install.Flags().StringSliceVar(&chromeDirs, "chrome-dirs", nil, "Chrome user data dirs to register in (default: Chrome and Chrome Beta)")

	var sandboxDir string
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Prepare the work lane: the read-only tool config and pins, the sandbox image and the provider",
		RunE: func(c *cobra.Command, _ []string) error {
			return browserSetup(c.Context(), env, sandboxDir, c.OutOrStdout())
		},
	}
	setup.Flags().StringVar(&sandboxDir, "sandbox-dir", filepath.Join(env.Home, "go/src/github.com/zanetworker/wingman/sandbox"),
		"The wingman repo's sandbox directory (Containerfile, claude-code-oauth.yaml)")

	host := &cobra.Command{
		Use:   "host",
		Short: "Run the native messaging host (Chrome starts this; not for interactive use)",
		// Chrome passes the caller origin (checked against the pinned
		// extension) and, on Windows, --parent-window.
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runBrowserHost(c.Context(), env, args)
		},
	}

	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check the work sandbox's egress: internet blocked, read-only server reachable only with a session token",
		RunE: func(c *cobra.Command, _ []string) error {
			return browserVerify(c.Context(), env, c.OutOrStdout())
		},
	}

	cmd.AddCommand(install, setup, host, verify)
	return cmd
}

var extIDRe = regexp.MustCompile(`^[a-p]{32}$`)

func validExtensionID(id string) error {
	if !extIDRe.MatchString(id) {
		return fmt.Errorf("invalid --extension-id %q: want the 32-letter id (a-p) shown in chrome://extensions", id)
	}
	return nil
}

// browserInstall writes the host wrapper and the Chrome manifests, and
// installs the ACP adapter if it is missing.
func browserInstall(ctx context.Context, env browserEnv, extID string, chromeDirs []string, allowTemp bool, out io.Writer) error {
	if err := validExtensionID(extID); err != nil {
		return err
	}
	exe, err := aimuxPath(env, allowTemp)
	if err != nil {
		return err
	}
	pathDirs, err := toolDirs(ctx, env, out)
	if err != nil {
		return err
	}
	dir := wingmanDir(env.Home)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}

	// The adapter goes first, so Chrome never finds a host that cannot answer.
	adapterDir := filepath.Join(dir, "adapter")
	adapter := filepath.Join(adapterDir, "node_modules", ".bin", "claude-agent-acp")
	if _, err := os.Stat(adapter); err != nil {
		_, _ = fmt.Fprintf(out, "Installing %s into %s\n", wingmanAdapterPkg, adapterDir)
		if _, err := env.Run(ctx, nil, "npm", "install", "--prefix", adapterDir, wingmanAdapterPkg); err != nil {
			return fmt.Errorf("install the ACP adapter: %w", err)
		}
	}
	_, _ = fmt.Fprintf(out, "Adapter:  %s\n", adapter)

	wrapper := filepath.Join(dir, "host.sh")
	if err := writeHostWrapper(wrapper, exe, pathDirs); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Wrapper:  %s\n", wrapper)

	// The host serves only this extension (it checks Chrome's caller origin).
	if err := os.WriteFile(extensionIDFile(env.Home), []byte(extID+"\n"), 0o600); err != nil {
		return fmt.Errorf("pin the extension id: %w", err)
	}
	if err := os.Chmod(extensionIDFile(env.Home), 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", extensionIDFile(env.Home), err)
	}

	manifest, err := json.MarshalIndent(map[string]any{
		"name":            wingmanHostName,
		"description":     "Wingman reading side panel",
		"path":            wrapper,
		"type":            "stdio",
		"allowed_origins": []string{"chrome-extension://" + extID + "/"},
	}, "", "  ")
	if err != nil {
		return err
	}
	if len(chromeDirs) == 0 {
		g := filepath.Join(env.Home, "Library", "Application Support", "Google")
		chromeDirs = []string{filepath.Join(g, "Chrome"), filepath.Join(g, "Chrome Beta")}
	}
	for _, d := range chromeDirs {
		nmh := filepath.Join(d, "NativeMessagingHosts")
		if err := os.MkdirAll(nmh, 0o755); err != nil { //nolint:gosec // G301: Chrome's own NativeMessagingHosts dir, which Chrome must be able to read
			return fmt.Errorf("create %s: %w", nmh, err)
		}
		p := filepath.Join(nmh, wingmanHostName+".json")
		if err := os.WriteFile(p, append(manifest, '\n'), 0o644); err != nil { // #nosec G306 -- Chrome reads it
			return fmt.Errorf("write %s: %w", p, err)
		}
		_, _ = fmt.Fprintf(out, "Manifest: %s\n", p)
	}
	return nil
}

// ensurePrivateDir creates dir 0700 or tightens an existing one to 0700.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: 0700 is intended for this owner-only directory; the search bit is needed
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}

// toolDirs resolves, in the installing user's environment, the directories
// the host needs on PATH: Chrome starts it with only /usr/bin:/bin:/usr/sbin:/sbin.
// node is the real binary (`node -p process.execPath`), not a version-manager
// shim, since the adapter is `#!/usr/bin/env node`. node and openshell are
// required; openshell-gateway, podman and claude are added when found.
func toolDirs(ctx context.Context, env browserEnv, out io.Writer) ([]string, error) {
	nodePath, err := env.Run(ctx, nil, "node", "-p", "process.execPath")
	nodePath = strings.TrimSpace(nodePath)
	if err != nil || nodePath == "" {
		return nil, fmt.Errorf("node not found (the claude-agent-acp adapter needs it): install Node 22 or activate it (mise), then rerun install from that shell: %v", err)
	}
	dirs := []string{filepath.Dir(nodePath)}
	for _, tool := range []string{"openshell", "openshell-gateway", "podman", "claude"} {
		p, err := env.LookPath(tool)
		if err != nil {
			if tool == "openshell" {
				return nil, fmt.Errorf("openshell not found on PATH (the work lane needs it): install it or add its directory to PATH, then rerun install")
			}
			_, _ = fmt.Fprintf(out, "Warning: %s not found on PATH; the host will not see it\n", tool)
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		dirs = append(dirs, filepath.Dir(p))
	}
	var uniq []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			uniq = append(uniq, d)
		}
	}
	return uniq, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// writeHostWrapper writes a 0700 sh script that puts pathDirs in front of
// PATH and execs exe with "browser host" and Chrome's arguments; a manifest
// path cannot carry arguments itself.
func writeHostWrapper(path, exe string, pathDirs []string) error {
	body := "#!/bin/sh\n"
	if len(pathDirs) > 0 {
		body += "export PATH=" + shellQuote(strings.Join(pathDirs, ":")) + ":\"$PATH\"\n"
	}
	body += "export AIMUX_WINGMAN_PATH_PINNED=1\n"
	body += "exec " + shellQuote(exe) + " browser host \"$@\"\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // G306: the native-messaging launcher script must be executable, owner-only
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // G302: the launcher script must stay executable, owner-only
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func openshell(args ...string) []string {
	return append([]string{"openshell", "-g", "local"}, args...)
}

// browserSetup prepares what the work lane needs; each step is skipped when
// already done. The work sandbox itself (aimux-work-readonly) is created by
// aimux remote serve on first use, from the compiled work-readonly profile.
func browserSetup(ctx context.Context, env browserEnv, sandboxDir string, out io.Writer) error {
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }

	cfgPath := filepath.Join(wingmanDir(env.Home), "readonly.json")
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		data, err := json.MarshalIndent(mcpproxy.DefaultReadonlyConfig(env.Home), "", "  ")
		if err != nil {
			return err
		}
		if err := ensurePrivateDir(filepath.Dir(cfgPath)); err != nil {
			return err
		}
		if err := os.WriteFile(cfgPath, append(data, '\n'), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", cfgPath, err)
		}
		say("Wrote %s", cfgPath)
	} else if err != nil {
		return fmt.Errorf("check %s: %w", cfgPath, err)
	}
	cfg, err := mcpproxy.LoadReadonlyConfig(cfgPath)
	if err != nil {
		return err
	}
	if st, err := os.Stat(cfg.WikiRoot); err != nil || !st.IsDir() {
		say("Warning: wiki root %s not found; the wiki tools will be off", cfg.WikiRoot)
	}
	if err := pinAllowedTools(ctx, env, cfg, say); err != nil {
		return err
	}
	if err := writeDefaultClients(env.Home, say); err != nil {
		return err
	}

	if err := env.Health(ctx); err != nil {
		return fmt.Errorf("OpenShell gateway: %w", err)
	}

	if _, err := env.Run(ctx, nil, "podman", "image", "exists", wingmanImage); err != nil {
		say("Building %s from %s", wingmanImage, sandboxDir)
		if _, err := env.Run(ctx, nil, "podman", "build", "-t", wingmanImage, "-f", filepath.Join(sandboxDir, "Containerfile"), sandboxDir); err != nil {
			return fmt.Errorf("build the sandbox image: %w", err)
		}
	}

	profiles, err := env.Run(ctx, nil, openshell("provider", "list-profiles")...)
	if err != nil {
		return fmt.Errorf("list provider profiles: %w", err)
	}
	if !hasTableName(profiles, wingmanProfile) {
		say("Importing provider profile %s", wingmanProfile)
		if _, err := env.Run(ctx, nil, openshell("provider", "profile", "import", "-f", filepath.Join(sandboxDir, "claude-code-oauth.yaml"), "--global")...); err != nil {
			return fmt.Errorf("import provider profile: %w", err)
		}
	}

	providers, err := env.Run(ctx, nil, openshell("provider", "list")...)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	if !hasTableName(providers, wingmanProvider) {
		tok, err := env.Run(ctx, nil, "security", "find-generic-password", "-s", wingmanKeychainItem, "-w")
		tok = strings.TrimSpace(tok)
		var ec interface{ ExitCode() int }
		if err != nil && (!errors.As(err, &ec) || ec.ExitCode() != securityItemNotFound) {
			return fmt.Errorf("read Keychain item %s: %s", wingmanKeychainItem, redact(err.Error(), tok))
		}
		if err != nil || tok == "" {
			say("The Claude token for Wingman is not in the Keychain (item %s). Run:", wingmanKeychainItem)
			say("  claude setup-token")
			say("  security add-generic-password -U -a \"$USER\" -s %s -w", wingmanKeychainItem)
			say("then run `aimux browser setup` again.")
			return errors.New("missing Keychain item " + wingmanKeychainItem)
		}
		say("Creating provider %s", wingmanProvider)
		if _, err := env.Run(ctx, []string{tokenEnv + "=" + tok}, openshell("provider", "create", "--name", wingmanProvider, "--type", wingmanProfile, "--credential", tokenEnv)...); err != nil {
			// The child's stderr may echo the credential: redact it.
			return fmt.Errorf("create provider %s: %s", wingmanProvider, redact(err.Error(), tok))
		}
	}

	say("Done. aimux remote serve creates the work sandbox %s on first use; the old %s sandbox is no longer used (delete it with `openshell -g local sandbox delete %s`).", workSandbox, wingmanOldSandbox, wingmanOldSandbox)
	return nil
}

// pinAllowedTools lists the read-only backends' live tools once and pins
// the ones cfg.Allow names: the hand-reviewed allowlist is trusted on first
// use. A backend that is down keeps whatever pins it had and gets no new
// ones, so its tools stay hidden until setup runs again.
func pinAllowedTools(ctx context.Context, env browserEnv, cfg mcpproxy.ReadonlyConfig, say func(string, ...any)) error {
	store := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(env.Home)}
	pins, err := store.Load()
	if err != nil {
		return err
	}
	cat, down := env.Catalog(ctx, cfg)
	downNames := make([]string, 0, len(down))
	for n := range down {
		downNames = append(downNames, n)
	}
	sort.Strings(downNames)
	for _, n := range downNames {
		say("Warning: backend %s unavailable (%s); its tools are not pinned and stay hidden until you rerun `aimux browser setup`", n, clipText(down[n].Error(), 200))
	}
	allowed := cat.Allowed(cfg.Allow)
	servers := make([]string, 0, len(cat.Servers))
	for n := range cat.Servers {
		servers = append(servers, n)
	}
	sort.Strings(servers)
	for _, srv := range servers {
		fresh := map[string]string{}
		for _, raw := range allowed.Servers[srv].Tools {
			tool, err := mcpproxy.DecodeTool(raw)
			if err != nil {
				continue
			}
			pin, err := toolgateway.PinTool(raw)
			if err != nil {
				continue
			}
			fresh[tool.Name] = pin
			say("Pinned %s/%s %s", srv, tool.Name, pin)
		}
		if len(fresh) == 0 {
			delete(pins, srv)
			continue
		}
		pins[srv] = fresh
	}
	return store.Save(pins)
}

// writeDefaultClients writes ~/.config/aimux/clients.yaml with the wingman
// client's built-in profiles and limits when the file is absent (0600); an
// existing file is the owner's and is left alone. Fields it leaves out
// (the socket) and other clients keep their defaults.
func writeDefaultClients(home string, say func(string, ...any)) error {
	p := filepath.Join(home, ".config", "aimux", "clients.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	w := clientscope.Defaults().Clients["wingman"]
	body := "# Which profiles each local client may ask for, and its limits.\n" +
		"# Clients and fields left out keep aimux's defaults.\n" +
		"clients:\n  wingman:\n" +
		"    profiles: [" + strings.Join(w.Profiles, ", ") + "]\n" +
		fmt.Sprintf("    max_sessions: %d\n    turns_per_minute: %d\n", w.MaxSessions, w.TurnsPerMinute)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: p is clients.yaml under the user's own aimux config dir
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", p, err)
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close() // the write error is the one to report
		return fmt.Errorf("write %s: %w", p, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	say("Wrote %s", p)
	return nil
}

// securityItemNotFound is errSecItemNotFound as security(1)'s exit code.
const securityItemNotFound = 44

var ansiEsc = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// tableRows yields the fields of each row of an openshell table, header and
// ANSI colors removed.
func tableRows(out string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(ansiEsc.ReplaceAllString(out, ""), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == "NAME" {
			continue
		}
		rows = append(rows, f)
	}
	return rows
}

func hasTableName(out, name string) bool {
	for _, f := range tableRows(out) {
		if f[0] == name {
			return true
		}
	}
	return false
}

// hostEnv holds what verify's read-only server is built from.
type hostEnv struct {
	Home   string
	Log    *log.Logger
	Listen func(network, addr string) (net.Listener, error)
	Build  func(ctx context.Context, cfg mcpproxy.ReadonlyConfig, pins mcpproxy.ToolPins, reject func(server, tool string, err error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error)
	Serve  func(ctx context.Context, ln net.Listener, servers map[string]*server.MCPServer, pool *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error
	// Rebuild retries backends that were down (mcpproxy.BuildReadonlyBackends).
	Rebuild func(ctx context.Context, cfg mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, pool *mcpproxy.Pool, reject func(server, tool string, err error)) (map[string]*server.MCPServer, map[string]error)
}

func newHostToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// openHostLog opens ~/Library/Logs/aimux/wingman/host.log for append (dir
// 0700, file 0600).
func openHostLog(home string) (*os.File, error) {
	dir := filepath.Join(home, "Library", "Logs", "aimux", "wingman")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: 0700 is intended for this owner-only directory; the search bit is needed
		return nil, fmt.Errorf("chmod %s: %w", dir, err)
	}
	p := filepath.Join(dir, "host.log")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: host.log under the user's own aimux dir
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close() // the chmod error is the one to report
		return nil, fmt.Errorf("chmod %s: %w", p, err)
	}
	return f, nil
}

// runBrowserHost is what Chrome starts per panel: a relay to aimux's v1 API
// on the wingman socket, which starts `aimux remote serve --local-only` when
// nothing answers there. Chrome terminates it when the panel closes.
func runBrowserHost(ctx context.Context, env browserEnv, args []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	logf, err := openHostLog(env.Home)
	if err != nil {
		return err
	}
	defer func() { _ = logf.Close() }()
	logger := log.New(logf, "", log.LstdFlags)
	newRelay := func() (*browserhost.Relay, error) {
		sock, err := wingmanSocket(env.Home)
		if err != nil {
			return nil, err
		}
		return &browserhost.Relay{Socket: sock, Start: func(context.Context) error { return startServe(env, logf, logger) }}, nil
	}
	return runBrowserHostWith(ctx, env, logger, args, os.Stdin, os.Stdout, newRelay)
}

// wingmanSocket is the wingman client's socket from home's clients.yaml.
func wingmanSocket(home string) (string, error) {
	scopes, err := clientscope.Load(filepath.Join(home, ".config", "aimux", "clients.yaml"))
	if err != nil {
		return "", err
	}
	sock := scopes.Clients["wingman"].Socket
	if sock == "" {
		return "", errors.New("the wingman client has no socket in ~/.config/aimux/clients.yaml")
	}
	return sock, nil
}

// serveCommand is `aimux remote serve --local-only` as a daemon, run from
// the binary of this host: its own session (no signal from Chrome or a
// terminal reaches it), cwd /, stdin /dev/null and its output in logf, so
// it never holds Chrome's pipes.
func serveCommand(env browserEnv, logf *os.File) (*exec.Cmd, error) {
	exe, err := env.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the aimux binary: %w", err)
	}
	cmd := exec.Command(exe, "remote", "serve", "--local-only") // #nosec G204 -- our own binary
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = logf, logf
	return cmd, nil
}

// startServe starts serveCommand and reaps it in the background.
func startServe(env browserEnv, logf *os.File, logger *log.Logger) error {
	cmd, err := serveCommand(env, logf)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s remote serve: %w", cmd.Path, err)
	}
	logger.Printf("host: started aimux remote serve --local-only (pid %d)", cmd.Process.Pid)
	go func() { _ = cmd.Wait() }() // its exit shows in the log; the relay times out on the socket
	return nil
}

// runBrowserHostWith serves one panel: only when Chrome's caller origin
// (args[0]) is the pinned extension and newRelay can build the relay (it
// reads clients.yaml, so only after the origin check); else it answers one
// error frame with the reason and returns an error. It relays until Chrome closes the port or SIGTERM
// arrives; both are a clean exit.
func runBrowserHostWith(ctx context.Context, env browserEnv, logger *log.Logger, args []string, in io.Reader, out io.Writer, newRelay func() (*browserhost.Relay, error)) error {
	refuse := func(err error) error {
		logger.Printf("host: refused: %v", err)
		msg, _ := json.Marshal(browserhost.Event{Type: "error", Error: err.Error()})
		_ = browserhost.WriteMessage(out, msg) // the host exits either way
		return err
	}
	if err := checkOrigin(env.Home, args); err != nil {
		return refuse(err)
	}
	relay, err := newRelay()
	if err != nil {
		return refuse(err)
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := relay.Serve(ctx, in, out); err != nil && ctx.Err() == nil {
		logger.Printf("host: %v", err)
		return err
	}
	return nil
}

// extensionIDFile is where install pins the Wingman extension id.
func extensionIDFile(home string) string { return filepath.Join(wingmanDir(home), "extension-id") }

// checkOrigin accepts only args[0] == chrome-extension://<pinned id>/.
func checkOrigin(home string, args []string) error {
	id, err := pinnedExtensionID(home)
	if err != nil {
		return fmt.Errorf("no Wingman extension is pinned (%v); run aimux browser install --extension-id <id>", err)
	}
	want := "chrome-extension://" + id + "/"
	if len(args) == 0 || args[0] != want {
		got := ""
		if len(args) > 0 {
			got = args[0]
		}
		return fmt.Errorf("caller %q is not the pinned Wingman extension", got)
	}
	return nil
}

// pinnedExtensionID is the id install wrote; an install from before the pin
// file is read from the Chrome manifests it wrote (Chrome, Chrome Beta).
func pinnedExtensionID(home string) (string, error) {
	if b, err := os.ReadFile(extensionIDFile(home)); err == nil {
		id := strings.TrimSpace(string(b))
		if err := validExtensionID(id); err != nil {
			return "", err
		}
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	g := filepath.Join(home, "Library", "Application Support", "Google")
	for _, d := range []string{filepath.Join(g, "Chrome"), filepath.Join(g, "Chrome Beta")} {
		b, err := os.ReadFile(filepath.Join(d, "NativeMessagingHosts", wingmanHostName+".json")) //nolint:gosec // G304: reads our own host manifest under the Chrome NativeMessagingHosts dir
		if err != nil {
			continue
		}
		var m struct {
			Origins []string `json:"allowed_origins"`
		}
		if json.Unmarshal(b, &m) != nil || len(m.Origins) != 1 {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(m.Origins[0], "chrome-extension://"), "/")
		if validExtensionID(id) == nil {
			return id, nil
		}
	}
	return "", errors.New("no extension-id file and no Wingman manifest")
}

// readonlyServer is verify's read-only server: the tool gateway's
// ReadonlyServer (toolgateway) built from env, with a fresh admin token.
type readonlyServer struct {
	env   hostEnv
	token string
}

func (r *readonlyServer) start(ctx context.Context) (func(), error) {
	srv := &toolgateway.ReadonlyServer{Home: r.env.Home, Token: r.token, Log: r.env.Log,
		Listen: r.env.Listen, Build: r.env.Build, Serve: r.env.Serve, Rebuild: r.env.Rebuild}
	return srv.Start(ctx)
}

// workProfile is the profile work turns run under; workSandbox is its
// compiled sandbox (profile.Compile names it aimux-<profile>).
const (
	workProfile = "work-readonly"
	workSandbox = "aimux-" + workProfile
)

// verifyScript is the node program run inside the work sandbox, whose
// compiled policy allows /usr/bin/node and claude only (no curl). Stdin
// line 1 is the session token, so it is on no command line; each check
// prints "<name> <HTTP status>" (000: no connection or timeout). mcpURL is
// the read-only gateway endpoint of a pinned server.
func verifyScript(mcpURL string) string {
	u, _ := json.Marshal(mcpURL)
	return `const t = require("fs").readFileSync(0, "utf8").split("\n")[0];
const u = ` + string(u) + `;
const init = JSON.stringify({jsonrpc: "2.0", id: 1, method: "initialize", params: {protocolVersion: "2025-06-18", capabilities: {}, clientInfo: {name: "aimux-verify", version: "1"}}});
const h = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"};
async function code(url, opts) {
  try { const r = await fetch(url, {...opts, signal: AbortSignal.timeout(10000)}); return String(r.status); }
  catch (e) { return "000"; }
}
(async () => {
  console.log("internet " + await code("https://example.com"));
  console.log("auth " + await code(u, {method: "POST", headers: {...h, Authorization: "Bearer " + t}, body: init}));
  console.log("noauth " + await code(u, {method: "POST", headers: h, body: init}));
  console.log("hostport " + await code("http://127.0.0.1:7381/"));
  console.log("hostgw " + await code("http://host.containers.internal:7381/"));
})();
`
}

// browserVerify checks the sandbox work turns run in: it compiles
// work-readonly against the pins, starts a short-lived read-only server on
// :7390 with an issued session token, ensures aimux-work-readonly through
// the launcher (as serve does), runs the egress checks there with node, and
// prints PASS/FAIL for each. It needs the OpenShell gateway up and :7390
// free; aimux remote serve holds :7390 while running.
func browserVerify(ctx context.Context, env browserEnv, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := env.Health(ctx); err != nil {
		return fmt.Errorf("OpenShell gateway: %w; start it, then run aimux browser verify again", err)
	}
	pins, err := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(env.Home)}.Load()
	if err != nil {
		return err
	}
	m, err := profile.Resolve(workProfile, nil) // a builtin: no user file can shadow it
	if err != nil {
		return err
	}
	c, err := profile.Compile(m, profile.PinnedSets(pins), profile.GatewayAddr{})
	if err != nil {
		return fmt.Errorf("compile %s: %w; run aimux browser setup to pin its tools", workProfile, err)
	}
	servers := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		servers = append(servers, n)
	}
	sort.Strings(servers)
	if len(servers) == 0 {
		return fmt.Errorf("%s has no pinned tools to check; run aimux browser setup", workProfile)
	}
	ln, err := env.Listen("tcp", wingmanReadonlyAddr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("%s is in use, most likely by aimux remote serve, which runs the read-only tool server there: stop aimux remote serve, run aimux browser verify, then start aimux remote serve again", wingmanReadonlyAddr)
		}
		return fmt.Errorf("listen on %s: %w", wingmanReadonlyAddr, err)
	}
	defer func() { _ = ln.Close() }()
	admin, err := env.NewToken() // required by the server; never sent
	if err != nil {
		return fmt.Errorf("make the read-only server token: %w", err)
	}
	tokens := &toolgateway.Tokens{}
	srv := &toolgateway.ReadonlyServer{Home: env.Home, Token: admin, Tokens: tokens, Log: log.New(out, "  ", 0),
		Listen: func(string, string) (net.Listener, error) { return ln, nil }, Build: env.Build, Serve: env.Serve}
	stop, err := srv.Start(ctx)
	if err != nil {
		return fmt.Errorf("start the read-only server: %w", err)
	}
	defer stop()

	_, _ = fmt.Fprintf(out, "Ensuring sandbox %s (created on first use; can take a few minutes)\n", c.Sandbox)
	if err := env.Ensure(ctx, c); err != nil {
		return fmt.Errorf("sandbox %s: %w", c.Sandbox, err)
	}
	const verifySession = "aimux-browser-verify"
	token, err := tokens.Issue(verifySession, c.Servers)
	if err != nil {
		return fmt.Errorf("issue the test token: %w", err)
	}
	defer tokens.Revoke(verifySession)

	mcpURL := wingmanMCPURL + "/mcp/" + servers[0]
	argv := openshell("sandbox", "exec", "--no-login-shell", "--no-tty", c.Sandbox, "--",
		"/usr/bin/env", "NODE_USE_ENV_PROXY=1", "/usr/bin/node", "-e", verifyScript(mcpURL))
	res, err := env.Exec(ctx, token+"\n", argv...)
	if err != nil {
		return fmt.Errorf("run the checks in sandbox %s: %s", c.Sandbox, redact(err.Error(), token, admin))
	}
	path := "/mcp/" + servers[0]
	got := map[string]string{}
	for _, f := range tableRows(res) {
		if len(f) == 2 {
			got[f[0]] = f[1]
		}
	}
	blocked := func(c string) bool { return c == "000" || c == "403" }
	checks := []struct {
		key, label string
		ok         func(string) bool
		want       string
	}{
		{"internet", "internet blocked (https://example.com)", blocked, "000 or 403"},
		{"auth", "read-only server accepts a session token (:7390" + path + ")", func(c string) bool { return c == "200" }, "200"},
		{"noauth", "read-only server rejects a missing token (:7390" + path + ")", func(c string) bool { return c == "401" }, "401"},
		{"hostport", "host Slack proxy blocked (127.0.0.1:7381)", blocked, "000 or 403"},
		{"hostgw", "host Slack proxy blocked through the host gateway (host.containers.internal:7381)", blocked, "000 or 403"},
	}
	failed := 0
	for _, c := range checks {
		code, found := got[c.key]
		if found && c.ok(code) {
			_, _ = fmt.Fprintf(out, "PASS: %s, HTTP %s\n", c.label, code)
			continue
		}
		failed++
		if !found {
			code = "no result"
		}
		_, _ = fmt.Fprintf(out, "FAIL: %s, got %s, want %s\n", c.label, code, c.want)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d egress checks failed", failed, len(checks))
	}
	return nil
}
