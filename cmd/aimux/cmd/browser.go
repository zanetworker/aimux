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
	"io/fs"
	"log"
	"maps"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/autoroute"
	"github.com/zanetworker/aimux/internal/browserhost"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
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
	// AutoKey checks, by existence only, whether Auto mode has an OpenRouter key.
	AutoKey keyProbe
	// RunnerKey is the platform secret store the tool runner's GitHub token
	// comes from (sandbox setup).
	RunnerKey secretStore
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
		AutoKey:   defaultKeyProbe(home),
		RunnerKey: defaultSecretStore(),
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
		Short: "Wingman browser side panel: Chrome native messaging host and setup (the sandbox is aimux sandbox)",
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
	var acceptPins bool
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Prepare the work lane: the read-only tool config, the sandbox and tool runner images, wingman-claude and (when its token is in the secret store) aimux-github; reports tools to review",
		RunE: func(c *cobra.Command, _ []string) error {
			return browserSetup(c.Context(), env, sandboxDir, acceptPins, c.OutOrStdout())
		},
	}
	setup.Flags().BoolVar(&acceptPins, "accept-pins", false, "Approve every listed tool without review (for automation; prints a warning)")
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
		Short: "Deprecated: use aimux sandbox verify",
		RunE: func(c *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(c.ErrOrStderr(), "Note: aimux browser verify is now aimux sandbox verify; this alias will be removed")
			return sandboxVerify(c.Context(), env, c.OutOrStdout())
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the Chrome native host registration, the pinned extension id and the host wrapper; read-only",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return browserStatus(env, c.OutOrStdout(), c.ErrOrStderr())
		},
	}

	var all bool
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Wingman native host from Chrome and Chrome Beta and its wrapper (--all: the pinned extension id too)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return browserUninstall(env.Home, all, c.OutOrStdout())
		},
	}
	uninstall.Flags().BoolVar(&all, "all", false, "Also remove the pinned extension id")

	cmd.AddCommand(install, setup, host, verify, status, uninstall)
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
func browserSetup(ctx context.Context, env browserEnv, sandboxDir string, acceptPins bool, out io.Writer) error {
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }

	reg, err := setupToolSources(env.Home, say)
	if err != nil {
		return err
	}
	if err := reviewToolsInSetup(ctx, toolsEnv{Home: env.Home, Catalog: env.Catalog, Now: time.Now}, reg, acceptPins, say); err != nil {
		return err
	}
	if err := writeDefaultClients(env.Home, say); err != nil {
		return err
	}

	if _, err := sandboxSetup(ctx, env, sandboxDir, "aimux browser setup", say); err != nil {
		return err
	}
	say("Sandbox image, provider and policies: aimux sandbox status (setup and verify are there too)")
	say("%s", autoKeySetupLine(env))
	say("Done. aimux remote serve creates the work sandbox %s on first use; the old %s sandbox is no longer used (delete it with `openshell -g local sandbox delete %s`).", workSandbox, wingmanOldSandbox, wingmanOldSandbox)
	say("Workspace needs the tool gateway worker: run `aimux mcp proxy gateway install` once (aimux mcp proxy gateway status shows it).")
	return nil
}

// setupToolSources makes sure ~/.config/aimux/tool-sources.yaml exists:
// migrated from readonly.json once (one line per allowlist entry the
// migration dropped), else the defaults (no notes source). An existing
// registry is never rewritten; one that does not load fails setup. It warns
// on each entry the registry skipped and each notes root that is missing,
// and returns the registry.
func setupToolSources(home string, say func(string, ...any)) (toolreg.Registry, error) {
	path, old := toolreg.DefaultPath(home), toolreg.LegacyPath(home)
	m, err := toolreg.MigrateReport(old, path)
	if err != nil {
		return toolreg.Registry{}, err
	}
	if m.Migrated {
		say("Migrated %s to %s (the old file is kept as %s.migrated)", old, path, old)
		for _, d := range m.Dropped {
			say("Migrated tool config to %s; %s", path, d)
		}
	} else if err := toolreg.WriteNew(path, toolreg.Default()); err == nil {
		say("Wrote %s", path)
	} else if !errors.Is(err, fs.ErrExist) {
		return toolreg.Registry{}, err
	}
	reg, err := toolreg.Load(path)
	if err != nil {
		return toolreg.Registry{}, err
	}
	for _, m := range []map[string]error{reg.SourceErrors, reg.SetErrors} {
		for _, n := range slices.Sorted(maps.Keys(m)) {
			say("Warning: %v; it is skipped", m[n])
		}
	}
	cfg := mcpproxy.ReadonlyConfigFrom(reg)
	for _, n := range slices.Sorted(maps.Keys(cfg.Notes)) {
		if st, err := os.Stat(cfg.Notes[n]); err != nil || !st.IsDir() {
			say("Warning: notes root %s (source %s) not found; its tools will be off", cfg.Notes[n], n)
		}
	}
	return reg, nil
}

