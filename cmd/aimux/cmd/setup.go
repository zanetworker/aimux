package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zanetworker/aimux/internal/autoroute"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/parts"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
	"os/signal"
	"syscall"
)

// setupSchemaVersion is the schema of `aimux doctor --json`, and of the other commands that share its checks.
const setupSchemaVersion = "1"

// setupJSONVersion is the schema of `aimux setup --json`: 2 added the parts list.
const setupJSONVersion = "2"

// Check and summary statuses, as setup and doctor report them.
const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
)

var checkSymbol = map[string]string{checkOK: "✓", checkWarn: "!", checkFail: "✗"}

// Step actions in `aimux setup --json`.
const (
	stepRan      = "ran"
	stepPending  = "pending"  // not run: no terminal to ask on and no --yes
	stepDeclined = "declined" // the owner answered no
	stepFailed   = "failed"
)

// frontEnv holds what setup and doctor touch outside the process: the
// machine (browserEnv), the profile dir, the clock and the terminal.
type frontEnv struct {
	browserEnv
	GOOS       string
	Now        func() time.Time
	ProfileDir string
	// IsTTY is whether setup may ask: stdin and stdout are terminals.
	IsTTY func() bool
	// In is where answers are read from.
	In func() io.Reader
	// Gateway reports the tool gateway worker (nil: its control socket
	// under Home).
	Gateway gatewayStatuser
}

func defaultFrontEnv() frontEnv {
	return frontEnv{browserEnv: defaultBrowserEnv(), GOOS: runtime.GOOS, Now: time.Now, ProfileDir: profileDir(),
		IsTTY: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) // #nosec G115 -- small fds
		},
		In: func() io.Reader { return os.Stdin },
	}
}

// toolsEnv lists cli sources through a read-only probe of aimux-tools
// (doctor and setup's review never create or update it).
func (e frontEnv) toolsEnv() toolsEnv {
	env := toolsEnv{Home: e.Home, Catalog: e.Catalog, Now: e.Now}
	if e.Run != nil {
		env.Runner = runnerBinding(runnerOver(e.Run), filepath.Join(e.Home, ".aimux", "sandboxes"))
	}
	return env
}

// runnerOver adapts browserEnv.Run to sandboxlaunch.Runner: stdout whole,
// the error from Close.
func runnerOver(run func(ctx context.Context, env []string, argv ...string) (string, error)) sandboxlaunch.Runner {
	return func(ctx context.Context, _ io.Reader, argv ...string) (io.ReadCloser, error) {
		out, err := run(ctx, nil, argv...)
		return closeErr{Reader: strings.NewReader(out), err: err}, nil
	}
}

type closeErr struct {
	io.Reader
	err error
}

func (c closeErr) Close() error { return c.err }

func newSetupCmd() *cobra.Command { return newSetupCmdWith(defaultFrontEnv()) }

