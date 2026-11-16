package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/httpsource"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolgateway"
	aimuxtools "github.com/zanetworker/aimux/sandbox/aimux-tools"
)

// The tool runner's GitHub provider: created from the Keychain item with
// aimux's trimmed profile (sandbox/aimux-tools/github-profile.yaml), whose
// id is not the built-in "github" so that one is never attached.
const (
	runnerProvider     = "aimux-github"
	runnerProfile      = "aimux-github"
	runnerKeychainItem = "aimux-github"
	runnerTokenEnv     = "GITHUB_TOKEN" //nolint:gosec // G101: the env var name, not a credential
)

// secretStore is the platform's secret store (keyring.System): the
// Keychain on macOS, the Secret Service on Linux, none elsewhere. Probe
// (keyring.Probe) tells a missing item, (false, nil), from a store that
// could not be asked, an error; on macOS it reads no secret. A nil func
// finds nothing.
type secretStore struct {
	GOOS   string
	Probe  func(service string) (bool, error)
	Lookup func(ctx context.Context, service string) (string, error)
}

func defaultSecretStore() secretStore {
	return secretStore{
		GOOS:   runtime.GOOS,
		Probe:  keyring.Probe,
		Lookup: func(ctx context.Context, service string) (string, error) { return keyring.System(service).Lookup(ctx) },
	}
}

// runnerSpecs lists an http-openapi source's operations for status's
// runner compile; a var so tests can make it fail.
var runnerSpecs = httpsource.SourceOperations

// sandboxSchemaVersion is the schema of `aimux sandbox status --json`.
const sandboxSchemaVersion = "1"

// Profile sandbox states in status, as JSON carries them.
const (
	stateCurrent = "current" // the sandbox runs the policy compiled now
	stateStale   = "stale"   // it exists with another (or no recorded) policy; the next turn applies it
	stateMissing = "missing" // serve creates it on first use
	stateUnknown = "unknown" // the gateway is unreachable or sandbox list failed
	stateError   = "error"   // the profile does not compile
)

var stateText = map[string]string{
	stateStale:   "stale (applies on next turn)",
	stateMissing: "missing (created on first use)",
	stateUnknown: "unknown (gateway unreachable)",
}

func newSandboxCmd() *cobra.Command { return newSandboxCmdWith(defaultBrowserEnv()) }

func newSandboxCmdWith(env browserEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "The OpenShell sandboxes profiles and the tool runner run in: status, setup (images and providers) and verify",
		Long: "Profiles that run in a sandbox (research, work-readonly) get one sandbox each, aimux-<profile>, which\n" +
			"aimux remote serve creates on first use and updates when the compiled policy changes. These commands\n" +
			"check and prepare what those sandboxes need; none of them creates a sandbox, except verify, which\n" +
			"ensures aimux-work-readonly as serve does.\n\n" +
			"The tool runner, aimux-tools, is the sandbox cli and http-openapi tools run in.\n" +
			"The tool gateway creates or updates it on first use with the policy compiled from the approved\n" +
			"sources; status shows that policy's state.",
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the sandbox image, provider, OpenShell gateway, and each sandboxed profile's and the tool runner aimux-tools' policy state; read-only",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return sandboxStatus(c.Context(), env, profileDir(), c.OutOrStdout(), c.ErrOrStderr())
		},
	}
	var sandboxDir string
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Build the sandbox and tool runner images and create the providers when missing (aimux-github only when its token is in the secret store); never creates sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			out := c.OutOrStdout()
			say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }
			did, err := sandboxSetup(c.Context(), env, sandboxDir, "aimux sandbox setup", say)
			if err != nil {
				return err
			}
			if !did {
				say("Nothing to do: %s, %s and providers %s, %s are present.", wingmanImage, profile.RunnerImage, wingmanProvider, runnerProvider)
			}
			say("aimux remote serve creates each profile's sandbox on first use (aimux sandbox status shows them).")
			return nil
		},
	}
	setup.Flags().StringVar(&sandboxDir, "sandbox-dir", defaultSandboxDir(env.Home),
		"The wingman repo's sandbox directory (Containerfile, claude-code-oauth.yaml)")
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check the work sandbox's egress: internet blocked, read-only server reachable only with a session token",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return sandboxVerify(c.Context(), env, c.OutOrStdout())
		},
	}
	cmd.AddCommand(status, setup, verify)
	return cmd
}

