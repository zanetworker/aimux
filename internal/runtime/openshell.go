package runtime

import "fmt"

// OpenShellRuntime represents a sandboxed runtime using NVIDIA OpenShell.
// All lifecycle methods are stubs that return "not yet implemented" errors.
// The ExecPrefix method returns the real command structure so callers can
// plan ahead even before the runtime is fully wired.
type OpenShellRuntime struct {
	name   string
	binary string
	policy SandboxConfig
}

// NewOpenShellRuntime creates an OpenShell runtime stub. If binary is
// empty, it defaults to "openshell".
func NewOpenShellRuntime(name, binary string) *OpenShellRuntime {
	if binary == "" {
		binary = "openshell"
	}
	return &OpenShellRuntime{name: name, binary: binary}
}

func (o *OpenShellRuntime) Type() string { return "openshell" }
func (o *OpenShellRuntime) Name() string { return o.name }

func (o *OpenShellRuntime) Create(_ CreateOpts) error {
	return fmt.Errorf("openshell runtime not yet implemented")
}

func (o *OpenShellRuntime) Start() error {
	return fmt.Errorf("openshell runtime not yet implemented")
}

func (o *OpenShellRuntime) Stop() error {
	return fmt.Errorf("openshell runtime not yet implemented")
}

func (o *OpenShellRuntime) Delete() error {
	return fmt.Errorf("openshell runtime not yet implemented")
}

func (o *OpenShellRuntime) Status() RuntimeStatus {
	return RuntimeStatus{State: StateStopped, Message: "not yet implemented"}
}

// ExecPrefix returns the real OpenShell exec command structure even
// though the runtime itself is not yet implemented. This allows the
// spawn layer to prepare the correct command shape.
func (o *OpenShellRuntime) ExecPrefix() []string {
	return []string{o.binary, "sandbox", "exec", "-n", o.name, "--tty", "--"}
}

func (o *OpenShellRuntime) Attach() error {
	return fmt.Errorf("openshell runtime not yet implemented")
}

// --- PolicyEnforcer implementation (stub) ---

func (o *OpenShellRuntime) ApplyPolicy(cfg SandboxConfig) error {
	o.policy = cfg
	return fmt.Errorf("openshell runtime not yet implemented")
}

func (o *OpenShellRuntime) UpdatePolicy(cfg SandboxConfig) error {
	o.policy = cfg
	return fmt.Errorf("openshell runtime not yet implemented")
}

func (o *OpenShellRuntime) CurrentPolicy() SandboxConfig {
	return o.policy
}