func newSetupCmdWith(env frontEnv) *cobra.Command {
	var o setupOpts
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Check every concern and run only the missing setup steps; on a terminal, offer to review changed tools",
		Long: "On a terminal setup first lists the parts aimux is made of, on or off, and asks which to turn on, by\n" +
			"number: each one chosen is stated in ~/.aimux/config.yaml and its service made to match, as aimux enable\n" +
			"does. Enter turns nothing on. Nothing is asked without a terminal or with --json, which lists the parts.\n" +
			"With sandboxes turned off in that file, the rest is not run.\n\n" +
			"setup checks the sandbox, tools, router, browser and OpenShell gateway, then runs only the steps that\n" +
			"are missing: the tool registry (migrating readonly.json once), clients.yaml, the sandbox image and\n" +
			"provider, and the Chrome native host (with --extension-id, or when an id is pinned but the host is\n" +
			"not registered). On a terminal it asks before each step; --yes runs them without asking; with no\n" +
			"terminal (or --json) and no --yes it runs none and lists them as pending.\n" +
			"Tools that need review are offered one by one, only on a terminal and never with --json: a tool is\n" +
			"approved only after you answer yes to its diff and yes to approving it. --yes never approves tools.\n" +
			"Ends with one line per concern (sandbox, tools, router, browser, gateway, worker: the tool gateway worker) and a Next: line per problem.\n" +
			"Exit 0 unless a step failed or a part chosen from the list could not be turned on.\n\n" +
			"With --check or --apply it does something else: it compares the background services with the parts\n" +
			"section of ~/.aimux/config.yaml. --check prints what would change and exits 1 when something would;\n" +
			"--apply makes the services match, asking before a restart.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if o.check && o.apply {
				return errors.New("--check and --apply are two different things to ask for; give one")
			}
			if o.check || o.apply {
				c.SilenceUsage = true
				// An interrupt ends a wait; a service that did not come up still gets its job file back first.
				ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				cfg, _ := config.Load(config.DefaultPath())
				opts := setupPartsOpts{apply: o.apply, yes: o.yes, asJSON: jsonOutput}
				if !jsonOutput && env.IsTTY != nil && env.IsTTY() {
					opts.ask = askOn(env.In(), c.OutOrStdout())
				}
				return runSetupParts(ctx, filepath.Join(env.Home, ".aimux", "config.yaml"), partServices(env.Home, c.Root().Version, cfg), opts, c.OutOrStdout())
			}
			o.partServices = func() []controller.PartService {
				cfg, _ := config.Load(config.DefaultPath())
				return partServices(env.Home, c.Root().Version, cfg)
			}
			return runSetup(c.Context(), env, o, c.OutOrStdout(), c.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&o.yes, "yes", false, "Run the missing setup steps without asking (never approves tools); with --apply, restart services without asking")
	cmd.Flags().BoolVar(&o.check, "check", false, "Compare the background services with the parts section of ~/.aimux/config.yaml and say what --apply would change; change nothing. Exit 1 when something would change")
	cmd.Flags().BoolVar(&o.apply, "apply", false, "Make the background services match the parts section of ~/.aimux/config.yaml: only the arguments that come from the settings change in a job file, and a service that does not answer afterwards gets its old job file back")
	cmd.Flags().StringVar(&o.extID, "extension-id", "", "Wingman extension id (32 letters a-p, from chrome://extensions): register the Chrome native host for it")
	cmd.Flags().StringVar(&o.sandboxDir, "sandbox-dir", defaultSandboxDir(env.Home),
		"The wingman repo's sandbox directory (Containerfile, claude-code-oauth.yaml)")
	return cmd
}

type setupOpts struct {
	yes        bool
	check      bool
	apply      bool
	extID      string
	sandboxDir string
	// partServices are the services a part chosen on the menu is made to match with.
	partServices func() []controller.PartService
}

// setupStep is one missing step: what it does and how to run it.
type setupStep struct {
	Name   string   `json:"name"`
	Action string   `json:"action"`
	Output []string `json:"output"`
	desc   string
	run    func(ctx context.Context, say func(string, ...any)) error
}

// summaryLine is one concern in setup's summary.
type summaryLine struct {
	Concern string   `json:"concern"`
	Status  string   `json:"status"`
	Detail  string   `json:"detail"`
	Next    []string `json:"next"`
}

