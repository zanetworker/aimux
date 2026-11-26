package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// doctorCheck is one check: a stable id, ok|warn|fail, what was found and,
// for a problem, the one command or action that fixes it.
type doctorCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func newDoctorCmd() *cobra.Command { return newDoctorCmdWith(defaultFrontEnv()) }

func newDoctorCmdWith(env frontEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Read-only checks of tools, router, port 7390, Wingman serve, sandbox and gateway, each problem with its fix; exit 1 on any failure",
		Long: "doctor checks, without writing anything, what Wingman's work lane depends on: the tool registry and pins,\n" +
			"tools that need review or are in no set, sets a profile compile leaves out, the router key, port 7390,\n" +
			"the Wingman serve's binary, the OpenShell gateway and each sandbox's policy. It lists the live tools of\n" +
			"each source (as aimux tools status does) and runs only read-only probes (podman image exists, openshell\n" +
			"status and lists, ps, lsof) and writes nothing. Every problem has one fix. Exit 1 when any check fails;\n" +
			"warnings alone exit 0.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runDoctor(c.Context(), env, c.OutOrStdout(), c.ErrOrStderr())
		},
	}
}

// runDoctor prints the checks on out, each problem with its fix, and each
// fix again as a Next: line on errOut, as the other nouns hint.
func runDoctor(ctx context.Context, env frontEnv, out, errOut io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	checks := collectDoctor(ctx, env)
	problems, failures := 0, 0
	for _, c := range checks {
		if c.Status != checkOK {
			problems++
		}
		if c.Status == checkFail {
			failures++
		}
	}
	if jsonOutput {
		if err := writeJSON(out, map[string]any{"schema_version": setupSchemaVersion, "healthy": failures == 0, "problems": problems, "checks": checks}); err != nil {
			return err
		}
	} else {
		for _, c := range checks {
			_, _ = fmt.Fprintf(out, "%s %s  %s\n", checkSymbol[c.Status], c.ID, c.Detail)
			if c.Fix != "" {
				_, _ = fmt.Fprintf(out, "    fix: %s\n", c.Fix)
			}
		}
	}
	for _, c := range checks {
		// A fix that is "nothing to do" names no next step; one that goes
		// on ("nothing to do; to serve it again, ...") hints the rest.
		next := strings.TrimPrefix(c.Fix, "nothing to do; ")
		if c.Status != checkOK && next != "" && !strings.HasPrefix(next, "nothing to do") {
			_, _ = fmt.Fprintf(errOut, "Next: %s\n", next)
		}
	}
	if failures > 0 {
		return fmt.Errorf("%s found (each has a fix)", plural(failures, "failure", "failures"))
	}
	if problems > 0 && !jsonOutput {
		_, _ = fmt.Fprintf(out, "%s, no failure (exit 0)\n", plural(problems, "warning", "warnings"))
	}
	return nil
}

func collectDoctor(ctx context.Context, env frontEnv) []doctorCheck {
	cs := toolsChecks(ctx, env)
	cs = append(cs, routerChecks(env)...)
	procs, psErr := listProcs(ctx, env.Run)
	port, portFree := portCheck(ctx, env, procs, psErr)
	cs = append(cs, port, serveCheck(env, procs, psErr))
	sb := collectSandboxStatus(ctx, env.browserEnv, env.ProfileDir)
	cs = append(cs, gatewayCheck(sb), gatewayWorkerCheck(ctx, env.gateway()))
	return append(cs, sandboxChecks(sb, portFree)...)
}

func ok(id, detail string) doctorCheck { return doctorCheck{ID: id, Status: checkOK, Detail: detail} }

