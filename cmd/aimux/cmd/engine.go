package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/parts"
)

// partsChange is what a command that edits the parts section needs to finish its work: where the file is, the
// services to make match it, and how to ask before a restart.
type partsChange struct {
	configPath string
	services   func() []controller.PartService
	ask        func(string) bool
	noApply    bool
	yes        bool
	asJSON     bool
	// part is the part the edit is about. After an apply that left its service as it was, the command does not
	// report success.
	part string
	// needsService: the command is about the part itself, so a part with no service aimux manages is said to be so.
	needsService bool
}

// errEditNotMade is an edit that was refused: the file is as it was.
var errEditNotMade = errors.New("the file was not changed")

// run makes the edit, says whether the file changed, and then makes the services match, unless told not to. An
// error after the edit says that the file holds the change and how to apply it: running the same command again
// would be refused for an add or a remove, which are done.
func (p partsChange) run(ctx context.Context, edit func(configPath string) error, out io.Writer) error {
	before, _ := os.ReadFile(p.configPath) // #nosec G304 -- the owner's own config file
	if err := edit(p.configPath); err != nil {
		return fmt.Errorf("%w: %w", errEditNotMade, err)
	}
	after, _ := os.ReadFile(p.configPath) // #nosec G304 -- the owner's own config file
	changed := !bytes.Equal(before, after)
	if p.noApply {
		if p.asJSON {
			return writeJSON(out, map[string]any{"schema_version": 1, "changed": changed, "applied": false})
		}
		_, _ = fmt.Fprintf(out, "%s\n\nNot applied. To make the services match: aimux setup --apply\n", p.said(changed))
		return nil
	}
	if !p.asJSON {
		_, _ = fmt.Fprintf(out, "%s\n\n", p.said(changed))
	}
	// Only the part this command is about: what the file says of another part waits for aimux setup --apply.
	err := runSetupParts(ctx, p.configPath, p.services(), setupPartsOpts{apply: true, yes: p.yes, asJSON: p.asJSON, ask: p.ask, onlyPart: p.part}, out)
	switch {
	case errors.Is(err, errNotAgreed):
		return errors.New("the file holds the change and the service was not restarted; to apply it later: aimux setup --apply")
	case errors.Is(err, errNeedsYes):
		return fmt.Errorf("the file holds the change; %w", err)
	case errors.Is(err, errLeftAlone):
		// Said below for the part this command was about, when it is that one.
		if own := p.leftAsItWas(); own != nil {
			return own
		}
		return fmt.Errorf("the file holds the change; %w", err)
	case err != nil:
		return err
	}
	return p.leftAsItWas()
}

// leftAsItWas is an error when the file says what was asked and the machine does not: the part's service could
// not be managed (started by hand, a job aimux cannot edit, a switch that is not built), or the part has no
// service aimux manages at all.
func (p partsChange) leftAsItWas() error {
	if p.part == "" {
		return nil
	}
	s, err := parts.Load(p.configPath)
	if err != nil {
		return err
	}
	lines := 0
	for _, line := range controller.PlanParts(p.services(), s) {
		if line.Part != p.part {
			continue
		}
		lines++
		if line.Action == controller.ActionSkip {
			return fmt.Errorf("the file holds the change, and the %s was left as it is: %s", parts.MustService(line.Service).Name, line.Note)
		}
	}
	if lines == 0 && p.needsService {
		return errors.New("the file holds the change; aimux does not start or stop anything for this part yet, so nothing else changed")
	}
	return nil
}

func (p partsChange) said(changed bool) string {
	if changed {
		return "Changed " + p.configPath + "."
	}
	return p.configPath + " already says that."
}

// partsChangeFor builds what an editing command needs from the machine it runs on.
func partsChangeFor(c *cobra.Command, env frontEnv, noApply, yes bool, part string) (partsChange, error) {
	if env.Home == "" {
		// Joined to nothing, the path would be a file in whatever folder this runs from.
		return partsChange{}, errors.New("the home folder is not known, so the config file cannot be found; set HOME")
	}
	change := partsChange{
		part:       part,
		configPath: filepath.Join(env.Home, ".aimux", "config.yaml"),
		noApply:    noApply, yes: yes, asJSON: jsonOutput,
		services: func() []controller.PartService {
			cfg, _ := config.Load(config.DefaultPath())
			return partServices(env.Home, c.Root().Version, cfg)
		},
	}
	if !jsonOutput && env.IsTTY != nil && env.IsTTY() {
		change.ask = askOn(env.In(), c.OutOrStdout())
	}
	return change, nil
}

