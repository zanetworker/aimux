package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/parts"
)

// upgradeService is a background service upgrade moves to its own copy of the program. The order is the order
// they are upgraded in.
type upgradeService struct{ id, name, label string }

var upgradeServices = func() []upgradeService {
	var managed []upgradeService
	for _, s := range parts.Services() {
		if s.Managed {
			managed = append(managed, upgradeService{s.ID, s.Name, s.Label})
		}
	}
	return managed
}()

// serviceProgram is a service's own copy of aimux, the file aimux upgrade maintains.
func serviceProgram(home, id string) string {
	return filepath.Join(home, ".aimux", "services", id, controller.ServiceProgram)
}

// proxyAgentBinary is the program the MCP proxy's job should run after an install run from cli: its own copy
// when the job already runs it, so an install does not undo what aimux upgrade did; otherwise cli.
func proxyAgentBinary(plistPath, own, cli string) string {
	if mcpproxy.ReadPlistBinary(plistPath) == own {
		return own
	}
	return cli
}

// notMovedService is a service upgrade leaves alone for now, with the command that updates it.
type notMovedService struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Update string `json:"update"`
}

var notMoved = func() []notMovedService {
	var left []notMovedService
	for _, s := range parts.Services() {
		if !s.Managed {
			left = append(left, notMovedService{s.ID, s.Name, s.Update})
		}
	}
	return left
}()

func newUpgradeCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "upgrade [service...]",
		Short: "Bring the background services to this build",
		Long: "Copy this aimux into each background service's own folder under ~/.aimux/services, restart the service\n" +
			"and wait until it answers on this build. A service that does not answer gets its previous program back.\n" +
			"A service already on this build and already running from its own folder is left running; the first\n" +
			"upgrade moves a service to its folder and restarts it whatever build it reports. Without names, every\n" +
			"service upgrade handles is done:\n" +
			"model-proxy, mcp-proxy and collector. Restarting the model proxy ends the turns in flight on it.\n" +
			"The exit code is 1 if a service was not upgraded.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := upgradeTargets(args)
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true
			// An interrupt cancels the wait; the controller then puts the previous program back before returning.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find the home directory for the services' folders: %w", err)
			}
			source, err := os.Executable()
			if err == nil {
				source, err = filepath.EvalSymlinks(source)
			}
			if err != nil {
				return fmt.Errorf("find this aimux program to copy: %w", err)
			}
			version := cmd.Root().Version
			cfg, _ := config.Load(config.DefaultPath())
			probes := defaultStatusProbes(defaultStatusEnv(home, version, cfg))
			servicesDir := filepath.Join(home, ".aimux", "services")
			var services []controller.UpgradeService
			for _, svc := range upgradeServices {
				if !slices.Contains(ids, svc.id) {
					continue
				}
				probe := probes[slices.IndexFunc(probes, func(p controller.StatusProbe) bool { return p.Name == svc.name })]
				var hold func() (func(), error)
				if svc.id == "model-proxy" {
					hold = func() (func(), error) { return holdPromotionLock(home) }
				}
				services = append(services, controller.UpgradeService{
					ID: svc.id, Name: svc.name, Dir: filepath.Join(servicesDir, svc.id), Probe: probe, Hold: hold,
					Job: launchdJob{agent: mcpproxy.Agent{
						Label:     svc.label,
						PlistPath: filepath.Join(home, "Library", "LaunchAgents", svc.label+".plist"),
						Run:       runLaunchctl, UID: os.Getuid(),
					}},
				})
			}
			results := controller.UpgradeServices(ctx, services, controller.UpgradeOptions{
				Source: source, Build: version, DryRun: dryRun,
			})
			return writeUpgrade(cmd.OutOrStdout(), version, servicesDir, results, args, dryRun, jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done; change nothing")
	return cmd
}