func defaultSandboxDir(home string) string {
	return filepath.Join(home, "go/src/github.com/zanetworker/wingman/sandbox")
}

// sandboxSetup builds the sandbox image and the tool runner image (on top
// of it), imports the provider profile and creates the provider, then the
// runner's GitHub provider (runnerSetup), each only when missing; it starts
// the OpenShell gateway when it is down (env.Health). It reports whether it
// did anything, or found something the owner must do. rerun is the command
// the Keychain instructions tell the owner to run again.
func sandboxSetup(ctx context.Context, env browserEnv, sandboxDir, rerun string, say func(string, ...any)) (bool, error) {
	if err := env.Health(ctx); err != nil {
		return false, fmt.Errorf("OpenShell gateway: %w", err)
	}
	did := false
	if _, err := env.Run(ctx, nil, "podman", "image", "exists", wingmanImage); err != nil {
		did = true
		say("Building %s from %s", wingmanImage, sandboxDir)
		if _, err := env.Run(ctx, nil, "podman", "build", "-t", wingmanImage, "-f", filepath.Join(sandboxDir, "Containerfile"), sandboxDir); err != nil {
			return did, fmt.Errorf("build the sandbox image: %w", err)
		}
	}
	if _, err := env.Run(ctx, nil, "podman", "image", "exists", profile.RunnerImage); err != nil {
		did = true
		say("Building %s", profile.RunnerImage)
		if err := withFile("Containerfile", aimuxtools.Containerfile, func(path string) error {
			_, err := env.Run(ctx, nil, "podman", "build", "-t", profile.RunnerImage, "-f", path, filepath.Dir(path))
			return err
		}); err != nil {
			return did, fmt.Errorf("build the tool runner image: %w", err)
		}
	}

	profiles, err := env.Run(ctx, nil, openshell("provider", "list-profiles")...)
	if err != nil {
		return did, fmt.Errorf("list provider profiles: %w", err)
	}
	if !hasTableName(profiles, wingmanProfile) {
		did = true
		say("Importing provider profile %s", wingmanProfile)
		if _, err := env.Run(ctx, nil, openshell("provider", "profile", "import", "-f", filepath.Join(sandboxDir, "claude-code-oauth.yaml"), "--global")...); err != nil {
			return did, fmt.Errorf("import provider profile: %w", err)
		}
	}

	providers, err := env.Run(ctx, nil, openshell("provider", "list")...)
	if err != nil {
		return did, fmt.Errorf("list providers: %w", err)
	}
	if !hasTableName(providers, wingmanProvider) {
		did = true
		if err := createWingmanProvider(ctx, env, rerun, say); err != nil {
			return did, err
		}
	}
	if !hasTableName(providers, runnerProvider) {
		did = true
		if err := runnerSetup(ctx, env, profiles, rerun, say); err != nil {
			return did, err
		}
	}
	return did, nil
}

// createWingmanProvider creates wingman-claude from its Keychain item.
func createWingmanProvider(ctx context.Context, env browserEnv, rerun string, say func(string, ...any)) error {
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
		say("then run `%s` again.", rerun)
		return errors.New("missing Keychain item " + wingmanKeychainItem)
	}
	say("Creating provider %s", wingmanProvider)
	if _, err := env.Run(ctx, []string{tokenEnv + "=" + tok}, openshell("provider", "create", "--name", wingmanProvider, "--type", wingmanProfile, "--credential", tokenEnv)...); err != nil {
		// The child's stderr may echo the credential: redact it.
		return fmt.Errorf("create provider %s: %s", wingmanProvider, redact(err.Error(), tok))
	}
	return nil
}