func addApplyFlags(cmd *cobra.Command, noApply, yes *bool) {
	cmd.Flags().BoolVar(noApply, "no-apply", false, "Only change the config file; do not make the services match (aimux setup --apply does that later)")
	cmd.Flags().BoolVar(yes, "yes", false, "Restart a service without asking")
}

func newEngineCmd() *cobra.Command { return newEngineCmdWith(defaultFrontEnv()) }

func newEngineCmdWith(env frontEnv) *cobra.Command {
	engine := &cobra.Command{
		Use:   "engine",
		Short: "See and change which engines the inference proxy answers with",
		Long: "An engine is what answers a model call behind the inference proxy: Codex and Anthropic, which are built\n" +
			"in, and any other address that speaks the Messages API, such as an internal gateway. A session picks one by\n" +
			"the model it names: a plain name for Anthropic, codex:<model>, or <name>:<model>.\n\n" +
			"enable and disable switch an engine that exists and keep its settings. add tells aimux of a new address and\n" +
			"remove forgets it. Each changes the parts section of ~/.aimux/config.yaml and then makes the proxy match,\n" +
			"asking before it restarts it.",
	}
	var noApply, yes bool
	edit := func(use, short string, args cobra.PositionalArgs, change func(file string, args []string) error, after func(out io.Writer, args []string)) *cobra.Command {
		cmd := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(c *cobra.Command, a []string) error {
			c.SilenceUsage = true
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			p, err := partsChangeFor(c, env, noApply, yes, "inference-proxy")
			if err != nil {
				return err
			}
			err = p.run(ctx, func(file string) error { return change(file, a) }, c.OutOrStdout())
			// Only once the edit is in the file: a name that was refused is not repeated as something to run.
			if after != nil && !jsonOutput && !errors.Is(err, errEditNotMade) {
				after(c.OutOrStdout(), a)
			}
			return err
		}}
		addApplyFlags(cmd, &noApply, &yes)
		return cmd
	}
	enable := edit("enable <name>", "Switch an engine on: anthropic, or an upstream that was added", cobra.ExactArgs(1),
		func(file string, a []string) error { return parts.EnableEngine(file, a[0]) }, nil)
	disable := edit("disable <name>", "Switch an engine off and keep its settings", cobra.ExactArgs(1),
		func(file string, a []string) error { return parts.DisableEngine(file, a[0]) }, nil)
	remove := edit("remove <name>", "Forget an engine that was added", cobra.ExactArgs(1),
		func(file string, a []string) error { return parts.RemoveEngine(file, a[0]) }, nil)
	// Tab offers only what each would take: one that is off, one that is on, one that was added.
	enable.ValidArgsFunction = completeEdit(env.Home, first(func(c parts.Candidates) []string { return c.Enable }))
	disable.ValidArgsFunction = completeEdit(env.Home, first(func(c parts.Candidates) []string { return c.Disable }))
	remove.ValidArgsFunction = completeEdit(env.Home, first(func(c parts.Candidates) []string { return c.Remove }))
	engine.AddCommand(
		enable, disable,
		edit("add <name>=<address>", "Add an address that speaks the Messages API, paid for with your key, and switch it on", cobra.ExactArgs(1),
			func(file string, a []string) error {
				name, address, ok := strings.Cut(a[0], "=")
				if !ok {
					return errors.New("give it as name=address, for example: aimux engine add enmaas=https://gateway.example")
				}
				return parts.AddEngine(file, name, address)
			},
			func(out io.Writer, a []string) {
				name, _, _ := strings.Cut(a[0], "=")
				// Whether the key is there, never the key. Only where that can be asked without reading it: on
				// macOS. Elsewhere the entry is named and not looked for.
				there, probeErr := false, error(nil)
				if runtime.GOOS == "darwin" {
					there, probeErr = keyring.Probe(name + "-key")
				}
				switch {
				case runtime.GOOS != "darwin" || probeErr != nil:
					// Not looked for, or the look failed (a locked store): said as unknown, never as missing.
					_, _ = fmt.Fprintf(out, "\nIts key is expected in the secret-store entry %s-key; whether it is there was not checked.\n", name)
				case !there:
					_, _ = fmt.Fprintf(out, "\nIts key is expected in the secret-store entry %s-key, which is not there yet. This asks for it without showing it:\n  security add-generic-password -a \"$USER\" -s %s-key -w\n", name, name)
				}
			}),
		remove,
		newEngineListCmd(env),
		newEngineNamesCmd(env, "model", "Name the models roles use: one place where a model name is written",
			"A role names an entry here, never a model, so replacing a model is one change. A value is a model as a\n"+
				"session names it (codex:gpt-6.2-sol, claude-opus-5-5, <engine>:<model>) or codex:default, which follows\n"+
				"the proxy's current default Codex model.",
			"update <entry> <model>", "Name a model, or give an entry another one",
			func(file string, a []string) error { return parts.SetModel(file, a[0], a[1]) },
			"delete <entry>", "Take an entry out; one a role still uses is refused",
			func(file string, a []string) error { return parts.ClearModel(file, a[0]) },
			// The entry is offered; the model after it is typed.
			completeEdit(env.Home, first(func(c parts.Candidates) []string { return c.Entries })),
			completeEdit(env.Home, first(func(c parts.Candidates) []string { return c.FreeEntries }))),
		newEngineNamesCmd(env, "role", "Say which model a kind of work uses",
			"The one role is subagent: your own subagents that name no model. The main conversation's model is\n"+
				"chosen in the session, and a kind of subagent that should differ names its model in its own definition,\n"+
				"which wins over the role. A role is given an entry of aimux engine model, not a model.",
			"update <role> <entry>", "Give a role an entry of models",
			func(file string, a []string) error { return parts.SetRole(file, a[0], a[1]) },
			"delete <role>", "Take a role out, so that work uses the session's model again",
			func(file string, a []string) error { return parts.ClearRole(file, a[0]) },
			completeEdit(env.Home, func(c parts.Candidates, position int) []string {
				return [][]string{c.Roles, c.Entries, nil}[min(position, 2)]
			}),
			completeEdit(env.Home, first(func(c parts.Candidates) []string { return c.SetRoles }))),
	)
	return engine
}

