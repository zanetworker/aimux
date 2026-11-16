package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	neturl "net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// toolsSchemaVersion is the schema of every `aimux tools ... --json` document.
const toolsSchemaVersion = "1"

// toolsListTimeout bounds listing the live tools of every source.
const toolsListTimeout = 30 * time.Second

// toolsEnv holds what the tools commands touch outside the process.
type toolsEnv struct {
	Home string
	// Catalog lists the sources' live tools (mcpproxy.ReadonlyCatalog).
	Catalog func(ctx context.Context, cfg mcpproxy.ReadonlyConfig) (*mcpproxy.Catalog, map[string]error)
	Now     func() time.Time
	// IsTTY is whether remove may ask (stdin and stdout are terminals); In
	// is where the answer is read. Nil: no terminal.
	IsTTY func() bool
	In    func() io.Reader
}

func newToolsCmd() *cobra.Command {
	home, _ := os.UserHomeDir()
	return newToolsCmdWith(toolsEnv{Home: home, Catalog: mcpproxy.ReadonlyCatalog, Now: time.Now,
		IsTTY: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) // #nosec G115 -- small fds
		},
		In: func() io.Reader { return os.Stdin },
	})
}

func newToolsCmdWith(env toolsEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Review the tools aimux exposes: status, diff and approve pins; add or remove sources",
		Long: "Each set in ~/.config/aimux/tool-sources.yaml names the tools a source may expose. A tool is served only\n" +
			"while its live definition matches the one approved into ~/.aimux/tools/pins.json.\n" +
			toolgateway.Disclaimer,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show each tool's review state (active, unpinned, changed, unavailable, source_changed, not_in_registry); read-only",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return toolsStatus(c.Context(), env, c.OutOrStdout(), c.ErrOrStderr())
		},
	}
	diff := &cobra.Command{
		Use:   "diff [set|set/tool]",
		Short: "Show what approving would change: added, removed and changed tools, field by field; read-only",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return toolsDiff(c.Context(), env, args, c.OutOrStdout())
		},
	}
	var all bool
	var expect string
	approve := &cobra.Command{
		Use:   "approve <set|set/tool>...",
		Short: "Pin the live definitions of the named tools (or --all) as reviewed",
		Long: "Pins the definitions the sources list now, and prints each one pinned with its short hash. A definition\n" +
			"can change between aimux tools diff and approve: pass --expect <hash> (the live hash diff printed) with one\n" +
			"set/tool to refuse when the live definition is another one.",
		RunE: func(c *cobra.Command, args []string) error {
			if all == (len(args) > 0) {
				return errors.New("name the sets or set/tool pairs to approve, or pass --all (not both)")
			}
			if expect != "" && (all || len(args) != 1 || !strings.Contains(args[0], "/")) {
				return errors.New("--expect checks one tool: name exactly one <set>/<tool>")
			}
			return toolsApprove(c.Context(), env, args, all, expect, c.OutOrStdout(), c.ErrOrStderr())
		},
	}
	approve.Flags().BoolVar(&all, "all", false, "Approve every tool of every set")
	approve.Flags().StringVar(&expect, "expect", "", "Refuse unless the live definition's hash starts with this one (12 or more hex digits, as diff prints it)")
	cmd.AddCommand(status, diff, approve, newToolsAddCmd(env), newToolsRemoveCmd(env))
	return cmd
}

// readRegistry loads the registry under home without writing anything
// (toolreg.ReadHome): when only readonly.json exists, it is converted in
// memory and note says so (aimux setup migrates it for real).
func readRegistry(home string) (reg toolreg.Registry, note string, err error) {
	path, legacy := toolreg.DefaultPath(home), toolreg.LegacyPath(home)
	reg, err = toolreg.ReadHome(home)
	if err != nil {
		return toolreg.Registry{}, "", err
	}
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(legacy); err == nil {
			note = fmt.Sprintf("Note: %s is not migrated yet; read from %s (aimux setup migrates it)", path, legacy)
		}
	}
	return reg, note, nil
}

// toolsReview is everything the tools commands judge from.
type toolsReview struct {
	reg      toolreg.Registry
	pins     toolgateway.PinFile
	live     map[string]toolgateway.LiveSource
	store    toolgateway.PinStore
	note     string
	problems []string // registry entries Load dropped
}