// profileSets maps each tool set to the available profiles that request
// it, sorted.
func profileSets(dir string) map[string][]string {
	user, _ := profile.LoadDir(dir) // a bad file is skipped, as everywhere profiles resolve
	names := profile.BuiltinNames()
	for n := range user {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	out := map[string][]string{}
	for _, n := range names {
		m, err := profile.Resolve(n, user)
		if err != nil || !m.Available {
			continue
		}
		for _, s := range m.Tools {
			out[s] = append(out[s], n)
		}
	}
	return out
}

func requestedBy(profiles []string) string {
	if len(profiles) == 0 {
		return "requested by no profile"
	}
	return "requested by " + strings.Join(profiles, ", ")
}

func toolsChecks(ctx context.Context, env frontEnv) []doctorCheck {
	home := env.Home
	regPath, legacy := toolreg.DefaultPath(home), toolreg.LegacyPath(home)
	var cs []doctorCheck

	if legacyRegistryWaiting(home) {
		cs = append(cs, doctorCheck{ID: "tools.legacy", Status: checkWarn,
			Detail: regPath + " is absent and " + legacy + " is not migrated yet (it is read in its place)",
			Fix:    "aimux setup --yes (migrates it once; the old file is kept as readonly.json.migrated)"})
	} else {
		cs = append(cs, ok("tools.legacy", "no readonly.json waiting to be migrated"))
	}

	pinsPath := toolgateway.DefaultPinsPath(home)
	_, pinsErr := (toolgateway.PinStore{Path: pinsPath}).LoadFile()
	if pinsErr != nil {
		cs = append(cs, doctorCheck{ID: "tools.pins", Status: checkFail,
			Detail: pinsErr.Error() + "; until it is fixed no tool is granted or served",
			Fix:    "move " + pinsPath + " aside, then aimux tools diff and aimux tools approve <set> for each set you review"})
	} else {
		cs = append(cs, ok("tools.pins", pinsPath+" reads"))
	}

	reg, note, err := readRegistry(home)
	if err != nil {
		return append(cs, doctorCheck{ID: "tools.registry", Status: checkFail, Detail: err.Error(),
			Fix: "fix " + regPath + " (strict YAML; aimux tools status shows the same error)"})
	}
	_ = note // tools.legacy says it
	cs = append(cs, ok("tools.registry", fmt.Sprintf("%s, %s", plural(len(reg.Sources), "source", "sources"), plural(len(reg.Sets), "set", "sets"))))
	wanted := profileSets(env.ProfileDir)
	for _, n := range slices.Sorted(maps.Keys(reg.SetErrors)) {
		cs = append(cs, doctorCheck{ID: "tools.registry.set." + n, Status: checkWarn,
			Detail: reg.SetErrors[n].Error() + "; the set is skipped, " + requestedBy(wanted[n]),
			Fix:    "fix set " + n + " in " + regPath + ", then aimux tools status"})
	}
	for _, n := range slices.Sorted(maps.Keys(reg.SourceErrors)) {
		cs = append(cs, doctorCheck{ID: "tools.registry.source." + n, Status: checkWarn,
			Detail: reg.SourceErrors[n].Error() + "; the source and its sets are skipped",
			Fix:    "fix source " + n + " in " + regPath + ", then aimux tools status"})
	}
	if pinsErr != nil {
		return cs // review and compiles need the pins
	}

	rv, err := reviewFrom(ctx, env.toolsEnv(), reg, "")
	if err != nil {
		return append(cs, doctorCheck{ID: "tools.review", Status: checkFail, Detail: err.Error(), Fix: "aimux tools status"})
	}
	st := toolgateway.Review(rv.reg, rv.pins, rv.live)
	var review, noSet []doctorCheck
	active := 0
	for _, s := range st {
		t := s.Set + "/" + s.Tool
		switch {
		case s.NeedsReview():
			review = append(review, doctorCheck{ID: "tools.review." + t, Status: checkWarn, Detail: s.Status + ": " + s.Reason + "; it is not served until approved",
				Fix: "aimux tools diff " + t + ", then aimux tools approve " + t})
		case s.Status == toolgateway.StatusNotInRegistry:
			c := doctorCheck{ID: "tools.no_set." + s.Source + "/" + s.Tool, Status: checkWarn,
				Detail: "pinned, but no set in tool-sources.yaml lists it, so it is never served",
				Fix:    "add " + s.Tool + " to a set of source " + s.Source + " in " + regPath + " if it should be served (else nothing to do)"}
			if _, known := reg.Sources[s.Source]; !known {
				c.Detail = s.Reason + "; it is never served"
				c.Fix = "nothing to do; to serve it again, add source " + s.Source + " back with aimux tools add (same folder or URL)"
			}
			noSet = append(noSet, c)
		case s.Status == toolgateway.StatusActive:
			active++
		}
	}
	if review == nil {
		review = []doctorCheck{ok("tools.review", plural(active, "tool active", "tools active")+", none to review")}
	}
	if noSet == nil {
		noSet = []doctorCheck{ok("tools.no_set", "every pinned tool is in a set")}
	}
	cs = append(append(cs, review...), noSet...)
	for _, n := range slices.Sorted(maps.Keys(rv.live)) {
		e := rv.live[n].Err
		if e == nil {
			continue
		}
		src := reg.Sources[n]
		fix := "start the " + n + " MCP server at " + src.URL + ", then aimux tools status"
		if src.Type == toolreg.TypeLocalNotes {
			fix = "create the notes folder " + src.Root + " or fix source " + n + " in " + regPath + ", then run aimux tools status"
		}
		cs = append(cs, doctorCheck{ID: "tools.backend." + n, Status: checkWarn, Detail: "unavailable: " + clipText(e.Error(), 200) + "; its tools are hidden", Fix: fix})
	}

	// Sets each profile's compile leaves out, other than those a registry
	// error above already names.
	snap, _, _ := toolgateway.NewStore(home).Reload() // reads only; never migrates
	sets := profile.RegistrySets(snap.Registry(), toolgateway.ServedPins(snap))
	var omitted []doctorCheck
	for _, s := range slices.Sorted(maps.Keys(wanted)) {
		if reg.SetErrors[s] != nil {
			continue
		}
		_, _, err := sets(s)
		if !errors.Is(err, profile.ErrUnavailable) {
			continue
		}
		fix := "add set " + s + " to " + regPath + ", or remove it from the profile"
		if errors.Is(err, profile.ErrUnpinned) {
			fix = "aimux tools diff " + s + ", then aimux tools approve " + s
		}
		for _, p := range wanted[s] {
			// A builtin set outside the default registry (gh:readonly) is
			// opt-in: not configured is not a problem, since a builtin cannot
			// drop it. A default set (jira, slack, google, wiki) still warns.
			if _, isDefault := toolreg.Default().Sets[s]; !isDefault && s != "wiki:readonly" && errors.Is(err, toolreg.ErrNotConfigured) && slices.Contains(profile.BuiltinNames(), p) {
				continue
			}
			omitted = append(omitted, doctorCheck{ID: "tools.omitted." + p + "." + s, Status: checkWarn,
				Detail: "profile " + p + " requests " + s + ", which its compile leaves out: " + err.Error(), Fix: fix})
		}
	}
	if omitted == nil {
		omitted = []doctorCheck{ok("tools.omitted", "every set the profiles request compiles in")}
	}
	return append(cs, omitted...)
}

func routerChecks(env frontEnv) []doctorCheck {
	r := probeRouterKey(env.Home, env.AutoKey, env.GOOS)
	cfg := ok("router.config", r.Path+" valid (or absent: built-in defaults)")
	if r.CfgErr != nil {
		cfg = doctorCheck{ID: "router.config", Status: checkFail, Detail: r.CfgErr.Error() + "; Auto asks every time until it is fixed",
			Fix: "fix " + r.Path + ", then aimux router status"}
	}
	key := ok("router.key", r.Line)
	if !r.Found {
		key = doctorCheck{ID: "router.key", Status: checkWarn, Detail: r.Line, Fix: r.keyFix()}
	}
	return []doctorCheck{cfg, key}
}

// procInfo is one running process: its pid, start time and command line
// as ps prints it (arguments joined by spaces).
type procInfo struct {
	PID   int
	Start time.Time
	Cmd   string
}

// aimuxServe is whether p is an aimux remote serve: its command line
// starts with ref (the binary host.sh execs, when known), or its first
// word is a binary named aimux. A grep or sh -c that mentions a serve is not one.
func (p procInfo) aimuxServe(ref string) bool {
	if ref != "" && strings.HasPrefix(p.Cmd, ref+" remote serve") {
		return true
	}
	f := strings.Fields(p.Cmd)
	return len(f) >= 3 && filepath.Base(f[0]) == "aimux" && f[1] == "remote" && f[2] == "serve"
}

func (p procInfo) localOnly() bool { return slices.Contains(strings.Fields(p.Cmd), "--local-only") }

// psLine is one line of ps -o pid=,lstart=,command=.
var psLine = regexp.MustCompile(`^\s*(\d+)\s+(\w{3}\s+\w{3}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4})\s+(.+)$`)

// listProcs lists every process with ps.
func listProcs(ctx context.Context, run func(context.Context, []string, ...string) (string, error)) ([]procInfo, error) {
	out, err := run(ctx, nil, "ps", "-axww", "-o", "pid=,lstart=,command=")
	if err != nil {
		return nil, err
	}
	var procs []procInfo
	for _, line := range strings.Split(out, "\n") {
		m := psLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		start, err := time.ParseInLocation(psLayout, strings.Join(strings.Fields(m[2]), " "), time.Local)
		if err != nil {
			continue
		}
		procs = append(procs, procInfo{PID: pid, Start: start, Cmd: strings.TrimSpace(m[3])})
	}
	return procs, nil
}

// psLayout is ps -o lstart's time format (spaces collapsed).
const psLayout = "Mon Jan 2 15:04:05 2006"

// hostWrapperExe is the aimux binary the wrapper at path execs: the
// shell-quoted word after "exec " that writeHostWrapper wrote. A missing
// file is an error wrapping fs.ErrNotExist.
func hostWrapperExe(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the user's own host.sh
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, found := strings.CutPrefix(line, "exec '")
		if !found {
			continue
		}
		var b strings.Builder
		for {
			j := strings.IndexByte(rest, '\'')
			if j < 0 {
				return "", fmt.Errorf("%s: unterminated quote in the exec line", path)
			}
			b.WriteString(rest[:j])
			rest = rest[j+1:]
			if after, ok := strings.CutPrefix(rest, `\''`); ok { // writeHostWrapper's '\'' for a quote
				b.WriteByte('\'')
				rest = after
				continue
			}
			break
		}
		if !strings.HasPrefix(rest, " browser host") {
			return "", fmt.Errorf("%s: the exec line does not run browser host", path)
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("%s: no exec line (not written by aimux browser install)", path)
}

// portCheck is the port.7390 check, and whether nothing listens there (so
// aimux sandbox verify can run).
func portCheck(ctx context.Context, env frontEnv, procs []procInfo, psErr error) (doctorCheck, bool) {
	ref, _ := hostWrapperExe(filepath.Join(wingmanDir(env.Home), "host.sh")) // "" when absent: match by name
	const id = "port.7390"
	out, err := env.Run(ctx, nil, "lsof", "-nP", "-iTCP:7390", "-sTCP:LISTEN", "-t")
	var ec interface{ ExitCode() int }
	if err != nil && errors.As(err, &ec) && ec.ExitCode() == 1 && strings.TrimSpace(out) == "" {
		return ok(id, "free (the tool gateway worker is not running; aimux sandbox verify can use the port)"), true
	}
	if err != nil {
		return doctorCheck{ID: id, Status: checkWarn, Detail: "could not check: " + clipText(err.Error(), 200), Fix: "lsof -nP -iTCP:7390 -sTCP:LISTEN (install lsof if it is missing)"}, false
	}
	var serves, others []string
	var otherPIDs []string
	for _, f := range strings.Fields(out) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		desc := "unknown command"
		if psErr != nil {
			desc = "ps failed"
		}
		serve := false
		for _, p := range procs {
			if p.PID == pid {
				desc, serve = p.Cmd, p.aimuxServe(ref)
			}
		}
		if strings.Contains(desc, "mcp proxy gateway serve") {
			return ok(id, fmt.Sprintf("held by the tool gateway worker, pid %d (aimux sandbox verify uses it)", pid)), true
		}
		if serve {
			serves = append(serves, fmt.Sprintf("pid %d (%s)", pid, desc))
		} else {
			others = append(others, fmt.Sprintf("pid %d (%s)", pid, desc))
			otherPIDs = append(otherPIDs, f)
		}
	}
	if len(others) > 0 {
		return doctorCheck{ID: id, Status: checkFail,
			Detail: "held by " + strings.Join(others, ", ") + ", so the tool gateway worker cannot serve sandboxes there",
			Fix:    "kill " + strings.Join(otherPIDs, " ") + " (or stop that program), then: launchctl kickstart -k gui/$(id -u)/com.aimux.tool-gateway"}, false
	}
	return doctorCheck{ID: id, Status: checkWarn, Detail: "held by an older aimux remote serve, " + strings.Join(serves, ", ") + ", which still runs its own gateway", Fix: "restart aimux remote serve from the current aimux, then: aimux mcp proxy gateway install"}, false
}

func serveCheck(env frontEnv, procs []procInfo, psErr error) doctorCheck {
	const id = "wingman.serve"
	wrapper := filepath.Join(wingmanDir(env.Home), "host.sh")
	ref, err := hostWrapperExe(wrapper)
	if errors.Is(err, fs.ErrNotExist) {
		return ok(id, "Wingman not installed (no "+wrapper+"), so no serve binary to compare")
	}
	if err != nil {
		return doctorCheck{ID: id, Status: checkWarn, Detail: err.Error(), Fix: "aimux setup --extension-id <id> (rewrites the wrapper)"}
	}
	if psErr != nil {
		return doctorCheck{ID: id, Status: checkWarn, Detail: "could not list processes: " + clipText(psErr.Error(), 200), Fix: "ps -axww -o pid=,lstart=,command= | grep 'remote serve --local-only'"}
	}
	st, statErr := os.Stat(ref)
	var fine []string
	for _, p := range procs {
		if !p.aimuxServe(ref) || !p.localOnly() {
			continue
		}
		mine := strings.HasPrefix(p.Cmd, ref+" remote serve")
		switch {
		case !mine:
			return doctorCheck{ID: id, Status: checkWarn, Detail: fmt.Sprintf("pid %d runs %s, not %s, which %s execs", p.PID, strings.Fields(p.Cmd)[0], ref, wrapper),
				Fix: fmt.Sprintf("kill %d; the next Wingman panel starts serve from %s", p.PID, ref)}
		case statErr != nil:
			return doctorCheck{ID: id, Status: checkWarn, Detail: fmt.Sprintf("pid %d runs %s, which is gone: %v", p.PID, ref, statErr),
				Fix: "reinstall aimux at " + ref + " or run aimux setup --extension-id <id> from the aimux you use, then kill " + strconv.Itoa(p.PID)}
		case st.ModTime().Sub(p.Start) > 2*time.Second: // ps shows whole seconds
			return doctorCheck{ID: id, Status: checkWarn,
				Detail: fmt.Sprintf("pid %d started %s, before %s was replaced (%s): it runs the older binary", p.PID,
					p.Start.Format(time.DateTime), ref, st.ModTime().Format(time.DateTime)),
				Fix: fmt.Sprintf("kill %d; the next Wingman panel starts serve from %s", p.PID, ref)}
		default:
			fine = append(fine, fmt.Sprintf("pid %d runs the current %s", p.PID, ref))
		}
	}
	if len(fine) == 0 {
		return ok(id, "no Wingman serve running (the next panel starts one from "+ref+")")
	}
	return ok(id, strings.Join(fine, "; "))
}

func gatewayCheck(r sandboxReport) doctorCheck {
	l := gatewaySummary(r)
	c := doctorCheck{ID: "gateway", Status: l.Status, Detail: l.Detail}
	if len(l.Next) > 0 {
		c.Fix = l.Next[0]
	}
	return c
}

func sandboxChecks(r sandboxReport, portFree bool) []doctorCheck {
	var cs []doctorCheck
	switch r.Image.Status {
	case "present":
		cs = append(cs, ok("sandbox.image", r.Image.Name+" present"))
	case "missing":
		cs = append(cs, doctorCheck{ID: "sandbox.image", Status: checkFail, Detail: r.Image.Name + " missing", Fix: "aimux setup --yes (or aimux sandbox setup)"})
	default:
		cs = append(cs, doctorCheck{ID: "sandbox.image", Status: checkWarn, Detail: "unknown: " + r.Image.Error, Fix: "podman machine start, then aimux doctor"})
	}
	switch r.RunnerImage.Status {
	case "present":
		cs = append(cs, ok("sandbox.runner_image", r.RunnerImage.Name+" present"))
	case "missing":
		cs = append(cs, doctorCheck{ID: "sandbox.runner_image", Status: checkFail, Detail: r.RunnerImage.Name + " missing (cli and http-openapi tools run in it)",
			Fix: "aimux setup --yes (or aimux sandbox setup)"})
	default:
		cs = append(cs, doctorCheck{ID: "sandbox.runner_image", Status: checkWarn, Detail: "unknown: " + r.RunnerImage.Error, Fix: "podman machine start, then aimux doctor"})
	}
	if len(r.RunnerDropped) > 0 {
		var parts []string
		for _, d := range r.RunnerDropped {
			parts = append(parts, d.Source+" ("+d.Reason+")")
		}
		cs = append(cs, doctorCheck{ID: "sandbox.runner_sources", Status: checkWarn,
			Detail: "left out of the tool runner: " + strings.Join(parts, "; "), Fix: "aimux sandbox status, then fix what each reason names"})
	}
	if r.Gateway.Status != "reachable" {
		return cs // the provider and sandboxes cannot be asked; the gateway check names it
	}
	switch {
	case r.RunnerProvider.Status == "present":
		cs = append(cs, ok("sandbox.runner_provider", r.RunnerProvider.Name+" present"))
	case r.RunnerProvider.Status == "missing" && r.RunnerProvider.Needed:
		cs = append(cs, doctorCheck{ID: "sandbox.runner_provider", Status: checkFail,
			Detail: r.RunnerProvider.Name + " missing; an approved source needs it, so its tools are left out of the tool runner",
			Fix:    "aimux setup --yes (or aimux sandbox setup; it creates it when the GitHub token is in the secret store)"})
	case r.RunnerProvider.Status == "missing":
		cs = append(cs, ok("sandbox.runner_provider", r.RunnerProvider.Name+" not created; no approved source needs it"))
	default:
		cs = append(cs, doctorCheck{ID: "sandbox.runner_provider", Status: checkWarn, Detail: "unknown: " + r.RunnerProvider.Error, Fix: "openshell -g local provider list, then aimux doctor"})
	}
	switch r.Provider.Status {
	case "present":
		cs = append(cs, ok("sandbox.provider", r.Provider.Name+" present"))
	case "missing":
		cs = append(cs, doctorCheck{ID: "sandbox.provider", Status: checkFail, Detail: r.Provider.Name + " missing", Fix: "aimux setup --yes (or aimux sandbox setup)"})
	default:
		cs = append(cs, doctorCheck{ID: "sandbox.provider", Status: checkWarn, Detail: "unknown: " + r.Provider.Error, Fix: "openshell -g local provider list, then aimux doctor"})
	}
	var policies []doctorCheck
	fine := 0
	for _, p := range r.Profiles {
		id := "sandbox.policy." + p.Profile
		switch p.State {
		case stateStale:
			policies = append(policies, doctorCheck{ID: id, Status: checkWarn, Detail: p.Sandbox + " runs another policy than " + p.Profile + " compiles to now (stale)",
				Fix: staleFix(p.Profile, portFree)})
		case stateError:
			policies = append(policies, doctorCheck{ID: id, Status: checkFail, Detail: p.Profile + " does not compile: " + p.Error,
				Fix: "aimux tools status, then fix what the error names"})
		default:
			fine++
		}
	}
	if policies == nil {
		policies = []doctorCheck{ok("sandbox.policy", plural(fine, "sandboxed profile", "sandboxed profiles")+" current or created on first use")}
	}
	return append(cs, policies...)
}

// staleFix is how a stale sandbox policy gets applied: the next question
// in that profile (serve applies it before the turn), or for the work
// sandbox aimux sandbox verify, which ensures it now but needs port 7390
// free, so it is offered only then.
func staleFix(profile string, portFree bool) string {
	switch profile {
	case workProfile:
		if portFree {
			return "applies on the next Workspace question; or run aimux sandbox verify"
		}
		return "applies on the next Workspace question"
	case "research":
		return "applies on the next Web question"
	}
	return "applies on the next turn in " + profile
}

// gateway is env's tool gateway worker reporter.
func (env frontEnv) gateway() gatewayStatuser {
	if env.Gateway != nil {
		return env.Gateway
	}
	return toolgateway.ControlClient{Path: toolgateway.ControlSocketPath(env.Home)}
}
