package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/parts"
)

// launchdPartJob is a service's launchd job as `aimux setup --apply` needs it.
type launchdPartJob struct{ job launchdJob }

func (j launchdPartJob) Read() ([]byte, error) {
	return os.ReadFile(j.job.agent.PlistPath) // #nosec G304 -- the service's own job file
}

// Write replaces the job file through a temporary file beside it, so launchd never reads half of one.
func (j launchdPartJob) Write(data []byte) error {
	path := j.job.agent.PlistPath
	temp, err := os.CreateTemp(filepath.Dir(path), ".plist-tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// The mode the file has: a job file kept private stays private.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(temp.Name(), mode); err != nil { // #nosec G302 -- the mode the job file already had
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Reload makes launchd run what the file says, started afresh. A job that Stop disabled is allowed again first.
func (j launchdPartJob) Reload() error {
	agent := j.job.agent
	// "enable" on a job that is not disabled does nothing; its failure would show in the reload that follows.
	_, _ = agent.Run("launchctl", "enable", fmt.Sprintf("gui/%d/%s", agent.UID, agent.Label))
	return j.job.Restart()
}

// KeptOff reports whether launchd has been told not to load the job.
func (j launchdPartJob) KeptOff() (bool, error) {
	agent := j.job.agent
	return launchdKeptOff(agent.Run, agent.UID, agent.Label)
}

var launchdDisabled = regexp.MustCompile(`"([^"]+)"\s*=>\s*(disabled|true)`)

// launchdKeptOff reads launchd's list of jobs it has been told not to load.
func launchdKeptOff(run func(string, ...string) ([]byte, error), uid int, label string) (bool, error) {
	out, err := run("launchctl", "print-disabled", fmt.Sprintf("gui/%d", uid))
	if err != nil {
		return false, fmt.Errorf("ask launchd which jobs are kept off: %w", err)
	}
	for _, match := range launchdDisabled.FindAllSubmatch(out, -1) {
		if string(match[1]) == label {
			return true, nil
		}
	}
	return false, nil
}

// Stop unloads the job and tells launchd not to load it again, at a crash or at the next login, until Reload.
// Disabled first: a job unloaded and still allowed could be brought back before it is marked.
func (j launchdPartJob) Stop() error {
	agent := j.job.agent
	target := fmt.Sprintf("gui/%d/%s", agent.UID, agent.Label)
	if out, err := agent.Run("launchctl", "disable", target); err != nil {
		return fmt.Errorf("tell launchd to keep the job off: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	out, bootErr := agent.Run("launchctl", "bootout", target)
	// bootout returns before launchd has let go of the job; stopped means it is gone.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		loaded, err := agent.Loaded()
		if err == nil && !loaded {
			return nil
		}
		if !time.Now().Before(deadline) {
			if bootErr != nil {
				return fmt.Errorf("unload the job: %w (%s)", bootErr, strings.TrimSpace(string(out)))
			}
			return errors.New("launchd still has the job ten seconds after it was told to unload it")
		}
	}
}

func (j launchdPartJob) Loaded() (bool, error) { return j.job.agent.Loaded() }

// partServices are the services aimux manages, with their launchd jobs and status probes.
func partServices(home, version string, cfg config.Config) []controller.PartService {
	env := defaultStatusEnv(home, version, cfg)
	probes := defaultStatusProbes(env)
	var services []controller.PartService
	for _, service := range parts.Services() {
		at := slices.IndexFunc(probes, func(p controller.StatusProbe) bool { return p.Name == service.Name })
		if !service.Managed || at < 0 {
			continue
		}
		var hold func() (func(), error)
		var probeFor func([]string) controller.StatusProbe
		if service.ID == "model-proxy" {
			hold = func() (func(), error) { return holdPromotionLock(home) }
			// The model proxy listens where its arguments say, which the settings may have just changed.
			probeFor = func(args []string) controller.StatusProbe {
				at := env
				at.modelAddr = fmt.Sprintf("127.0.0.1:%d", parts.ProxyPort(args, service.Port))
				return at.probe(service.Name, at.modelAddr, service.Label, service.Update, at.readModelStatus)
			}
		}
		services = append(services, controller.PartService{
			Service: service, Dir: filepath.Join(home, ".aimux", "services", service.ID), Probe: probes[at], ProbeFor: probeFor, Hold: hold,
			CLIBuild: version,
			Job: launchdPartJob{job: launchdJob{agent: mcpproxy.Agent{
				Label:     service.Label,
				PlistPath: filepath.Join(home, "Library", "LaunchAgents", service.Label+".plist"),
				Run:       runLaunchctl, UID: os.Getuid(),
			}}},
		})
	}
	return services
}

// setupPartsOpts is how `aimux setup --check` or `--apply` was asked for.
type setupPartsOpts struct {
	apply, yes, asJSON bool
	// onlyPart, when set, limits what is planned and applied to that part. A command about one part does not
	// carry out what the file says about another: a stop left pending there is not a side effect of this one.
	onlyPart string
	// ask puts a yes-or-no question to the owner; nil when there is nobody to ask.
	ask func(prompt string) bool
}

// errNeedsYes is a restart with nobody to ask about it: no service was touched.
var errNeedsYes = errors.New("not applied")

// errNotAgreed is the owner answering no to a restart: no service was touched.
var errNotAgreed = errors.New("no service was changed")

// errPartsWouldChange is --check finding something to do: exit 1, as a script can test.
var errPartsWouldChange = errors.New("the services do not match the settings yet; aimux setup --apply makes them")

// runSetupParts compares the services with the parts section of the config file at configPath and, with apply,
// makes them match. It changes nothing unless apply is set and every restart was agreed to.
func runSetupParts(ctx context.Context, configPath string, services []controller.PartService, o setupPartsOpts, out io.Writer) error {
	s, err := parts.Load(configPath)
	if err != nil {
		return err
	}
	if problems := s.Validate(); len(problems) > 0 {
		if o.asJSON {
			lines := make([]string, len(problems))
			for i, p := range problems {
				lines[i] = p.String()
			}
			_ = writeJSON(out, map[string]any{"schema_version": 1, "problems": lines})
		} else {
			_, _ = fmt.Fprintf(out, "The parts section of %s has %s:\n", configPath, plural(len(problems), "problem", "problems"))
			for _, p := range problems {
				_, _ = fmt.Fprintf(out, "  %s\n", p)
			}
		}
		return errors.New("nothing was changed: fix the settings first")
	}
	planFor := func(s parts.Settings) []controller.JobChange {
		plan := controller.PlanParts(services, s)
		if o.onlyPart != "" {
			plan = slices.DeleteFunc(plan, func(c controller.JobChange) bool { return c.Part != o.onlyPart })
		}
		return plan
	}
	plan := planFor(s)
	pending := slices.ContainsFunc(plan, func(c controller.JobChange) bool {
		return c.Action == controller.ActionAdopt || c.Action == controller.ActionChange || c.Action == controller.ActionStart || c.Action == controller.ActionStop
	})
	if !o.apply || !pending {
		switch {
		case o.asJSON && o.apply:
			// The same shape as an apply that did something: every line, with how it went.
			results := []controller.JobResult{}
			for _, line := range plan {
				outcome := controller.ApplyUnchanged
				if line.Action == controller.ActionSkip {
					outcome = controller.ApplySkipped
				}
				results = append(results, controller.JobResult{JobChange: line, Outcome: outcome, Detail: line.Note})
			}
			if err := writeJSON(out, map[string]any{"schema_version": 1, "results": results}); err != nil {
				return err
			}
		case o.asJSON:
			if err := writeJSON(out, map[string]any{"schema_version": 1, "stated": s.Stated, "plan": emptyIfNil(plan), "changes": pending}); err != nil {
				return err
			}
		default:
			writePartsPlan(out, configPath, s, plan, pending, o.apply)
		}
		if pending {
			return errPartsWouldChange
		}
		if o.apply {
			return leftAlone(plan)
		}
		return nil
	}
	var restarting, stopping []string
	for _, line := range plan {
		switch {
		case line.Action == controller.ActionStop:
			stopping = append(stopping, parts.MustService(line.Service).Name)
		case line.Restarts:
			restarting = append(restarting, parts.MustService(line.Service).Name)
		}
	}
	if !o.asJSON {
		writePartsPlan(out, configPath, s, plan, pending, true)
	}
	if len(restarting)+len(stopping) > 0 && !o.yes {
		var doing []string
		if len(restarting) > 0 {
			doing = append(doing, "restart the "+strings.Join(restarting, " and the "))
		}
		if len(stopping) > 0 {
			doing = append(doing, "stop the "+strings.Join(stopping, " and the "))
		}
		what := strings.Join(doing, ", and ")
		question := fmt.Sprintf("\n%s now? Turns in flight end", strings.ToUpper(what[:1])+what[1:])
		if len(stopping) > 0 {
			question += ", and sessions that go through a stopped service get no answer until it is turned on again"
		}
		question += ". [y/N] "
		if o.ask == nil {
			return fmt.Errorf("%w: this would %s; run aimux setup --apply --yes to agree", errNeedsYes, what)
		}
		if !o.ask(question) {
			return errNotAgreed
		}
		// The question may have stood open a while: what is applied is the file as it is now, and only when
		// that is still the plan that was shown.
		if s, err = parts.Load(configPath); err != nil {
			return err
		}
		if len(s.Validate()) > 0 || !samePlan(plan, planFor(s)) {
			return fmt.Errorf("%w; nothing was changed. Run it again", controller.ErrStalePlan)
		}
	}
	results, applyErr := controller.ApplyParts(ctx, services, s, plan, controller.ApplyOptions{})
	if o.asJSON {
		if err := writeJSON(out, map[string]any{"schema_version": 1, "results": results}); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintln(out)
		for _, r := range results {
			if r.Outcome == controller.ApplySkipped || r.Outcome == controller.ApplyUnchanged {
				continue
			}
			line := fmt.Sprintf("%s: %s", parts.MustService(r.Service).Name, r.Outcome)
			if r.Detail != "" {
				line += " (" + r.Detail + ")"
			}
			_, _ = fmt.Fprintln(out, line)
		}
	}
	if errors.Is(applyErr, controller.ErrStalePlan) {
		return fmt.Errorf("%w; nothing was changed. Run it again", applyErr)
	}
	if applyErr != nil {
		return applyErr
	}
	return leftAlone(plan)
}

// errLeftAlone is an apply after which a service still does not match the settings, because it could not be
// managed as things are. An apply that leaves that behind does not report success.
var errLeftAlone = errors.New("a service does not match the settings and was left alone")

func leftAlone(plan []controller.JobChange) error {
	for _, line := range plan {
		if line.Action == controller.ActionSkip {
			return fmt.Errorf("%w: the %s: %s", errLeftAlone, parts.MustService(line.Service).Name, line.Note)
		}
	}
	return nil
}

// samePlan reports whether two plans say the same thing to the owner.
func samePlan(a, b []controller.JobChange) bool {
	return slices.EqualFunc(a, b, func(x, y controller.JobChange) bool {
		return x.Part == y.Part && x.Service == y.Service && x.Action == y.Action && slices.Equal(x.Was, y.Was) && slices.Equal(x.Now, y.Now)
	})
}

func emptyIfNil(plan []controller.JobChange) []controller.JobChange {
	if plan == nil {
		return []controller.JobChange{}
	}
	return plan
}

func writePartsPlan(out io.Writer, configPath string, s parts.Settings, plan []controller.JobChange, pending, applying bool) {
	if !s.Stated {
		_, _ = fmt.Fprintf(out, "%s has no parts section, so aimux manages no service: each runs as it was installed.\n", configPath)
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "PART\tSERVICE\tWILL\tDETAIL")
	for _, line := range plan {
		will, detail := line.Action, line.Note
		switch line.Action {
		case controller.ActionNone:
			will, detail = "nothing", "runs what the settings state"
		case controller.ActionAdopt:
			will, detail = "be managed", "as it runs now; no restart"
		case controller.ActionChange:
			will, detail = "restart", "from: "+printableArgs(line.Was)+"\n\t\t\tto:   "+printableArgs(line.Now)
		case controller.ActionStart:
			will, detail = "start", "its job is as the settings state, and it is not running"
		case controller.ActionStop:
			will, detail = "stop", "the settings turn this part off; it stays off until turned on again"
		case controller.ActionSkip:
			will = "be left alone"
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", line.Part, parts.MustService(line.Service).Name, will, detail)
	}
	_ = table.Flush()
	leftAlone := slices.ContainsFunc(plan, func(c controller.JobChange) bool { return c.Action == controller.ActionSkip })
	switch {
	case !pending && leftAlone:
		// Not "nothing to change": the settings may ask for something that cannot be done as things are.
		_, _ = fmt.Fprintln(out, "\nNothing can be changed as things are: see what is left alone, and why, above.")
	case !pending:
		_, _ = fmt.Fprintln(out, "\nNothing to change.")
	case !applying:
		_, _ = fmt.Fprintln(out, "\nNothing was changed. To do this: aimux setup --apply")
	}
}

// printableArgs is a job's arguments as a line. A key reference is a reference, never a key, so it is safe to show.
func printableArgs(args []string) string {
	shown := make([]string, len(args))
	for i, arg := range args {
		shown[i] = printable(arg)
	}
	return strings.Join(shown, " ")
}

// askOn reads yes-or-no answers from in, echoing the question to out.
func askOn(in io.Reader, out io.Writer) func(string) bool {
	reader := bufio.NewReader(in)
	return func(prompt string) bool { return ask(reader, out, prompt, false) }
}