// reviewToolsInSetup lists the sources' live tools once and reports the
// ones that need review with the commands to review them; it writes no
// pins. With acceptPins it approves every set instead and says so loudly.
// A backend that is down is named; its pins are kept.
func reviewToolsInSetup(ctx context.Context, env toolsEnv, reg toolreg.Registry, acceptPins bool, say func(string, ...any)) error {
	rv, err := reviewFrom(ctx, env, reg, "")
	if err != nil {
		return err
	}
	for _, n := range slices.Sorted(maps.Keys(rv.live)) {
		if e := rv.live[n].Err; e != nil {
			say("Warning: backend %s unavailable (%s); its tools stay hidden until it is up and approved (aimux tools status)", n, clipText(e.Error(), 200))
		}
	}
	if acceptPins {
		res, _, err := approveAndSave(rv, slices.Sorted(maps.Keys(reg.Sets)), env.Now())
		if err != nil {
			return err
		}
		count := map[string]int{}
		for _, a := range res {
			count[a.Action]++
		}
		msg := fmt.Sprintf("Warning: --accept-pins approved %s without review, dropped %s of a tool no longer listed, skipped %d (backend down or tool not listed)",
			plural(count[toolgateway.ActionApproved]+count[toolgateway.ActionUpgraded], "tool", "tools"), plural(count[toolgateway.ActionRemoved], "pin", "pins"), count[toolgateway.ActionSkipped])
		if n := unsetTools(reg, rv.live); n > 0 {
			msg += fmt.Sprintf("; %s in no set (not approved)", plural(n, "listed tool is", "listed tools are"))
		}
		say("%s. %s", msg, toolgateway.Disclaimer)
		return nil
	}
	st := toolgateway.Review(rv.reg, rv.pins, rv.live)
	var need []string
	for _, s := range st {
		if s.NeedsReview() {
			need = append(need, s.Set+"/"+s.Tool)
		}
	}
	if len(need) == 0 {
		return nil
	}
	say("Tools need review (%d): %s", len(need), strings.Join(need, ", "))
	say("  Review:  aimux tools diff")
	say("  Approve: aimux tools approve %s", strings.Join(reviewSets(st), " "))
	say("They stay hidden until approved. %s", toolgateway.Disclaimer)
	return nil
}

// plural is n with one when n is 1, else with many ("1 tool", "2 tools").
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// unsetTools counts the tools live backends list that no set of their
// source names: --accept-pins never approves them.
func unsetTools(reg toolreg.Registry, live map[string]toolgateway.LiveSource) int {
	named := map[string]bool{}
	for _, s := range reg.Sets {
		for _, t := range s.Tools {
			named[s.Source+"/"+t] = true
		}
	}
	n := 0
	for src, l := range live {
		for t := range l.Tools {
			if !named[src+"/"+t] {
				n++
			}
		}
	}
	return n
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
		fmt.Sprintf("    max_sessions: %d\n    turns_per_minute: %d\n", w.MaxSessions, w.TurnsPerMinute) +
		"    # Approve changed tools from the panel (POST /v1/tools/approve). Each approval\n" +
		"    # also needs a step-up from a kind in step_up_kinds, which Wingman has none of yet.\n" +
		fmt.Sprintf("    tools_approve: %v\n", w.ToolsApprove)
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
	srv := &toolgateway.ReadonlyServer{Store: toolgateway.NewStore(r.env.Home), Token: r.token, Log: r.env.Log,
		Listen: r.env.Listen, Build: r.env.Build, Serve: r.env.Serve, Rebuild: r.env.Rebuild}
	return srv.Start(ctx)
}

// autoKeySetupLine reports the mode-router's key, using the owner's
// deciders.yaml when there is one.
func autoKeySetupLine(env browserEnv) string {
	cfg, err := autoroute.LoadDecider(filepath.Join(env.Home, ".config", "aimux", "deciders.yaml"))
	if err != nil {
		return "Auto mode: cannot check the OpenRouter key: " + err.Error()
	}
	return autoKeyLine(cfg.Key, env.AutoKey, runtime.GOOS)
}

