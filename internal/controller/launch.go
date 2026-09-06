package controller

import (
	"fmt"
	"os/exec"
	"sort"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/spawn"
)

// Harness is the subset of Provider that the launch path needs.
// It represents the "brain" -- what agent to run and how to configure it.
// The name aligns with industry terminology: the harness is the component
// that calls the model, parses tool calls, and decides when to stop.
type Harness interface {
	Name() string
	SpawnCommand(dir, model, mode string) *exec.Cmd
	OTELEnv(endpoint string) string
}

// LaunchRequest captures all user-specified launch parameters.
// Frontends (TUI, Web, CLI) build this; the controller resolves it.
type LaunchRequest struct {
	Dir            string
	Model          string
	Mode           string
	Prompt         string
	Shell          string
	SessionManager string
	OTELEnabled    bool
	OTELEndpoint   string
	Runtime        string
	ContainerOpts  spawn.ContainerOpts
}

// LaunchSpec is the fully-resolved command ready for execution.
// The Environment (execution plane) can run this without knowing
// which harness produced it.
type LaunchSpec struct {
	Provider       string
	Cmd            *exec.Cmd
	Dir            string
	Shell          string
	SessionManager string
	EnvPrefix      string
	Runtime        string
	ContainerOpts  spawn.ContainerOpts
}

// LaunchResult carries information about a launched agent.
type LaunchResult struct {
	PID          int
	TmuxSession  string
	SandboxName  string
	OTELSessionID string
}

// BuildLaunchSpec resolves a LaunchRequest into a LaunchSpec using the harness.
// This is the single point where "brain config" (from the harness) meets
// "user intent" (from the request). The result is execution-plane-ready.
func BuildLaunchSpec(h Harness, req LaunchRequest) LaunchSpec {
	if h == nil {
		return LaunchSpec{Dir: req.Dir, Runtime: req.Runtime}
	}

	var cmd *exec.Cmd
	cmd = h.SpawnCommand(req.Dir, req.Model, req.Mode)

	if req.Prompt != "" && cmd != nil {
		switch h.Name() {
		case "codex":
			cmd.Args = append(cmd.Args, "--prompt", req.Prompt)
		default:
			cmd.Args = append(cmd.Args, req.Prompt)
		}
	}

	envPrefix := ""
	if req.OTELEnabled && req.OTELEndpoint != "" {
		envPrefix = h.OTELEnv(req.OTELEndpoint)
	}

	shell := req.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	sessionMgr := req.SessionManager
	if sessionMgr == "" {
		sessionMgr = "tmux"
	}

	return LaunchSpec{
		Provider:       h.Name(),
		Cmd:            cmd,
		Dir:            req.Dir,
		Shell:          shell,
		SessionManager: sessionMgr,
		EnvPrefix:      envPrefix,
		Runtime:        req.Runtime,
		ContainerOpts:  req.ContainerOpts,
	}
}

// ExecuteLocalLaunch runs a resolved LaunchSpec in the local environment.
// This replaces the three duplicated launch paths in app.go, main.go (Web),
// and main.go (CLI).
func ExecuteLocalLaunch(spec LaunchSpec) (LaunchResult, error) {
	if spec.Cmd == nil {
		return LaunchResult{}, fmt.Errorf("no command to execute for provider %q", spec.Provider)
	}

	if spec.Runtime == "container" {
		if err := spawn.LaunchInContainer(spec.Cmd, spec.Provider, spec.Dir, spec.Shell, spec.EnvPrefix, spec.ContainerOpts); err != nil {
			return LaunchResult{}, fmt.Errorf("container launch: %w", err)
		}
	} else {
		if err := spawn.Launch(spec.Cmd, spec.Provider, spec.Dir, spec.SessionManager, spec.Shell, spec.EnvPrefix); err != nil {
			return LaunchResult{}, fmt.Errorf("launch: %w", err)
		}
	}

	tmuxName := spawn.TmuxSessionName(spec.Provider, spec.Dir)
	return LaunchResult{TmuxSession: tmuxName}, nil
}

// ResolveLaunchRuntime maps an environment type to the runtime string
// used by the launch dispatch in app.go. This centralizes the mapping
// so both TUI and web frontends use the same logic.
func ResolveLaunchRuntime(env config.EnvironmentConfig) string {
	switch env.Type {
	case "openshell", "k8s":
		return "remote"
	default:
		return "local"
	}
}

// EnvironmentNames returns sorted environment names from a config map.
// "local" is always sorted first if present.
func EnvironmentNames(envs map[string]config.EnvironmentConfig) []string {
	if len(envs) == 0 {
		return nil
	}
	names := make([]string, 0, len(envs))
	for name := range envs {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "local" {
			return true
		}
		if names[j] == "local" {
			return false
		}
		return names[i] < names[j]
	})
	return names
}