// runnerSetup creates the tool runner's provider aimux-github when the
// platform secret store (env.RunnerKey) has the item aimux-github, after
// linting and importing aimux's trimmed GitHub profile if profiles (the
// list-profiles output) lacks it. The token goes to openshell in the
// environment only. Without the item, or without a secret store on this
// platform, it says how to add the item and succeeds: gh tools are
// optional. It probes first (keyring.Probe), so an absent item is never
// read; a probe or read that fails for any reason but not-found (a timeout,
// an access error, a locked store) fails setup, naming the store and item.
func runnerSetup(ctx context.Context, env browserEnv, profiles, rerun string, say func(string, ...any)) error {
	st := env.RunnerKey
	present := false
	if st.Probe != nil && st.Lookup != nil {
		var err error
		if present, err = st.Probe(runnerKeychainItem); err != nil {
			// keyring errors name the store and an exit status, never the value.
			return fmt.Errorf("check %s item %s: %w", storeName(st.GOOS), runnerKeychainItem, err)
		}
	}
	var tok string
	if present {
		var err error
		tok, err = st.Lookup(ctx, runnerKeychainItem)
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("read %s item %s: %w", storeName(st.GOOS), runnerKeychainItem, err)
		}
		present = err == nil // not found: removed between the probe and the read
	}
	if !present {
		sayAddRunnerToken(st.GOOS, rerun, say)
		return nil
	}
	if !hasTableName(profiles, runnerProfile) {
		say("Importing provider profile %s", runnerProfile)
		if err := withFile("github-profile.yaml", aimuxtools.GitHubProfile, func(path string) error {
			if _, err := env.Run(ctx, nil, openshell("profile", "lint", "-f", path)...); err != nil {
				return fmt.Errorf("lint: %w", err)
			}
			_, err := env.Run(ctx, nil, openshell("profile", "import", "-f", path)...)
			return err
		}); err != nil {
			return fmt.Errorf("import provider profile %s: %w", runnerProfile, err)
		}
	}
	say("Creating provider %s", runnerProvider)
	if _, err := env.Run(ctx, []string{runnerTokenEnv + "=" + tok}, openshell("provider", "create", "--name", runnerProvider, "--type", runnerProfile, "--credential", runnerTokenEnv)...); err != nil {
		// The child's stderr may echo the credential: redact it.
		return fmt.Errorf("create provider %s: %s", runnerProvider, redact(err.Error(), tok))
	}
	return nil
}

// storeName names goos's secret store for messages.
func storeName(goos string) string {
	switch goos {
	case "darwin":
		return "Keychain"
	case "linux":
		return "Secret Service"
	}
	return "secret store"
}

// sayAddRunnerToken says how to store the GitHub token for goos's secret
// store, or that this platform has none.
func sayAddRunnerToken(goos, rerun string, say func(string, ...any)) {
	const kind = "a fine-grained personal access token (public repositories, read-only, no account or organization permissions)"
	switch goos {
	case "darwin":
		say("The GitHub token for the gh tools is not in the Keychain (item %s), so provider %s is not created.", runnerKeychainItem, runnerProvider)
		say("Create %s, then run:", kind)
		say("  security add-generic-password -U -a \"$USER\" -s %s -w", runnerKeychainItem)
	case "linux":
		say("The GitHub token for the gh tools is not in the Secret Service (service %s), so provider %s is not created.", runnerKeychainItem, runnerProvider)
		say("Create %s, then run (secret-tool is in libsecret-tools):", kind)
		say("  secret-tool store --label %s service %s", runnerKeychainItem, runnerKeychainItem)
	default:
		say("This platform has no supported secret store for the GitHub token (item %s), so provider %s is not created.", runnerKeychainItem, runnerProvider)
		return
	}
	say("and run `%s` again.", rerun)
}