func runSetup(ctx context.Context, env frontEnv, o setupOpts, out, errOut io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if o.extID != "" {
		if err := validExtensionID(o.extID); err != nil {
			return err
		}
	}
	interactive := !jsonOutput && env.IsTTY != nil && env.IsTTY()
	var in *bufio.Reader
	if interactive {
		in = bufio.NewReader(env.In())
	}
	text := func(format string, a ...any) {
		if !jsonOutput {
			_, _ = fmt.Fprintf(out, format+"\n", a...)
		}
	}

	// The parts come first: what is on, and on a terminal the offer to turn more on.
	configPath := filepath.Join(env.Home, ".aimux", "config.yaml")
	settings, loadErr := parts.Load(configPath)
	partsProblem := ""
	switch {
	case env.Home == "":
		// Joined to nothing, the path would be a file in whatever folder this runs from.
		settings, partsProblem = parts.Settings{}, "the home folder is not known, so the config file cannot be found; set HOME"
	case loadErr != nil:
		partsProblem = fmt.Sprintf("%s could not be read: %v", configPath, loadErr)
	case len(settings.Validate()) > 0:
		partsProblem = fmt.Sprintf("the parts section of %s has a problem; aimux setup --check says where", configPath)
	}
	var notOn []string
	if interactive && partsProblem == "" && o.partServices != nil {
		// A restart is always asked about here: --yes is about the setup steps below. Services are started
		// through launchd, so elsewhere the parts are listed and none is offered.
		var interrupted bool
		notOn, interrupted = turnOnParts(ctx, in, out, env.GOOS == "darwin", partsChange{configPath: configPath, services: o.partServices,
			ask: func(prompt string) bool { return ask(in, out, prompt, false) }})
		if interrupted {
			return errors.New("interrupted: setup stopped there, and nothing further was run")
		}
		settings, _ = parts.Load(configPath)
	}
	// A file with a problem is not reported part by part, as if it had been read.
	menu := []controller.PartChoice{}
	if partsProblem == "" {
		menu = controller.PartsMenu(settings)
	}
	doc := func(steps []setupStep, approved []string, summary []summaryLine) map[string]any {
		d := map[string]any{"schema_version": setupJSONVersion, "parts": menu, "steps": steps, "approved": approved, "summary": summary}
		if partsProblem != "" {
			d["parts_problem"] = partsProblem
		}
		return d
	}
	saidProblem := func() {
		if partsProblem != "" {
			_, _ = fmt.Fprintf(errOut, "Next: aimux setup --check (%s)\n", partsProblem)
		}
	}
	notOnErr := func() error {
		if len(notOn) == 0 {
			return nil
		}
		return fmt.Errorf("not turned on: %s (the reason is above)", strings.Join(notOn, ", "))
	}
	// Everything below belongs to one part, Sandboxes. Turned off in the file, none of it runs: that much is
	// plain whatever is wrong with another part.
	sandboxesClear := !slices.ContainsFunc(settings.Validate(), func(p parts.Problem) bool { return strings.HasPrefix(p.Path, "parts.sandboxes") })
	if sandboxes, ok := settings.Parts["sandboxes"]; loadErr == nil && sandboxesClear && ok && sandboxes.Enabled != nil && !*sandboxes.Enabled {
		if jsonOutput {
			if err := writeJSON(out, doc([]setupStep{}, []string{}, []summaryLine{})); err != nil {
				return err
			}
		}
		text("Sandboxes is off in %s: its checks and setup steps are not run.", configPath)
		saidProblem()
		return notOnErr()
	}
	if interactive {
		text("Sandboxes")
	}

	sb := collectSandboxStatus(ctx, env.browserEnv, env.ProfileDir)
	steps := missingSteps(env, o, sb)
	var pending, failed []string
	ran := false
	for i := range steps {
		s := &steps[i]
		s.Output = []string{}
		switch {
		case o.yes:
		case interactive:
			if !ask(in, out, s.desc+"? [Y/n] ", true) {
				s.Action = stepDeclined
				text("Skipped (declined): %s", s.desc)
				continue
			}
		default:
			s.Action = stepPending
			pending = append(pending, s.Name)
			text("Pending (needs --yes, or a terminal to ask on): %s", s.desc)
			continue
		}
		text("Running: %s", s.desc)
		say := func(format string, a ...any) {
			line := fmt.Sprintf(format, a...)
			s.Output = append(s.Output, line)
			text("%s", line)
		}
		ran = true
		if err := s.run(ctx, say); err != nil {
			s.Action = stepFailed
			s.Output = append(s.Output, err.Error())
			failed = append(failed, s.Name)
			text("Failed: %s: %v", s.Name, err)
			continue
		}
		s.Action = stepRan
	}

	approved := []string{}
	rv, rvErr := loadToolsReview(ctx, env.toolsEnv())
	var st []toolgateway.ToolStatus
	if rvErr == nil {
		st = toolgateway.Review(rv.reg, rv.pins, rv.live)
		if interactive {
			pins, names, err := reviewOnTerminal(in, out, rv, st, env.Now())
			if err != nil {
				return err
			}
			approved = names
			rv.pins = pins
			st = toolgateway.Review(rv.reg, rv.pins, rv.live)
		}
	}

	// Approvals can change what the runner needs (a first gh tool needs
	// aimux-github), so the report is read again after them too.
	if ran || len(approved) > 0 {
		sb = collectSandboxStatus(ctx, env.browserEnv, env.ProfileDir)
	}
	summary := []summaryLine{
		sandboxSummary(sb),
		toolsSummary(rv, st, rvErr),
		routerSummary(probeRouterKey(env.Home, env.AutoKey, env.GOOS)),
		browserSummary(collectBrowser(env.Home)),
		gatewaySummary(sb),
		workerSummary(ctx, env.gateway()),
	}
	var next []string
	if len(pending) > 0 {
		next = append(next, "aimux setup --yes (runs: "+strings.Join(pending, ", ")+")")
	}
	for _, l := range summary {
		for _, n := range l.Next {
			if !slices.Contains(next, n) {
				next = append(next, n)
			}
		}
	}

	if jsonOutput {
		if steps == nil {
			steps = []setupStep{}
		}
		if err := writeJSON(out, doc(steps, approved, summary)); err != nil {
			return err
		}
	} else {
		if len(steps) > 0 || len(approved) > 0 {
			_, _ = fmt.Fprintln(out)
		}
		for _, l := range summary {
			_, _ = fmt.Fprintf(out, "%s %-8s %s\n", checkSymbol[l.Status], l.Concern, l.Detail)
		}
	}
	for _, n := range next {
		_, _ = fmt.Fprintf(errOut, "Next: %s\n", n)
	}
	saidProblem()
	if len(failed) > 0 {
		return fmt.Errorf("setup step failed: %s (see its output above)", strings.Join(failed, ", "))
	}
	return notOnErr()
}

