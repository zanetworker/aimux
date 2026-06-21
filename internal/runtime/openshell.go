package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/zanetworker/aimux/internal/openshell"
)

// OpenShellRuntime manages a sandboxed runtime using NVIDIA OpenShell.
// Uses the shared openshell.Client for all CLI operations.
type OpenShellRuntime struct {
	name   string
	client *openshell.Client
	state  State
	policy SandboxConfig
}

// NewOpenShellRuntime creates an OpenShell runtime. If binary is
// empty, it defaults to "openshell". The name can be empty; Create()
// will assign one from the gateway.
func NewOpenShellRuntime(name, binary string) *OpenShellRuntime {
	return &OpenShellRuntime{
		name:   name,
		client: openshell.NewClient(openshell.Config{Binary: binary}),
		state:  StateStopped,
	}
}

func (o *OpenShellRuntime) Type() string { return "openshell" }
func (o *OpenShellRuntime) Name() string { return o.name }

// Create provisions a sandbox via the OpenShell gateway. If Name is empty,
// the gateway assigns one. The image field from CreateOpts is used if set.
func (o *OpenShellRuntime) Create(opts CreateOpts) error {
	return o.CreateWithProvider(opts, "")
}

// CreateWithProvider provisions a sandbox with an OpenShell provider attached
// for credential injection (e.g., "claude" injects ANTHROPIC_API_KEY).
func (o *OpenShellRuntime) CreateWithProvider(opts CreateOpts, provider string) error {
	o.state = StateCreating

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	createOpts := openshell.CreateOpts{
		Name:     o.name,
		Image:    opts.Image,
		Provider: provider,
		Env:      opts.Env,
	}

	name, err := o.client.CreateSandbox(ctx, createOpts)
	if err != nil {
		o.state = StateError
		return fmt.Errorf("openshell create sandbox: %w", err)
	}

	o.name = name
	o.state = StateRunning
	return nil
}

// Start is a no-op; sandboxes are running after Create.
func (o *OpenShellRuntime) Start() error {
	if o.state != StateRunning {
		return fmt.Errorf("sandbox %q is not running (state: %s)", o.name, o.state)
	}
	return nil
}

// Stop is a no-op; OpenShell sandboxes don't have a stop-without-delete.
func (o *OpenShellRuntime) Stop() error {
	return nil
}

// Delete removes the sandbox via the OpenShell gateway.
func (o *OpenShellRuntime) Delete() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := o.client.DeleteSandbox(ctx, o.name); err != nil {
		return fmt.Errorf("openshell delete sandbox %q: %w", o.name, err)
	}

	o.state = StateStopped
	return nil
}

// Status returns the current lifecycle state.
func (o *OpenShellRuntime) Status() RuntimeStatus {
	return RuntimeStatus{State: o.state}
}

// ExecPrefix returns the command prefix for executing inside this sandbox.
func (o *OpenShellRuntime) ExecPrefix() []string {
	return []string{o.client.Binary(), "sandbox", "exec", "-n", o.name, "--tty", "--"}
}

// ConnectCommand returns the command to open an interactive terminal session
// to this sandbox. Used by the spawn layer to create tmux sessions.
func (o *OpenShellRuntime) ConnectCommand() []string {
	return []string{o.client.Binary(), "sandbox", "connect", o.name}
}

// Attach opens an interactive terminal to the sandbox by exec'ing
// `openshell sandbox connect`. Replaces the current process.
func (o *OpenShellRuntime) Attach() error {
	if o.name == "" {
		return fmt.Errorf("no sandbox name set; call Create first")
	}
	cmd := o.ConnectCommand()
	binary, err := exec.LookPath(cmd[0])
	if err != nil {
		return fmt.Errorf("openshell binary not found: %w", err)
	}
	return execSyscall(binary, cmd, os.Environ())
}

// Client returns the underlying openshell client for direct operations.
func (o *OpenShellRuntime) Client() *openshell.Client {
	return o.client
}

// execSyscall is a variable so tests can override it. The default uses
// syscall.Exec to replace the process (used by Attach).
var execSyscall = defaultExecSyscall

func defaultExecSyscall(binary string, args []string, env []string) error {
	return fmt.Errorf("Attach must be called from an interactive terminal")
}

// --- PolicyEnforcer implementation ---

func (o *OpenShellRuntime) ApplyPolicy(cfg SandboxConfig) error {
	o.policy = cfg
	return nil
}

func (o *OpenShellRuntime) UpdatePolicy(cfg SandboxConfig) error {
	o.policy = cfg
	return nil
}

func (o *OpenShellRuntime) CurrentPolicy() SandboxConfig {
	return o.policy
}
