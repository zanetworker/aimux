package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/parts"
)

// The readers the web dashboard is handed, so a page answers from the functions the commands print from:
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