// browserHost is one Chrome's native host manifest in browser status.
type browserHost struct {
	Browser    string `json:"browser"`
	Manifest   string `json:"manifest"`
	Registered bool   `json:"registered"`
}

// chromeBrowsers are the Chromes install registers in by default.
var chromeBrowsers = []string{"Chrome", "Chrome Beta"}

func hostManifestPath(home, browser string) string {
	return filepath.Join(home, "Library", "Application Support", "Google", browser, "NativeMessagingHosts", wingmanHostName+".json")
}

// browserHostFiles are what install writes for Chrome to start the host:
// the manifest in Chrome and Chrome Beta, then the wrapper. The adapter
// and the pinned extension id are not among them.
func browserHostFiles(home string) []string {
	var out []string
	for _, b := range chromeBrowsers {
		out = append(out, hostManifestPath(home, b))
	}
	return append(out, filepath.Join(wingmanDir(home), "host.sh"))
}

// browserState is what browser status reports. Usable is whether Chrome
// can start the host: a manifest registered, an id pinned, the wrapper there.
type browserState struct {
	Hosts          []browserHost
	ExtensionID    string
	IDErr          error
	Wrapper        string
	WrapperPresent bool
}

func (b browserState) registered() bool {
	for _, h := range b.Hosts {
		if h.Registered {
			return true
		}
	}
	return false
}

func (b browserState) usable() bool { return b.registered() && b.IDErr == nil && b.WrapperPresent }

// collectBrowser reads, without writing, what install sets up.
func collectBrowser(home string) browserState {
	var b browserState
	for _, name := range chromeBrowsers {
		p := hostManifestPath(home, name)
		_, err := os.Stat(p)
		b.Hosts = append(b.Hosts, browserHost{Browser: name, Manifest: p, Registered: err == nil})
	}
	b.ExtensionID, b.IDErr = pinnedExtensionID(home)
	b.Wrapper = filepath.Join(wingmanDir(home), "host.sh")
	st, err := os.Stat(b.Wrapper)
	b.WrapperPresent = err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o100 != 0
	return b
}

// browserStatus reports, without writing, what install sets up: the host
// manifest in Chrome and Chrome Beta, the pinned extension id and the
// wrapper script. Next names install when the host is not usable.
func browserStatus(env browserEnv, out, errOut io.Writer) error {
	b := collectBrowser(env.Home)
	if jsonOutput {
		if err := writeJSON(out, map[string]any{
			"schema_version": "1",
			"hosts":          b.Hosts,
			"extension_id":   b.ExtensionID,
			"wrapper":        map[string]any{"path": b.Wrapper, "present": b.WrapperPresent},
		}); err != nil {
			return err
		}
	} else {
		for _, h := range b.Hosts {
			s := "registered"
			if !h.Registered {
				s = "not registered"
			}
			_, _ = fmt.Fprintf(out, "%-14s %s  %s\n", h.Browser+":", h.Manifest, s)
		}
		if b.IDErr != nil {
			_, _ = fmt.Fprintf(out, "%-14s not pinned\n", "Extension id:")
		} else {
			_, _ = fmt.Fprintf(out, "%-14s %s\n", "Extension id:", b.ExtensionID)
		}
		s := "present"
		if !b.WrapperPresent {
			s = "missing"
		}
		_, _ = fmt.Fprintf(out, "%-14s %s  %s\n", "Host wrapper:", b.Wrapper, s)
	}
	if !b.usable() {
		_, _ = fmt.Fprintln(errOut, "Next: aimux browser install --extension-id <id> (the id is in chrome://extensions)")
	}
	return nil
}

// browserUninstall removes the host manifests and the wrapper, and with
// all the pinned extension id too. The adapter is left (npm installed it
// and reinstalling it is slow); a file already gone is skipped.
func browserUninstall(home string, all bool, out io.Writer) error {
	paths := browserHostFiles(home)
	if all {
		paths = append(paths, extensionIDFile(home))
	}
	removed := 0
	for _, p := range paths {
		err := os.Remove(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		removed++
		_, _ = fmt.Fprintf(out, "Removed %s\n", p)
	}
	if removed == 0 {
		_, _ = fmt.Fprintln(out, "Nothing to remove: the Wingman native host is not installed.")
	}
	return nil
}