// turnOnParts shows the parts and asks which to turn on, by number. Each one chosen is stated in the config file
// and its service made to match, as `aimux enable` does. It returns the ids of those that could not be turned on;
// why is printed where it happened. An empty answer, or none to read, turns nothing on. With offer false the parts
// are listed and nothing is asked. interrupted: an interrupt arrived while a service was being changed; that
// change was finished or put back, and nothing after it was begun.
func turnOnParts(ctx context.Context, in *bufio.Reader, out io.Writer, offer bool, change partsChange) (notOn []string, interrupted bool) {
	settings, _ := parts.Load(change.configPath)
	menu := controller.PartsMenu(settings)
	_, _ = fmt.Fprintln(out, "Parts")
	offered := map[int]controller.PartChoice{}
	for _, c := range menu {
		number, about := "   ", c.About
		if offer && c.Number > 0 {
			number = fmt.Sprintf("%3d", c.Number)
			offered[c.Number] = c
		}
		if c.Why != "" {
			about += " (" + c.Why + ")"
		}
		_, _ = fmt.Fprintf(out, "%s  %-19s %-18s %s\n", number, c.Name, c.State, about)
	}
	_, _ = fmt.Fprintln(out)
	if len(offered) == 0 {
		return nil, false
	}
	var chosen []controller.PartChoice
	for chosen == nil {
		_, _ = fmt.Fprint(out, "Turn on (numbers, or Enter for none): ")
		line, err := in.ReadString('\n')
		words := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
		if len(words) == 0 {
			if err != nil {
				_, _ = fmt.Fprintln(out)
			}
			return nil, false
		}
		for _, word := range words {
			var n int
			if _, scanErr := fmt.Sscanf(word, "%d", &n); scanErr != nil || fmt.Sprint(n) != word || offered[n].ID == "" {
				_, _ = fmt.Fprintf(out, "%s is not one of the numbers.\n", word)
				chosen = nil
				break
			}
			if !slices.ContainsFunc(chosen, func(c controller.PartChoice) bool { return c.ID == offered[n].ID }) {
				chosen = append(chosen, offered[n])
			}
		}
		if err != nil && chosen == nil {
			return nil, false
		}
	}
	for _, c := range chosen {
		_, _ = fmt.Fprintf(out, "\n%s\n", c.Name)
		one := change
		one.part, one.needsService = c.ID, true
		// An interrupt while a service is changed ends the wait; a service that did not come up still gets its job
		// file back first, and then setup stops. At the question before a restart an interrupt ends the command
		// as it does at any other question, so it is not caught while one is open.
		applyCtx, cancel := context.WithCancel(ctx)
		sig := make(chan os.Signal, 1)
		watch := func() { signal.Notify(sig, os.Interrupt, syscall.SIGTERM) }
		go func() {
			select {
			case <-sig:
				cancel()
			case <-applyCtx.Done():
			}
		}()
		one.ask = func(prompt string) bool {
			signal.Stop(sig)
			defer watch()
			return change.ask(prompt)
		}
		watch()
		err := one.run(applyCtx, func(file string) error { return parts.EnablePart(file, c.ID) }, out)
		signal.Stop(sig)
		stopped := applyCtx.Err() != nil && ctx.Err() == nil
		cancel()
		if err != nil {
			_, _ = fmt.Fprintf(out, "%s: %v\n", c.Name, err)
			notOn = append(notOn, c.ID)
		}
		if stopped {
			return notOn, true
		}
	}
	_, _ = fmt.Fprintln(out)
	return notOn, false
}