func loadToolsReview(ctx context.Context, env toolsEnv) (toolsReview, error) {
	reg, note, err := readRegistry(env.Home)
	if err != nil {
		return toolsReview{}, err
	}
	return reviewFrom(ctx, env, reg, note)
}

// reviewFrom lists reg's sources live and loads the pins.
func reviewFrom(ctx context.Context, env toolsEnv, reg toolreg.Registry, note string) (toolsReview, error) {
	store := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(env.Home)}
	pins, err := store.LoadFile()
	if err != nil {
		return toolsReview{}, fmt.Errorf("%w; until it is fixed, no tool is granted or served (remote serve fails closed)", err)
	}
	ctx, cancel := context.WithTimeout(ctx, toolsListTimeout)
	defer cancel()
	cat, down := env.Catalog(ctx, mcpproxy.ReadonlyConfigFrom(reg))
	problems := []string{}
	for _, m := range []map[string]error{reg.SourceErrors, reg.SetErrors} {
		for _, n := range slices.Sorted(maps.Keys(m)) {
			problems = append(problems, m[n].Error())
		}
	}
	return toolsReview{reg: reg, pins: pins, live: toolgateway.LiveFromCatalog(cat, down), store: store, note: note, problems: problems}, nil
}

func writeJSON(out io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

// reviewSets is the sets with a tool to review, sorted.
func reviewSets(st []toolgateway.ToolStatus) []string {
	var sets []string
	for _, s := range st {
		if s.NeedsReview() && !slices.Contains(sets, s.Set) {
			sets = append(sets, s.Set)
		}
	}
	slices.Sort(sets)
	return sets
}

func toolsStatus(ctx context.Context, env toolsEnv, out, errOut io.Writer) error {
	rv, err := loadToolsReview(ctx, env)
	if err != nil {
		return err
	}
	st := toolgateway.Review(rv.reg, rv.pins, rv.live)
	need := 0
	for _, s := range st {
		if s.NeedsReview() {
			need++
		}
	}
	if rv.note != "" {
		_, _ = fmt.Fprintln(errOut, rv.note)
	}
	if jsonOutput {
		if st == nil {
			st = []toolgateway.ToolStatus{}
		}
		if err := writeJSON(out, map[string]any{"schema_version": toolsSchemaVersion, "tools": st, "needs_review": need, "registry_errors": rv.problems}); err != nil {
			return err
		}
	} else {
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "SET\tTOOL\tSTATUS\tAPPROVED\tREASON")
		for _, s := range st {
			set, at := s.Set, "-"
			if set == "" {
				set = "(" + s.Source + ")"
			}
			if s.ApprovedAt != nil {
				at = s.ApprovedAt.UTC().Format("2006-01-02 15:04")
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", set, s.Tool, s.Status, at, s.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		for _, p := range rv.problems {
			_, _ = fmt.Fprintf(out, "Warning: %s; it is skipped\n", p)
		}
	}
	if sets := reviewSets(st); len(sets) > 0 {
		_, _ = fmt.Fprintf(errOut, "Next: aimux tools diff, then aimux tools approve %s\n", strings.Join(sets, " "))
	}
	return nil
}

// matchTarget reports whether set/tool is selected by target ("" selects
// everything). Resolve makes wiki:readonly select notes:readonly.
func matchTarget(reg toolreg.Registry, target, set, tool string) bool {
	if target == "" {
		return true
	}
	tset, ttool, hasTool := strings.Cut(target, "/")
	if hasTool && ttool != tool {
		return false
	}
	if tset == set {
		return true
	}
	a, errA := reg.Resolve(tset)
	b, errB := reg.Resolve(set)
	return errA == nil && errB == nil && reflect.DeepEqual(a, b)
}

func checkTarget(reg toolreg.Registry, target string) error {
	set, tool, hasTool := strings.Cut(target, "/")
	s, err := reg.Resolve(set)
	if err != nil {
		return fmt.Errorf("target %q: %w", target, err)
	}
	if hasTool && !slices.Contains(s.Tools, tool) {
		return fmt.Errorf("target %q: set %s does not list tool %q", target, set, tool)
	}
	return nil
}

func toolsDiff(ctx context.Context, env toolsEnv, args []string, out io.Writer) error {
	rv, err := loadToolsReview(ctx, env)
	if err != nil {
		return err
	}
	target := ""
	if len(args) == 1 {
		target = args[0]
		if err := checkTarget(rv.reg, target); err != nil {
			return err
		}
	}
	changes := []toolgateway.Change{}
	for _, c := range toolgateway.Diff(rv.reg, rv.pins, rv.live) {
		if matchTarget(rv.reg, target, c.Set, c.Tool) {
			changes = append(changes, c)
		}
	}
	if jsonOutput {
		return writeJSON(out, map[string]any{"schema_version": toolsSchemaVersion, "changes": changes})
	}
	if len(changes) == 0 {
		_, _ = fmt.Fprintln(out, "No changes to review.")
		return nil
	}
	printChanges(out, changes)
	_, _ = fmt.Fprintln(out, "Approve with: aimux tools approve <set|set/tool>")
	return nil
}

// printChanges prints each change and its field-level differences.
func printChanges(out io.Writer, changes []toolgateway.Change) {
	for _, c := range changes {
		_, _ = fmt.Fprintf(out, "%s %s/%s", c.Kind, c.Set, c.Tool)
		if c.Hash != "" {
			_, _ = fmt.Fprintf(out, " (live %s)", toolgateway.ShortHash(c.Hash))
		}
		_, _ = fmt.Fprintln(out)
		if c.Note != "" {
			_, _ = fmt.Fprintf(out, "  %s\n", c.Note)
		}
		switch {
		case c.Fields != nil:
			for _, f := range c.Fields {
				switch {
				case f.Before == "":
					_, _ = fmt.Fprintf(out, "  + %s: %s\n", f.Path, f.After)
				case f.After == "":
					_, _ = fmt.Fprintf(out, "  - %s: %s\n", f.Path, f.Before)
				default:
					_, _ = fmt.Fprintf(out, "  %s: %s -> %s\n", f.Path, f.Before, f.After)
				}
			}
		case c.After != nil:
			_, _ = fmt.Fprintf(out, "  live: %s\n", c.After)
		case c.Before != nil:
			_, _ = fmt.Fprintf(out, "  approved: %s\n", c.Before)
		}
	}
}

// approveAndSave approves targets against rv and writes the pins only when
// a record changed. It reports whether it wrote.
func approveAndSave(rv toolsReview, targets []string, now time.Time) ([]toolgateway.Approval, bool, error) {
	out, res, err := toolgateway.Approve(rv.reg, rv.pins, rv.live, targets, now)
	if err != nil {
		return nil, false, err
	}
	if reflect.DeepEqual(out, rv.pins) {
		return res, false, nil
	}
	if err := rv.store.Save(out); err != nil {
		return res, false, err
	}
	return res, true, nil
}

var expectPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// checkExpect refuses target (one set/tool) unless its live definition's
// pin starts with expect (12+ hex digits, sha256: optional).
func checkExpect(rv toolsReview, target, expect string) error {
	want := strings.TrimPrefix(expect, "sha256:")
	if !expectPattern.MatchString(want) {
		return fmt.Errorf("--expect %q: want 12 to 64 lowercase hex digits, as aimux tools diff prints them", expect)
	}
	if err := checkTarget(rv.reg, target); err != nil {
		return err
	}
	set, tool, _ := strings.Cut(target, "/")
	s, _ := rv.reg.Resolve(set) // checked above
	raw, ok := rv.live[s.Source].Tools[tool]
	if !ok {
		return fmt.Errorf("%s: not listed live now, so it cannot match --expect; run aimux tools status", target)
	}
	live, err := toolgateway.PinTool(raw)
	if err != nil {
		return fmt.Errorf("%s: live definition is malformed: %w", target, err)
	}
	if !strings.HasPrefix(strings.TrimPrefix(live, "sha256:"), want) {
		return fmt.Errorf("%s: the live definition is %s, not the %s you expected; it changed since you looked: run aimux tools diff %s again",
			target, toolgateway.ShortHash(live), toolgateway.ShortHash(want), target)
	}
	return nil
}

func toolsApprove(ctx context.Context, env toolsEnv, targets []string, all bool, expect string, out, errOut io.Writer) error {
	rv, err := loadToolsReview(ctx, env)
	if err != nil {
		return err
	}
	if expect != "" {
		if err := checkExpect(rv, targets[0], expect); err != nil {
			return err
		}
	}
	if rv.note != "" {
		_, _ = fmt.Fprintln(errOut, rv.note)
	}
	if all {
		targets = slices.Sorted(maps.Keys(rv.reg.Sets))
	}
	res, wrote, err := approveAndSave(rv, targets, env.Now())
	if err != nil {
		return err
	}
	var skipped []string
	for _, a := range res {
		if a.Action == toolgateway.ActionSkipped {
			skipped = append(skipped, a.Set+"/"+a.Tool)
		}
	}
	if jsonOutput {
		if res == nil {
			res = []toolgateway.Approval{}
		}
		if err := writeJSON(out, map[string]any{"schema_version": toolsSchemaVersion, "results": res, "written": wrote, "note": toolgateway.Disclaimer}); err != nil {
			return err
		}
	} else {
		for _, a := range res {
			line := a.Action + " " + a.Set + "/" + a.Tool
			if a.Hash != "" {
				line += " " + toolgateway.ShortHash(a.Hash)
			}
			if a.Reason != "" {
				line += " (" + a.Reason + ")"
			}
			_, _ = fmt.Fprintln(out, line)
		}
		if wrote {
			_, _ = fmt.Fprintf(out, "Wrote %s\n", rv.store.Path)
		} else {
			_, _ = fmt.Fprintln(out, "Nothing to write: the pins already match.")
		}
		_, _ = fmt.Fprintln(out, toolgateway.Disclaimer)
	}
	if len(skipped) > 0 {
		_, _ = fmt.Fprintln(errOut, "Next: start the backends that are down, check aimux tools status, then approve again")
		return fmt.Errorf("could not approve %s", strings.Join(skipped, ", "))
	}
	return nil
}

// notesTools are the tools a local-notes source serves.
var notesTools = []string{"wiki_search", "wiki_read"}

func newToolsAddCmd(env toolsEnv) *cobra.Command {
	var folder, name, url, tools string
	cmd := &cobra.Command{
		Use:       "add <notes|mcp>",
		ValidArgs: []string{"notes", "mcp"},
		Short:     "Add a source and its <name>:readonly set to tool-sources.yaml; approves nothing",
		Long: "aimux tools add notes --folder <dir> [--name notes] adds a local-notes source over the folder and its set\n" +
			"<name>:readonly (wiki_search, wiki_read). aimux tools add mcp --name <name> --url <loopback url> --tools a,b\n" +
			"adds an MCP server on this Mac and its set <name>:readonly with those exact tools. Every other entry of\n" +
			"tool-sources.yaml is kept; a name it already has is refused. Nothing is approved: review the new tools with\n" +
			"aimux tools diff, then approve them with aimux tools approve <name>:readonly.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var src toolreg.Source
			var list []string
			switch args[0] {
			case "notes":
				if name == "" {
					name = "notes"
				}
				root, err := notesFolder(folder)
				if err != nil {
					return err
				}
				src, list = toolreg.Source{Type: toolreg.TypeLocalNotes, Root: root}, notesTools
			case "mcp":
				var err error
				if src, list, err = mcpSource(name, url, tools); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown source kind %q: want notes or mcp", args[0])
			}
			return toolsAdd(env, name, src, list, c.OutOrStdout(), c.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&folder, "folder", "", "notes: the folder of Markdown notes to search")
	cmd.Flags().StringVar(&name, "name", "", "The source's name, which its set, pins and tool names key on (notes: default notes)")
	cmd.Flags().StringVar(&url, "url", "", "mcp: the server's http URL on this Mac (127.0.0.1, ::1 or localhost)")
	cmd.Flags().StringVar(&tools, "tools", "", "mcp: the exact tool names the set may expose, comma separated")
	return cmd
}

// notesFolder is folder as an absolute path to an existing directory.
func notesFolder(folder string) (string, error) {
	if folder == "" {
		return "", errors.New("pass --folder <dir>: the folder of notes to search")
	}
	root, err := filepath.Abs(folder)
	if err != nil {
		return "", fmt.Errorf("folder %s: %w", folder, err)
	}
	st, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("folder %s: %w", root, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("folder %s is not a directory", root)
	}
	return root, nil
}

// mcpSource is an mcp-http source on a loopback URL with the named tools.
func mcpSource(name, rawURL, tools string) (toolreg.Source, []string, error) {
	if name == "" {
		return toolreg.Source{}, nil, errors.New("pass --name <name> for the MCP source")
	}
	u, err := neturl.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || !isLoopbackHost(u.Hostname()) {
		return toolreg.Source{}, nil, fmt.Errorf("--url %q must be an http URL on this Mac's loopback (127.0.0.1, ::1 or localhost), without userinfo", rawURL)
	}
	var list []string
	for _, t := range strings.Split(tools, ",") {
		if t = strings.TrimSpace(t); t != "" {
			list = append(list, t)
		}
	}
	if len(list) == 0 {
		return toolreg.Source{}, nil, errors.New("pass --tools a,b,c: the exact tools the set may expose")
	}
	return toolreg.Source{Type: toolreg.TypeMCPHTTP, URL: rawURL}, list, nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// migrateForEdit carries readonly.json into tool-sources.yaml first, so
// an edit never shadows it, and says so on stderr (stdout may carry --json).
func migrateForEdit(home string, out io.Writer) error {
	path, old := toolreg.DefaultPath(home), toolreg.LegacyPath(home)
	m, err := toolreg.MigrateReport(old, path)
	if err != nil {
		return err
	}
	if m.Migrated {
		_, _ = fmt.Fprintf(out, "Migrated %s to %s (the old file is kept as %s.migrated)\n", old, path, old)
		for _, d := range m.Dropped {
			_, _ = fmt.Fprintf(out, "Migrated tool config to %s; %s\n", path, d)
		}
	}
	return nil
}

func toolsAdd(env toolsEnv, name string, src toolreg.Source, tools []string, out, errOut io.Writer) error {
	path := toolreg.DefaultPath(env.Home)
	if err := migrateForEdit(env.Home, errOut); err != nil {
		return err
	}
	set, err := toolreg.AddSource(path, name, src, tools)
	if err != nil {
		return err
	}
	next := "aimux tools approve " + set
	if jsonOutput {
		return writeJSON(out, map[string]any{"schema_version": toolsSchemaVersion, "source": name, "set": set, "tools": tools, "path": path, "next": next})
	}
	_, _ = fmt.Fprintf(out, "Added source %s (%s) and set %s (%s) to %s\n", name, src.Type, set, strings.Join(tools, ", "), path)
	_, _ = fmt.Fprintln(out, "Nothing is approved yet: its tools stay hidden until you approve them.")
	_, _ = fmt.Fprintf(errOut, "Next: aimux tools diff %s, then %s\n", set, next)
	return nil
}

func newToolsRemoveCmd(env toolsEnv) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove <source>",
		Short: "Remove a source and every set naming it from tool-sources.yaml; its pins are kept, unused",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return toolsRemove(env, args[0], yes, c.OutOrStdout(), c.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Remove without asking")
	return cmd
}

func toolsRemove(env toolsEnv, name string, yes bool, out, errOut io.Writer) error {
	path := toolreg.DefaultPath(env.Home)
	if err := migrateForEdit(env.Home, errOut); err != nil {
		return err
	}
	if !yes {
		reg, err := toolreg.Load(path)
		if err != nil {
			return err
		}
		if _, ok := reg.Sources[name]; !ok && reg.SourceErrors[name] == nil {
			return fmt.Errorf("source %q: %w", name, toolreg.ErrNoSource)
		}
		if env.IsTTY == nil || env.In == nil || !env.IsTTY() {
			return fmt.Errorf("remove source %s: pass --yes to remove it without a terminal to ask on", name)
		}
		var sets []string
		for _, n := range slices.Sorted(maps.Keys(reg.Sets)) {
			if reg.Sets[n].Source == name {
				sets = append(sets, n)
			}
		}
		prompt := fmt.Sprintf("Remove source %s and its sets (%s) from %s?", name, strings.Join(sets, ", "), path)
		if !ask(bufio.NewReader(env.In()), out, prompt, false) {
			_, _ = fmt.Fprintln(out, "Nothing removed.")
			return nil
		}
	}
	sets, err := toolreg.RemoveSource(path, name)
	if err != nil {
		return err
	}
	kept := 0
	if pins, err := (toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(env.Home)}).LoadFile(); err == nil {
		kept = len(pins[name]) // an unreadable pins file is reported by aimux tools status
	}
	if jsonOutput {
		return writeJSON(out, map[string]any{"schema_version": toolsSchemaVersion, "source": name, "sets": sets, "path": path, "kept_pins": kept})
	}
	_, _ = fmt.Fprintf(out, "Removed source %s and sets %s from %s\n", name, strings.Join(sets, ", "), path)
	if kept > 0 {
		_, _ = fmt.Fprintf(out, "Its %d pins are kept but unused; aimux tools status lists them as %s, and adding %s back with the same folder or URL reuses them.\n",
			kept, toolgateway.StatusNotInRegistry, name)
	}
	return nil
}