// upgradeTargets turns the names on the command line into service ids, in upgrade order. No names means all.
func upgradeTargets(args []string) ([]string, error) {
	var valid []string
	for _, svc := range upgradeServices {
		valid = append(valid, svc.id)
	}
	if len(args) == 0 {
		return valid, nil
	}
	for _, arg := range args {
		if slices.Contains(valid, arg) {
			continue
		}
		for _, other := range notMoved {
			if other.ID == arg {
				return nil, fmt.Errorf("upgrade does not move the %s yet; update it with: %s", other.Name, other.Update)
			}
		}
		return nil, fmt.Errorf("unknown service %q: choose from %s", arg, strings.Join(valid, ", "))
	}
	return slices.DeleteFunc(valid, func(id string) bool { return !slices.Contains(args, id) }), nil
}

// holdPromotionLock takes the lock scripts/model-proxy-promote uses (a folder made beside the CLI), so an upgrade
// and a promotion or rollback of the model proxy never swap its program at the same time. The folder outlives a
// killed upgrade; the error names it so it can be removed.
func holdPromotionLock(home string) (release func(), err error) {
	lock := filepath.Join(home, "go", "bin", ".aimux-model-proxy-promotion")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil { // #nosec G301 -- ~/go/bin is an ordinary bin folder
		return nil, err
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("a promotion or another upgrade of the model proxy is in progress (lock: %s)", lock)
		}
		return nil, err
	}
	return func() { _ = os.Remove(lock) }, nil
}

// launchdJob is a service's launchd job as upgrade needs it.
type launchdJob struct{ agent mcpproxy.Agent }

var loadedProgram = regexp.MustCompile(`(?m)^\s*program = (.+)$`)

// Program is the program launchd has loaded for the job, which is what a restart without a reload would run; the
// job file's when the job is not loaded. They differ when a move was interrupted between writing the file and
// reloading the job.
func (j launchdJob) Program() (string, error) {
	if _, err := os.Stat(j.agent.PlistPath); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if out, err := j.agent.Run("launchctl", "print", fmt.Sprintf("gui/%d/%s", j.agent.UID, j.agent.Label)); err == nil {
		if m := loadedProgram.FindSubmatch(out); m != nil {
			return strings.TrimSpace(string(m[1])), nil
		}
	}
	program := mcpproxy.ReadPlistBinary(j.agent.PlistPath)
	if program == "" {
		return "", fmt.Errorf("%s names no program", j.agent.PlistPath)
	}
	return program, nil
}

// Point makes the job file run program and reloads the job, also when the file already names it, so launchd runs
// what the file says. A reload that fails leaves the old file loaded.
func (j launchdJob) Point(program string) error {
	b, err := os.ReadFile(j.agent.PlistPath) // #nosec G304 -- the service's own job file
	if err != nil {
		return err
	}
	// With a Program key launchd runs that file, and the first argument is only the name the program sees.
	if bytes.Contains(b, []byte("<key>Program</key>")) {
		return fmt.Errorf("%s sets Program, which upgrade does not rewrite; remove it so ProgramArguments names the program", j.agent.PlistPath)
	}
	agent := j.agent
	agent.Binary = program
	_, switched, err := agent.SwitchBinary()
	if err != nil {
		return err
	}
	if !switched {
		return j.Restart()
	}
	return j.start()
}

// Restart reloads the job from its file and starts it. A reload, not a kickstart: it reads the file again, and
// launchd does not make it wait out the ten seconds it keeps between two starts of a loaded job.
func (j launchdJob) Restart() error {
	if err := j.agent.Restart(); err != nil {
		return err
	}
	return j.start()
}