// sandboxPrepare is what aimux sandbox setup would do for sb: start the
// gateway, build the sandbox and tool runner images, create the providers
// (the runner's only when an approved source needs it). Empty: nothing.
func sandboxPrepare(sb sandboxReport) []string {
	var what []string
	if sb.Gateway.Status == "unreachable" {
		what = append(what, "start the OpenShell gateway")
	}
	for _, img := range []component{sb.Image, sb.RunnerImage} {
		if img.Status == "missing" {
			what = append(what, "build "+img.Name)
		}
	}
	switch sb.Provider.Status {
	case "missing":
		what = append(what, "create provider "+wingmanProvider)
	case "unknown":
		if sb.Gateway.Status == "unreachable" {
			what = append(what, "create provider "+wingmanProvider+" if missing")
		}
	}
	switch {
	case !sb.RunnerProvider.Needed:
	case sb.RunnerProvider.Status == "missing":
		what = append(what, "create provider "+runnerProvider)
	case sb.RunnerProvider.Status == "unknown" && sb.Gateway.Status == "unreachable":
		what = append(what, "create provider "+runnerProvider+" if missing")
	}
	return what
}

// missingSteps is each setup step whose result is missing, in the order
// they run: the tool registry, clients.yaml, the sandbox, the browser host.
func missingSteps(env frontEnv, o setupOpts, sb sandboxReport) []setupStep {
	home := env.Home
	var steps []setupStep
	if _, err := os.Lstat(toolreg.DefaultPath(home)); errors.Is(err, fs.ErrNotExist) {
		desc := "Write the default tool registry " + toolreg.DefaultPath(home)
		if _, err := os.Stat(toolreg.LegacyPath(home)); err == nil {
			desc = "Migrate " + toolreg.LegacyPath(home) + " to " + toolreg.DefaultPath(home) + " (the old file is kept)"
		}
		steps = append(steps, setupStep{Name: "tools registry", desc: desc, run: func(_ context.Context, say func(string, ...any)) error {
			_, err := setupToolSources(home, say)
			return err
		}})
	}
	clients := filepath.Join(home, ".config", "aimux", "clients.yaml")
	if _, err := os.Lstat(clients); errors.Is(err, fs.ErrNotExist) {
		steps = append(steps, setupStep{Name: "clients", desc: "Write " + clients + " (the profiles Wingman may ask for, and its limits)",
			run: func(_ context.Context, say func(string, ...any)) error { return writeDefaultClients(home, say) }})
	}
	if what := sandboxPrepare(sb); len(what) > 0 {
		runner := slices.ContainsFunc(what, func(w string) bool { return strings.HasPrefix(w, "create provider "+runnerProvider) })
		steps = append(steps, setupStep{Name: "sandbox", desc: "Prepare the sandbox: " + strings.Join(what, ", ") + " (aimux sandbox setup)",
			run: func(ctx context.Context, say func(string, ...any)) error {
				_, err := sandboxSetupFor(ctx, env.browserEnv, o.sandboxDir, "aimux setup", say, runner)
				return err
			}})
	}
	br := collectBrowser(home)
	id := o.extID
	if id == "" && br.IDErr == nil {
		id = br.ExtensionID
	}
	if id != "" && (!br.usable() || br.ExtensionID != id) {
		steps = append(steps, setupStep{Name: "browser", desc: "Register the Wingman native host for extension " + id + " (aimux browser install)",
			run: func(ctx context.Context, say func(string, ...any)) error {
				var b bytes.Buffer
				err := browserInstall(ctx, env.browserEnv, id, nil, false, &b)
				for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
					if l != "" {
						say("%s", l)
					}
				}
				return err
			}})
	}
	return steps
}