// withFile writes data to name in a new private temp directory, runs fn
// with its path and removes the directory.
func withFile(name string, data []byte, fn func(path string) error) error {
	dir, err := os.MkdirTemp("", "aimux-tools-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return fn(path)
}

// component is one thing status checks: its name, a status word and why
// it could not be checked.
type component struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// profileSandbox is one sandboxed profile's sandbox in status. Exists is
// null when the gateway could not be asked.
type profileSandbox struct {
	Profile        string `json:"profile,omitempty"`
	Sandbox        string `json:"sandbox"`
	Exists         *bool  `json:"exists"`
	State          string `json:"state"`
	RecordedDigest string `json:"recorded_digest"`
	CompiledDigest string `json:"compiled_digest"`
	Error          string `json:"error,omitempty"`
}

type sandboxReport struct {
	SchemaVersion string           `json:"schema_version"`
	Image         component        `json:"image"`
	Provider      component        `json:"provider"`
	Gateway       component        `json:"gateway"`
	Profiles      []profileSandbox `json:"profiles"`
	Runner        profileSandbox   `json:"runner"` // aimux-tools; profile is empty
	Next          []string         `json:"next"`
}

// collectSandboxStatus checks everything status shows. It only reads:
// podman image exists, openshell status, provider list and sandbox list,
// the recorded digests and a compile from the current snapshot (which
// never migrates or writes). It never starts the gateway.
func collectSandboxStatus(ctx context.Context, env browserEnv, profileDir string) sandboxReport {
	r := sandboxReport{SchemaVersion: sandboxSchemaVersion, Next: []string{},
		Image: component{Name: wingmanImage}, Provider: component{Name: wingmanProvider}, Gateway: component{Name: "local"}}
	next := func(s string) {
		if !slices.Contains(r.Next, s) {
			r.Next = append(r.Next, s)
		}
	}

	var ec interface{ ExitCode() int }
	switch _, err := env.Run(ctx, nil, "podman", "image", "exists", wingmanImage); {
	case err == nil:
		r.Image.Status = "present"
	case errors.As(err, &ec) && ec.ExitCode() == 1:
		r.Image.Status = "missing"
		next("aimux sandbox setup")
	default:
		r.Image.Status, r.Image.Error = "unknown", clipText(err.Error(), 300)
		next("podman machine start, then aimux sandbox status")
	}

	var sandboxes string
	var listErr error
	var providers *string // the gateway's provider list, when it listed
	gwErr := func() error { _, err := env.Run(ctx, nil, openshell("status")...); return err }()
	if gwErr != nil {
		r.Gateway.Status, r.Gateway.Error = "unreachable", clipText(gwErr.Error(), 300)
		r.Provider.Status = "unknown"
		next("start the OpenShell gateway (openshell-gateway --config ~/.config/openshell/gateway-podman.toml; aimux sandbox setup and remote serve start it too), then aimux sandbox status")
	} else {
		r.Gateway.Status = "reachable"
		out, err := env.Run(ctx, nil, openshell("provider", "list")...)
		if err == nil {
			providers = &out
		}
		switch {
		case err != nil:
			r.Provider.Status, r.Provider.Error = "unknown", clipText(err.Error(), 300)
		case hasTableName(out, wingmanProvider):
			r.Provider.Status = "present"
		default:
			r.Provider.Status = "missing"
			next("aimux sandbox setup")
		}
		out, err = env.Run(ctx, nil, openshell("sandbox", "list")...)
		if err != nil {
			listErr = err
			r.Gateway.Error = "sandbox list failed: " + clipText(err.Error(), 300)
			next("openshell -g local sandbox list (it failed; see the gateway line), then aimux sandbox status")
		}
		sandboxes = out
	}

	// sandboxState is one sandbox's row: compiled is its compiled digest,
	// err why it does not compile, hint the Next line for that error.
	sandboxState := func(name, sandbox, compiled string, err error, hint string) profileSandbox {
		p := profileSandbox{Profile: name, Sandbox: sandbox}
		if b, err := os.ReadFile(filepath.Join(env.Home, ".aimux", "sandboxes", p.Sandbox+".digest")); err == nil { //nolint:gosec // G304: a digest file under the user's own aimux dir, named from a profile name
			p.RecordedDigest = strings.TrimSpace(string(b))
		}
		if gwErr == nil && listErr == nil {
			exists := hasTableName(sandboxes, p.Sandbox)
			p.Exists = &exists
		}
		switch {
		case err != nil:
			p.State, p.Error = stateError, err.Error()
			next(hint)
		case p.Exists == nil:
			p.CompiledDigest, p.State = compiled, stateUnknown
			if listErr != nil {
				p.Error = "sandbox list failed"
			}
		case !*p.Exists:
			p.CompiledDigest, p.State = compiled, stateMissing
		case p.RecordedDigest == compiled:
			p.CompiledDigest, p.State = compiled, stateCurrent
		default:
			p.CompiledDigest, p.State = compiled, stateStale
		}
		return p
	}

	snap, _, snapErr := toolgateway.NewStore(env.Home).Reload()
	user, _ := profile.LoadDir(profileDir) // a bad file is skipped, as everywhere profiles resolve
	names := profile.BuiltinNames()
	for n := range user {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		m, err := profile.Resolve(name, user)
		if err != nil || (m.Runs != "sandbox" && m.Runs != "run") || !m.Available {
			continue // not sandboxed (page runs over acp) or not offered
		}
		c, err := profile.CompileAt(m, snap, profile.GatewayAddr{})
		if err == nil && snapErr != nil {
			err = snapErr
		}
		sandbox := c.Sandbox
		if err != nil {
			sandbox = "aimux-" + name // no compile to name it; profile.Compile's rule
		}
		r.Profiles = append(r.Profiles, sandboxState(name, sandbox, c.PolicyDigest, err, "aimux tools status (a profile does not compile; see its error)"))
	}
	if r.Profiles == nil {
		r.Profiles = []profileSandbox{}
	}
	// Compile as Ensure applies it: sources whose provider the gateway lacks
	// are left out (unknown providers: compile everything).
	var have func(string) bool
	if providers != nil {
		have = func(p string) bool { return hasTableName(*providers, p) }
	}
	rc, _, err := profile.CompileRunnerAvailable(snap, clipack.Builtin, runnerSpecs, have)
	if err == nil && snapErr != nil {
		err = snapErr
	}
	r.Runner = sandboxState("", profile.RunnerSandbox, rc.PolicyDigest, err, "aimux tools status (the tool runner "+profile.RunnerSandbox+" does not compile; see its error)")
	return r
}

// sandboxStatus prints collectSandboxStatus (JSON with --json) and one
// Next: line per action on stderr. It always exits 0: what it reports is
// the answer, not a failure.
func sandboxStatus(ctx context.Context, env browserEnv, profileDir string, out, errOut io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r := collectSandboxStatus(ctx, env, profileDir)
	if jsonOutput {
		if err := writeJSON(out, r); err != nil {
			return err
		}
	} else {
		line := func(label string, c component) {
			s := c.Status
			switch {
			case c.Error != "" && c.Status == "reachable":
				s += ", " + c.Error
			case c.Error != "":
				s += " (" + c.Error + ")"
			}
			_, _ = fmt.Fprintf(out, "%-10s %s  %s\n", label+":", c.Name, s)
		}
		line("Image", r.Image)
		line("Provider", r.Provider)
		line("Gateway", r.Gateway)
		_, _ = fmt.Fprintln(out)
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "PROFILE\tSANDBOX\tPOLICY")
		for _, p := range append(slices.Clone(r.Profiles), r.Runner) {
			name := p.Profile
			if name == "" {
				name = "(tool runner)"
			}
			s := p.State
			if t, ok := stateText[s]; ok {
				s = t
			}
			switch {
			case p.State == stateError:
				s = "error: " + p.Error
			case p.State == stateUnknown && p.Error != "":
				s = "unknown (" + p.Error + ")"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", name, p.Sandbox, s)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	for _, n := range r.Next {
		_, _ = fmt.Fprintf(errOut, "Next: %s\n", n)
	}
	return nil
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

// sandboxVerify checks the sandbox work turns run in: it compiles
// work-readonly against the pins, starts a short-lived read-only server on
// :7390 with an issued session token, ensures aimux-work-readonly through
// the launcher (as serve does), runs the egress checks there with node, and
// prints PASS/FAIL for each. It needs the OpenShell gateway up and :7390
// free; aimux remote serve holds :7390 while running.
func sandboxVerify(ctx context.Context, env browserEnv, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := env.Health(ctx); err != nil {
		return fmt.Errorf("OpenShell gateway: %w; start it, then run aimux sandbox verify again", err)
	}
	m, err := profile.Resolve(workProfile, nil) // a builtin: no user file can shadow it
	if err != nil {
		return err
	}
	// One snapshot for the compile, the server and the token, as in serve.
	store := toolgateway.NewStore(env.Home)
	snap, _, err := store.Reload()
	if err != nil {
		return err
	}
	if err := snap.Err(); err != nil {
		return err
	}
	c, err := profile.CompileAt(m, snap, profile.GatewayAddr{})
	if err != nil {
		return fmt.Errorf("compile %s: %w; run aimux tools status, then aimux tools approve", workProfile, err)
	}
	servers := c.Grant.Servers()
	if len(servers) == 0 {
		return fmt.Errorf("%s has no pinned tools to check; run aimux tools status, then aimux tools approve", workProfile)
	}
	ln, err := env.Listen("tcp", wingmanReadonlyAddr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("%s is in use, most likely by aimux remote serve, which runs the read-only tool server there: stop aimux remote serve, run aimux sandbox verify, then start aimux remote serve again", wingmanReadonlyAddr)
		}
		return fmt.Errorf("listen on %s: %w", wingmanReadonlyAddr, err)
	}
	defer func() { _ = ln.Close() }()
	admin, err := env.NewToken() // required by the server; never sent
	if err != nil {
		return fmt.Errorf("make the read-only server token: %w", err)
	}
	tokens := &toolgateway.Tokens{}
	srv := &toolgateway.ReadonlyServer{Store: store, Token: admin, Tokens: tokens, Log: log.New(out, "  ", 0),
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
	token, err := tokens.Issue(verifySession, c.Grant.Scope(), c.Snapshot)
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
