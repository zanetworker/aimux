package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/parts"
)

// The readers the web dashboard and the TUI are handed, so a page answers from the functions the commands print from:
// ServicesReader is `aimux status`, ProxyDoctorReader `aimux model proxy doctor`, EnginesReader
// `aimux engine list`. Each reads only.

// ServicesReader reads the background services on this machine: the document `aimux status --json` prints. The
// config file is read on each call, as the command reads it on each run, so a collector moved to another port is
// found there.
func ServicesReader(home, version string) func(context.Context) (any, error) {
	return func(ctx context.Context) (any, error) {
		if home == "" {
			return nil, errors.New("the home folder is not known, so the services cannot be found")
		}
		cfg, _ := config.Load(config.DefaultPath())
		return statusDocument(controller.ServicesOverview(ctx, version, defaultStatusProbes(defaultStatusEnv(home, version, cfg)), 2*time.Second)), nil
	}
}

// ProxyDoctorReader diagnoses the model proxy on the port its installed job starts it on. The report leaves out
// the finding about the shell: the process serving a page is not one of the owner's terminals.
func ProxyDoctorReader(home, version string) func(context.Context) (controller.ProxyReport, error) {
	return func(ctx context.Context) (controller.ProxyReport, error) {
		if home == "" {
			return controller.ProxyReport{}, errors.New("the home folder is not known, so the model proxy cannot be found")
		}
		label := parts.MustService("model-proxy").Label
		port := parts.InstalledProxyPort(filepath.Join(home, "Library", "LaunchAgents", label+".plist"), modelproxy.StablePort)
		deps := doctorDeps{
			key:    func(ctx context.Context) (string, error) { return keyring.Stored(modelproxy.KeyService).Lookup(ctx) },
			getenv: os.Getenv, home: home, cliBuild: version, now: time.Now,
		}
		return proxyDoctorReport(ctx, port, deps, false)
	}
}

// EnginesReader reads the engines, the roles and the subagent definitions under home and, when cwd is named,
// under that folder: the document `aimux engine list --json` prints when run there.
func EnginesReader(home, cwd string) func(context.Context) (any, error) {
	return func(ctx context.Context) (any, error) {
		if home == "" {
			return nil, errors.New("the home folder is not known, so the config file cannot be found")
		}
		read, err := readEngines(ctx, home, cwd)
		if err != nil {
			return nil, err
		}
		return enginesDocument(read.rows, read.port, read.roles), nil
	}
}

// SystemReader reads the whole machine at once for a frontend that shows it on one screen, as the TUI does: the
// services, the proxy's diagnosis, the engines and roles, and the last day's model calls by role, each from the
// function its command prints from. The four are read side by side; one that fails says why and leaves the rest.
func SystemReader(home, version, cwd string) func(context.Context) controller.SystemSnapshot {
	doctor := ProxyDoctorReader(home, version)
	return func(ctx context.Context) controller.SystemSnapshot {
		snap := controller.SystemSnapshot{At: time.Now()}
		var reads sync.WaitGroup
		reads.Go(func() {
			cfg, _ := config.Load(config.DefaultPath())
			snap.Services = controller.ServicesOverview(ctx, version, defaultStatusProbes(defaultStatusEnv(home, version, cfg)), 2*time.Second)
		})
		reads.Go(func() {
			report, err := doctor(ctx)
			if err != nil {
				snap.DoctorErr = err.Error()
			}
			snap.Doctor = report
		})
		reads.Go(func() {
			read, err := readEngines(ctx, home, cwd)
			if err != nil {
				snap.EnginesErr = err.Error()
				return
			}
			snap.EnginesPort, snap.Engines, snap.Roles, snap.Subagents = read.port, read.rows, read.roles.Roles, read.roles.Subagents
		})
		reads.Go(func() {
			// The ledger is files on disk and its reading takes no context, so it is read to the side: a disk
			// that stalls costs the meter, not the whole screen.
			type metered struct {
				report controller.MeterReport
				err    error
			}
			done := make(chan metered, 1)
			at := snap.At
			go func() {
				opts, err := controller.ParseMeterOptions("24h", string(controller.MeterByRole), "", at)
				var report controller.MeterReport
				if err == nil {
					report, err = controller.Meter(filepath.Join(home, ".aimux", "ledger"), opts)
				}
				done <- metered{report, err}
			}()
			select {
			case m := <-done:
				snap.Meter = m.report
				if m.err != nil {
					snap.MeterErr = m.err.Error()
				}
			case <-ctx.Done():
				snap.MeterErr = "the ledger took too long to read"
			}
		})
		reads.Wait()
		return snap
	}
}