// newEngineNamesCmd is the pair of commands that change one entry of models or of roles. They change the file
// and nothing else: no service runs differently for a role, which reaches a session through its shell.
func newEngineNamesCmd(env frontEnv, use, short, long, updateUse, updateShort string, update func(string, []string) error, deleteUse, deleteShort string, remove func(string, []string) error, completeUpdate, completeDelete completer) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Long: long}
	one := func(use, short string, n int, change func(string, []string) error, complete completer) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(n), ValidArgsFunction: complete, RunE: func(c *cobra.Command, a []string) error {
			c.SilenceUsage = true
			if env.Home == "" {
				return errors.New("the home folder is not known, so the config file cannot be found; set HOME")
			}
			file := filepath.Join(env.Home, ".aimux", "config.yaml")
			before, _ := os.ReadFile(file) // #nosec G304 -- the owner's own config file
			if err := change(file, a); err != nil {
				return err
			}
			after, _ := os.ReadFile(file) // #nosec G304 -- the owner's own config file
			changed := !bytes.Equal(before, after)
			if jsonOutput {
				return writeJSON(c.OutOrStdout(), map[string]any{"schema_version": 1, "changed": changed})
			}
			said := "Changed " + file + "."
			if !changed {
				said = file + " already says that."
			}
			_, _ = fmt.Fprintf(c.OutOrStdout(), "%s\nTo see what each role and subagent runs on: aimux engine list\n", said)
			return nil
		}}
	}
	cmd.AddCommand(one(updateUse, updateShort, 2, update, completeUpdate), one(deleteUse, deleteShort, 1, remove, completeDelete))
	return cmd
}

// engineRow is one engine as `aimux engine list` shows it; the dashboard and the TUI show the same row.
type engineRow = controller.EngineRow

func newEngineListCmd(env frontEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the engines: what the config file states, and what the running proxy answers with",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			c.SilenceUsage = true
			if env.Home == "" {
				return errors.New("the home folder is not known, so the config file cannot be found; set HOME")
			}
			cwd, _ := os.Getwd()
			read, err := readEngines(c.Context(), env.Home, cwd)
			if err != nil {
				return err
			}
			return writeEngines(c.OutOrStdout(), read.rows, read.port, read.answer, jsonOutput, read.roles)
		},
	}
}

// enginesRead is everything `aimux engine list` shows.
type enginesRead struct {
	rows         []engineRow
	port, answer int
	roles        engineRoles
}