// ask prints prompt and reads one answer: y or yes is true, an empty line
// is def, anything else (and end of input) is false.
func ask(in *bufio.Reader, out io.Writer, prompt string, def bool) bool {
	_, _ = fmt.Fprint(out, prompt)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		_, _ = fmt.Fprintln(out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

// reviewOnTerminal offers each tool that needs review: a yes shows its
// diff, and only a second yes approves it. The approved tools are pinned
// in one save. It returns the pins after and the approved set/tool names.
func reviewOnTerminal(in *bufio.Reader, out io.Writer, rv toolsReview, st []toolgateway.ToolStatus, now time.Time) (toolgateway.PinFile, []string, error) {
	changes := toolgateway.Diff(rv.reg, rv.pins, rv.live)
	var targets []string
	for _, s := range st {
		if !s.NeedsReview() {
			continue
		}
		target := s.Set + "/" + s.Tool
		if !ask(in, out, fmt.Sprintf("%s %s: Show diff and approve now? [y/N] ", s.Status, target), false) {
			continue
		}
		var mine []toolgateway.Change
		for _, c := range changes {
			if c.Set == s.Set && c.Tool == s.Tool {
				mine = append(mine, c)
			}
		}
		if len(mine) == 0 {
			_, _ = fmt.Fprintf(out, "%s %s\n  %s\n", s.Status, target, s.Reason)
		}
		printChanges(out, mine)
		if ask(in, out, "Approve "+target+"? [y/N] ", false) {
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return rv.pins, []string{}, nil
	}
	res, _, err := approveAndSave(rv, targets, now)
	if err != nil {
		return rv.pins, nil, err
	}
	names := []string{}
	for _, a := range res {
		switch a.Action {
		case toolgateway.ActionApproved, toolgateway.ActionUpgraded, toolgateway.ActionUnchanged:
			names = append(names, a.Set+"/"+a.Tool)
			_, _ = fmt.Fprintf(out, "Approved %s/%s\n", a.Set, a.Tool)
		case toolgateway.ActionSkipped:
			_, _ = fmt.Fprintf(out, "Not approved %s/%s (%s)\n", a.Set, a.Tool, a.Reason)
		}
	}
	_, _ = fmt.Fprintln(out, toolgateway.Disclaimer)
	pins, err := rv.store.LoadFile()
	return pins, names, err
}

func sandboxSummary(r sandboxReport) summaryLine {
	l := summaryLine{Concern: "sandbox", Status: checkOK, Next: []string{}}
	var problems []string
	worse := func(s string) {
		if s == checkFail || l.Status == checkOK {
			l.Status = s
		}
	}
	for _, c := range []component{r.Image, r.RunnerImage, r.Provider} {
		switch c.Status {
		case "missing":
			worse(checkFail)
			problems = append(problems, c.Name+" missing")
			l.Next = appendNew(l.Next, "aimux setup --yes (or aimux sandbox setup)")
		case "unknown":
			if c.Name == wingmanProvider && r.Gateway.Status == "unreachable" {
				worse(checkWarn)
				problems = append(problems, "provider unknown (gateway unreachable)")
				continue
			}
			worse(checkFail)
			problems = append(problems, c.Name+" unknown ("+c.Error+")")
			if c.Name != wingmanProvider { // an image
				l.Next = appendNew(l.Next, "podman machine start, then aimux setup")
			}
		}
	}
	if r.RunnerProvider.Status == "missing" && r.RunnerProvider.Needed {
		worse(checkFail)
		problems = append(problems, r.RunnerProvider.Name+" missing (an approved source needs it)")
		l.Next = appendNew(l.Next, "aimux setup --yes (or aimux sandbox setup)")
	}
	current, stale := 0, 0
	for _, p := range r.Profiles {
		switch p.State {
		case stateCurrent, stateMissing:
			current++
		case stateStale:
			stale++
			worse(checkWarn)
			problems = append(problems, p.Sandbox+" policy stale (the next turn applies it)")
		case stateError:
			worse(checkFail)
			problems = append(problems, p.Profile+" does not compile")
			l.Next = appendNew(l.Next, "aimux doctor (names why "+p.Profile+" does not compile)")
		}
	}
	if len(problems) > 0 {
		l.Detail = strings.Join(problems, "; ")
		return l
	}
	l.Detail = fmt.Sprintf("image and provider present; %s up to date", plural(current, "sandbox policy", "sandbox policies"))
	if stale > 0 || r.Gateway.Status != "reachable" {
		l.Detail = "image and provider present"
	}
	return l
}

func toolsSummary(rv toolsReview, st []toolgateway.ToolStatus, err error) summaryLine {
	l := summaryLine{Concern: "tools", Status: checkOK, Next: []string{}}
	if err != nil {
		l.Status, l.Detail = checkFail, err.Error()
		l.Next = append(l.Next, "aimux doctor")
		return l
	}
	var problems, need []string
	active := 0
	for _, s := range st {
		switch {
		case s.NeedsReview():
			need = append(need, s.Set+"/"+s.Tool)
		case s.Status == toolgateway.StatusActive:
			active++
		}
	}
	if len(need) > 0 {
		problems = append(problems, fmt.Sprintf("%s review: %s", plural(len(need), "tool needs", "tools need"), strings.Join(need, ", ")))
		l.Next = append(l.Next, "aimux tools diff, then aimux tools approve "+strings.Join(reviewSets(st), " ")+" (or run aimux setup on a terminal)")
	}
	for _, n := range slices.Sorted(maps.Keys(rv.live)) {
		if rv.live[n].Err != nil {
			problems = append(problems, "backend "+n+" unavailable")
			l.Next = appendNew(l.Next, "aimux doctor (names each backend that is down)")
		}
	}
	if len(rv.problems) > 0 {
		problems = append(problems, plural(len(rv.problems), "registry entry skipped", "registry entries skipped"))
		l.Next = appendNew(l.Next, "aimux doctor (names each registry error)")
	}
	if len(problems) > 0 {
		l.Status, l.Detail = checkWarn, strings.Join(problems, "; ")
		return l
	}
	l.Detail = plural(active, "tool active", "tools active") + ", none to review"
	return l
}

// routerKeyProbe is the router's config and key as setup and doctor judge
// them; the key is checked by existence only.
type routerKeyProbe struct {
	Path    string
	Service string
	CfgErr  error
	Line    string // autoKeyLine, without its "Auto mode: " prefix
	Found   bool   // a secret store item or a 0600 file
	EnvOnly bool   // only the shell environment has it
	GOOS    string
}

func probeRouterKey(home string, p keyProbe, goos string) routerKeyProbe {
	r := routerKeyProbe{Path: filepath.Join(home, ".config", "aimux", "deciders.yaml"), GOOS: goos}
	kc := autoroute.DefaultDecider().Key
	if cfg, err := autoroute.LoadDecider(r.Path); err != nil {
		r.CfgErr = err
	} else {
		kc = cfg.Key
	}
	r.Service = kc.Service
	if r.Service == "" {
		r.Service = "aimux-openrouter"
	}
	line := autoKeyLine(kc, p, goos)
	r.Found = strings.HasPrefix(line, "Auto mode: OpenRouter key found (")
	r.EnvOnly = strings.Contains(line, "in your shell environment")
	r.Line = strings.TrimPrefix(line, "Auto mode: ")
	return r
}

// keyFix is how to store the router key where a Chrome-started serve finds it.
func (r routerKeyProbe) keyFix() string {
	if r.GOOS == "darwin" {
		return `security add-generic-password -U -a "$USER" -s ` + r.Service + ` -w`
	}
	return "put the key in a file with mode 0600 at ~/.config/aimux/keys/openrouter"
}

func routerSummary(r routerKeyProbe) summaryLine {
	l := summaryLine{Concern: "router", Status: checkOK, Detail: r.Line, Next: []string{}}
	switch {
	case r.CfgErr != nil:
		l.Status, l.Detail = checkFail, r.Path+" invalid: "+r.CfgErr.Error()
		l.Next = append(l.Next, "fix "+r.Path+", then aimux router status")
	case !r.Found:
		l.Status = checkWarn
		l.Next = append(l.Next, "store the OpenRouter key: "+r.keyFix()+" (then aimux router test \"<question>\")")
	}
	return l
}

func browserSummary(b browserState) summaryLine {
	l := summaryLine{Concern: "browser", Status: checkOK, Next: []string{}}
	switch {
	case b.usable():
		l.Detail = "native host registered for extension " + b.ExtensionID
	case b.IDErr == nil:
		l.Status, l.Detail = checkWarn, "extension "+b.ExtensionID+" pinned, but the native host is not registered"
		l.Next = append(l.Next, "aimux setup --yes")
	default:
		l.Status, l.Detail = checkWarn, "not set up (no Wingman extension id)"
		l.Next = append(l.Next, "aimux setup --extension-id <id> (the id is in chrome://extensions)")
	}
	return l
}

// gatewayListFix is the fix when the gateway answers status but sandbox
// list fails, most often because -g local is not registered.
const gatewayListFix = "openshell gateway list; if local is missing, register it: openshell gateway add http://<bind_address of ~/.config/openshell/gateway-podman.toml> --name local, then aimux doctor"

// gatewayStartFix is how to start the OpenShell gateway by hand.
const gatewayStartFix = "start the OpenShell gateway: openshell-gateway --config ~/.config/openshell/gateway-podman.toml (aimux setup --yes, aimux sandbox setup and remote serve start it too)"

func gatewaySummary(r sandboxReport) summaryLine {
	l := summaryLine{Concern: "gateway", Status: checkOK, Detail: "OpenShell gateway " + r.Gateway.Name + " reachable", Next: []string{}}
	switch {
	case r.Gateway.Status != "reachable":
		l.Status, l.Detail = checkFail, "OpenShell gateway "+r.Gateway.Name+" unreachable ("+r.Gateway.Error+")"
		l.Next = append(l.Next, gatewayStartFix)
	case r.Gateway.Error != "":
		l.Status, l.Detail = checkWarn, "OpenShell gateway "+r.Gateway.Name+" reachable, "+r.Gateway.Error
		l.Next = append(l.Next, gatewayListFix)
	}
	return l
}

// workerSummary is the tool gateway worker in setup's summary: Workspace
// turns need it.
func workerSummary(ctx context.Context, g gatewayStatuser) summaryLine {
	c := gatewayWorkerCheck(ctx, g)
	l := summaryLine{Concern: "worker", Status: c.Status, Detail: c.Detail, Next: []string{}}
	if c.Fix != "" {
		l.Next = append(l.Next, c.Fix)
	}
	return l
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}
