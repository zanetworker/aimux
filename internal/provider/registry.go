package provider

import (
	"path/filepath"

	"github.com/zanetworker/aimux/internal/config"
)

// Registry is the one place providers are built. Every frontend and command
// takes its providers from it, so the set and its order are defined once.
// Each provider is built once per Registry: Local, Enabled and ByName
// return the same instances.
type Registry struct {
	local []Provider
	k8s   Provider // nil unless cfg.Kubernetes.IsActive()
	cfg   config.Config
}

// NewRegistry builds the providers. home is the user's home directory that
// Claude Code's files sit under (~/.claude/projects, ~/.claude/sessions);
// "" leaves the provider's own defaults, which resolve the home directory
// when used. cfg decides which providers Enabled returns and whether the
// Kubernetes provider exists.
func NewRegistry(home string, cfg config.Config) *Registry {
	claude := &Claude{}
	if home != "" {
		claude.ProjectsDir = filepath.Join(home, ".claude", "projects")
		claude.RegistryDir = filepath.Join(home, ".claude", "sessions")
	}
	r := &Registry{local: []Provider{claude, &Codex{}}, cfg: cfg}
	if cfg.Kubernetes.IsActive() {
		r.k8s = NewK8s(K8sConfig{
			RedisURL:   cfg.Kubernetes.RedisURL,
			TeamID:     cfg.Kubernetes.TeamID,
			Namespace:  cfg.Kubernetes.Namespace,
			Kubeconfig: cfg.Kubernetes.Kubeconfig,
		})
	}
	return r
}

// Local is the local CLI providers, Claude then Codex, whatever the config
// says: process discovery, trace parsers and recent dirs outside the TUI
// use every one of them.
func (r *Registry) Local() []Provider {
	return append([]Provider(nil), r.local...)
}

// Enabled is the TUI's set: the local providers, then Kubernetes when it is
// active, keeping only those cfg.IsProviderEnabled allows, in that order.
func (r *Registry) Enabled() []Provider {
	all := r.Local()
	if r.k8s != nil {
		all = append(all, r.k8s)
	}
	var out []Provider
	for _, p := range all {
		if r.cfg.IsProviderEnabled(p.Name()) {
			out = append(out, p)
		}
	}
	return out
}

// ByName is the provider called name, enabled or not, or nil when the
// registry has none (Kubernetes exists only when active).
func (r *Registry) ByName(name string) Provider {
	for _, p := range r.local {
		if p.Name() == name {
			return p
		}
	}
	if r.k8s != nil && r.k8s.Name() == name {
		return r.k8s
	}
	return nil
}