// readEngines joins what the config file under home states with what the running proxy answers with, and adds
// the roles and the subagent definitions under home and, when cwd is named, under it. It reads only.
func readEngines(ctx context.Context, home, cwd string) (enginesRead, error) {
	views, stated, err := parts.ListEngines(filepath.Join(home, ".aimux", "config.yaml"))
	if err != nil {
		return enginesRead{}, err
	}
	service := parts.MustService("model-proxy")
	port := parts.InstalledProxyPort(filepath.Join(home, "Library", "LaunchAgents", service.Label+".plist"), modelproxy.StablePort)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	live, liveErr := readModelLiveness(ctx, fmt.Sprintf("127.0.0.1:%d", port))
	answer := proxySilent
	switch {
	case liveErr != nil || live.Service != modelproxy.LivenessService:
	case live.Engines == nil:
		answer = proxyOlder
	default:
		answer = proxySays
	}
	// Roles, and what each of the owner's subagent definitions runs on, from the same file.
	settings, _ := parts.Load(filepath.Join(home, ".aimux", "config.yaml"))
	proxy := settings.Parts["inference-proxy"]
	dirs := []controller.SubagentDir{{Path: filepath.Join(home, ".claude", "agents"), Label: "~/.claude/agents"}}
	if cwd != "" && filepath.Join(cwd, ".claude", "agents") != dirs[0].Path {
		dirs = append(dirs, controller.SubagentDir{Path: filepath.Join(cwd, ".claude", "agents"), Label: ".claude/agents"})
	}
	roles := engineRoles{Roles: controller.RolesView(proxy, live.Engines), Subagents: controller.SubagentModels(dirs, proxy)}
	return enginesRead{rows: engineRows(views, stated, live.Engines, answer), port: port, answer: answer, roles: roles}, nil
}

// enginesDocument is the list as JSON: what `aimux engine list --json` prints and the dashboard reads.
func enginesDocument(rows []engineRow, port int, roles engineRoles) map[string]any {
	doc := map[string]any{"schema_version": 1, "port": port, "engines": rows, "roles": roles.Roles, "subagents": roles.Subagents}
	if roles.Subagents == nil {
		doc["subagents"] = []controller.SubagentRow{}
	}
	return doc
}

// What the running proxy said about its engines.
const (
	proxySilent = iota // it does not answer
	proxyOlder         // it answers and does not say: a build from before it listed them
	proxySays
)

// engineRows joins what the file states with what the running proxy says it answers with. stated is false when
// the file says nothing about engines: the proxy then runs as it was installed, and there is nothing to differ
// from. An engine the proxy runs that the file does not list (one started by hand) is shown too.
func engineRows(views []parts.EngineView, stated bool, running []string, answer int) []engineRow {
	var rows []engineRow
	seen := map[string]bool{}
	for _, v := range views {
		row := engineRow{Name: v.Name, On: v.On, Type: v.Type, Auth: v.Auth, URL: v.URL, Stated: stated || v.Name == parts.EngineCodex}
		switch {
		case v.KeyItem != "":
			row.KeyAt = "secret store: " + v.KeyItem
		case v.Key != nil && v.Key.Env != nil:
			row.KeyAt = "environment: " + v.Key.Env.Name
		case v.Key != nil && v.Key.File != nil:
			row.KeyAt = "file: " + v.Key.File.Path
		case v.Key != nil && v.Key.Cmd != nil:
			row.KeyAt = "a command"
		}
		if answer == proxySays {
			is := slices.Contains(running, v.Name)
			row.Running = &is
		}
		seen[v.Name] = true
		rows = append(rows, row)
	}
	for _, name := range running {
		if !seen[name] {
			yes := true
			// When the file lists engines, one that runs and is not listed is one the file says is off.
			rows = append(rows, engineRow{Name: name, Type: controller.EngineNotInFile, Running: &yes, Stated: stated})
		}
	}
	return rows
}

// engineRoles is what the list shows beside the engines: the roles, and the owner's subagent definitions.
type engineRoles struct {
	Roles     []controller.RoleRow
	Subagents []controller.SubagentRow
}

