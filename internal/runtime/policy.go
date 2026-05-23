package runtime

// PolicyEnforcer is implemented by runtimes that support sandboxing
// with network and filesystem access control. Currently only the
// OpenShell runtime implements this interface.
type PolicyEnforcer interface {
	// ApplyPolicy configures the sandbox with the given policy and
	// restarts enforcement.
	ApplyPolicy(cfg SandboxConfig) error

	// UpdatePolicy merges new rules into the running policy without a
	// full restart.
	UpdatePolicy(cfg SandboxConfig) error

	// CurrentPolicy returns the active sandbox configuration.
	CurrentPolicy() SandboxConfig
}

// SandboxConfig describes the security policy for a sandboxed runtime.
type SandboxConfig struct {
	Type       string        `yaml:"type"`       // "openshell", "gvisor", etc.
	Network    NetworkPolicy `yaml:"network"`
	Filesystem FSPolicy      `yaml:"filesystem"`
}

// HasNetworkPolicy returns true if any network restrictions are configured.
func (s SandboxConfig) HasNetworkPolicy() bool {
	return s.Network.DenyAll || len(s.Network.Rules) > 0 || len(s.Network.Groups) > 0
}

// NetworkPolicy defines network access rules for a sandbox.
type NetworkPolicy struct {
	DenyAll bool           `yaml:"deny_all"` // block all egress by default
	Rules   []NetworkRule  `yaml:"rules"`    // explicit allow/deny rules
	Groups  []string       `yaml:"groups"`   // named policy groups (e.g. "web-browsing")
}

// NetworkRule describes a single network access rule.
type NetworkRule struct {
	Name     string   `yaml:"name"`                // human-readable label
	Hosts    []string `yaml:"hosts,omitempty"`      // hostnames or CIDRs
	Ports    []int    `yaml:"ports,omitempty"`      // TCP/UDP port numbers
	Binaries []string `yaml:"binaries,omitempty"`   // process names allowed to connect
	Access   string   `yaml:"access"`               // "allow" or "deny"
}

// FSPolicy describes filesystem access rules for a sandbox.
type FSPolicy struct {
	ReadOnly  []string `yaml:"read_only"`  // paths with read-only access
	ReadWrite []string `yaml:"read_write"` // paths with read-write access
}