// start asks launchd to run the loaded job. A job with RunAtLoad or KeepAlive is already running and this does
// nothing; one started by hand would otherwise stay stopped after a reload.
func (j launchdJob) start() error {
	target := fmt.Sprintf("gui/%d/%s", j.agent.UID, j.agent.Label)
	if out, err := j.agent.Run("launchctl", "kickstart", target); err != nil {
		return fmt.Errorf("start the reloaded job: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writeUpgrade prints the results and returns an error when a service was not upgraded. A service that is not
// installed counts only when it was asked for by name (requested).
func writeUpgrade(out io.Writer, version, servicesDir string, results []controller.UpgradeResult, requested []string, dryRun, asJSON bool) error {
	failed := 0
	for i, r := range results {
		switch {
		case r.Outcome == controller.UpgradeFailed:
			failed++
		case r.Outcome == controller.UpgradeNotInstalled && slices.Contains(requested, r.ID):
			failed++
			results[i].Problem = "it has no launchd job, so there is nothing to upgrade"
		}
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if results == nil {
			results = []controller.UpgradeResult{}
		}
		if err := enc.Encode(map[string]any{"build": version, "dry_run": dryRun, "services": results, "not_moved": notMoved}); err != nil {
			return fmt.Errorf("write upgrade JSON: %w", err)
		}
	} else if err := writeUpgradeTable(out, version, servicesDir, results, dryRun); err != nil {
		return fmt.Errorf("write upgrade table: %w", err)
	}
	if failed > 0 {
		return fmt.Errorf("%s", plural(failed, "service was not upgraded", "services were not upgraded"))
	}
	return nil
}

func writeUpgradeTable(out io.Writer, version, servicesDir string, results []controller.UpgradeResult, dryRun bool) error {
	var text strings.Builder
	note := ""
	if dryRun {
		note = " (dry run, nothing was changed)"
	}
	_, _ = fmt.Fprintf(&text, "aimux upgrade to build %s%s\n\n", upgradeBuild(version), note)
	table := tabwriter.NewWriter(&text, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "SERVICE\tRESULT\tBUILD")
	var notes []string
	for _, r := range results {
		result, build := string(r.Outcome), upgradeBuild(r.To)
		switch r.Outcome {
		case controller.UpgradeDone, controller.UpgradeRestarted:
			if dryRun {
				result = map[controller.UpgradeOutcome]string{
					controller.UpgradeDone: "would upgrade", controller.UpgradeRestarted: "would restart",
				}[r.Outcome]
			}
			if r.From != "" && r.From != r.To {
				build = upgradeBuild(r.From) + " -> " + build
			}
		case controller.UpgradeCurrent:
			result = "already current"
		case controller.UpgradeFailed:
			build = upgradeBuild(r.From)
		case controller.UpgradeNotInstalled:
			build = "-"
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\n", statusCell(r.Name, 12), result, statusCell(build, 24))
		own := filepath.Join(servicesDir, r.ID, controller.ServiceProgram)
		switch {
		case r.Outcome == controller.UpgradeFailed && r.Restored:
			notes = append(notes, r.Name+": "+r.Problem+"; the previous program runs again")
		case r.Problem != "":
			notes = append(notes, r.Name+": "+r.Problem)
		case r.MovedFrom != "" && dryRun:
			notes = append(notes, fmt.Sprintf("%s: its launchd job runs %s; it would now run %s", r.Name, r.MovedFrom, own))
		case r.MovedFrom != "":
			notes = append(notes, fmt.Sprintf("%s: its launchd job ran %s; it now runs %s", r.Name, r.MovedFrom, own))
		}
	}
	_ = table.Flush()
	for _, other := range notMoved {
		notes = append(notes, fmt.Sprintf("%s: not moved by upgrade yet; update it with: %s", other.Name, other.Update))
	}
	text.WriteString("\nNOTES\n")
	for _, n := range notes {
		text.WriteString(printable(n) + "\n")
	}
	_, err := io.WriteString(out, text.String())
	return err
}

// upgradeBuild is a version as the table shows it: the commit count when it has one.
func upgradeBuild(version string) string {
	if version == "" {
		return "-"
	}
	return shortVersion(controller.BuildNumber(version))
}