func writeEngines(out io.Writer, rows []engineRow, port, answer int, asJSON bool, more ...engineRoles) error {
	var roles engineRoles
	if len(more) > 0 {
		roles = more[0]
	}
	if asJSON {
		if len(more) == 0 {
			return writeJSON(out, map[string]any{"schema_version": 1, "port": port, "engines": rows})
		}
		return writeJSON(out, enginesDocument(rows, port, roles))
	}
	defer writeRoles(out, roles)
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "ENGINE\tIN THE FILE\tRUNNING\tTYPE\tWHERE\tKEY")
	differs, unstated := false, false
	for _, r := range rows {
		stated, running, rowDiffers := r.Words()
		differs, unstated = differs || rowDiffers, unstated || stated == "not stated"
		kind, where := r.Type, printable(r.URL)
		if r.Name == parts.EngineCodex {
			where = "the Codex program, on your ChatGPT plan"
		}
		if r.Auth == parts.AuthCaller {
			where += " (your Claude login)"
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", printable(r.Name), stated, running, kind, where, printable(r.KeyAt))
	}
	_ = table.Flush()
	switch {
	case answer == proxySilent:
		_, _ = fmt.Fprintf(out, "\nThe inference proxy does not answer on port %d, so only the file is shown.\n", port)
	case answer == proxyOlder:
		_, _ = fmt.Fprintf(out, "\nThe inference proxy on port %d is a build from before it said which engines it has; aimux upgrade model-proxy brings it up to date.\n", port)
	case differs:
		_, _ = fmt.Fprintln(out, "\nThe running proxy and the file differ. To make them match: aimux setup --apply")
	case unstated:
		_, _ = fmt.Fprintln(out, "\nThe file says nothing about engines, so the proxy runs with the ones it was installed with.")
	}
	return nil
}

func newEnableCmd() *cobra.Command  { return newPartSwitchCmd(defaultFrontEnv(), true) }
func newDisableCmd() *cobra.Command { return newPartSwitchCmd(defaultFrontEnv(), false) }

func newPartSwitchCmd(env frontEnv, on bool) *cobra.Command {
	use, short, change := "enable <part>", "Turn a part of aimux on", parts.EnablePart
	if !on {
		use, short, change = "disable <part>", "Turn a part of aimux off", parts.DisablePart
	}
	var ids []string
	for _, part := range parts.Parts() {
		if !part.Always {
			ids = append(ids, part.ID)
		}
	}
	var noApply, yes bool
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(1),
		ValidArgsFunction: completeWords(func(position int, _ []string) []string {
			if position > 0 {
				return nil
			}
			return slices.Clone(ids)
		}),
		Long: short + ": " + strings.Join(ids, ", ") + ".\n\n" +
			"It states it in the parts section of ~/.aimux/config.yaml and then makes that part's service match, asking\n" +
			"before a restart or a stop (--yes agrees to either). What the file says about another part waits for\n" +
			"aimux setup --apply. A part the file does not name is left as it is found; aimux setup --check shows what the file says.",
		RunE: func(c *cobra.Command, a []string) error {
			c.SilenceUsage = true
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			p, err := partsChangeFor(c, env, noApply, yes, a[0])
			if err != nil {
				return err
			}
			p.needsService = true
			return p.run(ctx, func(file string) error { return change(file, a[0]) }, c.OutOrStdout())
		},
	}
	addApplyFlags(cmd, &noApply, &yes)
	return cmd
}

// writeRoles prints the roles and the subagent definitions under the engines. With no role set and no definition
// found there is nothing to say, and nothing is printed.
func writeRoles(out io.Writer, r engineRoles) {
	if len(r.Roles) > 1 {
		table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(table, "\nROLE\tMODEL\tENGINE")
		for _, row := range r.Roles {
			model, engine := "(the session's)", "-"
			if row.Entry != "" {
				model, engine = printable(row.Entry)+" = "+printable(row.Model), row.Engine
			}
			_, _ = fmt.Fprintf(table, "%s\t%s\t%s\n", row.Role, model, engine)
		}
		_ = table.Flush()
		for _, row := range r.Roles {
			if row.Note != "" {
				_, _ = fmt.Fprintf(out, "%s: %s\n", row.Role, row.Note)
			}
		}
	}
	if len(r.Subagents) == 0 {
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "\nSUBAGENT\tDEFINED IN\tITS MODEL\tRUNS ON")
	for _, row := range r.Subagents {
		model, on := printable(row.Model), row.RunsOn
		switch {
		case row.FromRole:
			model = "(none: the role's)"
		case row.Model == "":
			model = "(none)"
		}
		if on == "" {
			on = "the session's model"
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", printable(row.Name), row.DefinedIn, model, on)
	}
	_ = table.Flush()
	_, _ = fmt.Fprintln(out, "RUNS ON holds for a session started from a shell set by aimux model proxy env; one already running keeps what it started with.")
	_, _ = fmt.Fprintln(out, "Built-in subagents (Explore and the like) and those a plugin brings are not listed; Explore uses the session's model whatever the role says.")
}
