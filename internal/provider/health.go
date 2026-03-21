package provider

import (
	"fmt"
	"os/exec"
	"strings"
)

// ProviderHealth describes the health of a single provider.
type ProviderHealth struct {
	Name       string       // e.g. "claude", "k8s"
	Kind       string       // "local" or "infra"
	BinaryPath string       // resolved path to the binary (local only)
	BinaryOK   bool         // true if binary is found in PATH
	Version    string       // binary version string (local only)
	Agents     int          // number of currently active agents
	Infra      *HealthStatus // infra health details (infra only, nil for local)
}

// SystemHealth holds the health of all providers and infra.
type SystemHealth struct {
	Providers []ProviderHealth
}

// GatherHealth collects health from all providers and an optional infra
// provider. Each local provider's binary is checked via exec.LookPath.
// Agent counts come from the most recent discovery results.
func GatherHealth(providers []Provider, infra InfraProvider, agentCounts map[string]int) SystemHealth {
	var sh SystemHealth

	for _, p := range providers {
		ph := ProviderHealth{
			Name:   p.Name(),
			Kind:   "local",
			Agents: agentCounts[p.Name()],
		}

		// Derive binary path from SpawnCommand.
		cmd := p.SpawnCommand(".", "", "")
		if cmd != nil && len(cmd.Args) > 0 {
			binary := cmd.Args[0]
			if path, err := exec.LookPath(binary); err == nil {
				ph.BinaryOK = true
				ph.BinaryPath = path
				ph.Version = getBinaryVersion(path)
			}
		}

		// If this is the infra provider, mark it as such and include
		// infra health. Skip adding it again below.
		if infra != nil && p.Name() == infra.Name() {
			ph.Kind = "infra"
			h := infra.CheckHealth()
			ph.Infra = &h
		}

		sh.Providers = append(sh.Providers, ph)
	}

	return sh
}

// getBinaryVersion runs "<binary> --version" and returns the first line.
func getBinaryVersion(binaryPath string) string {
	out, err := exec.Command(binaryPath, "--version").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	// Take first line only.
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	// Trim common prefixes like "claude-code v2.1.72" → "v2.1.72"
	if parts := strings.Fields(line); len(parts) >= 2 {
		for _, p := range parts {
			if strings.HasPrefix(p, "v") || strings.HasPrefix(p, "V") || (len(p) > 0 && p[0] >= '0' && p[0] <= '9') {
				return p
			}
		}
	}
	if len(line) > 30 {
		line = line[:30]
	}
	return line
}

// FormatHealth renders SystemHealth as a human-readable string.
// This is used by the TUI health view but lives here so it can be
// tested without TUI dependencies.
func FormatHealth(sh SystemHealth) string {
	var b strings.Builder

	// Group by kind.
	var locals, infras []ProviderHealth
	for _, p := range sh.Providers {
		if p.Kind == "infra" {
			infras = append(infras, p)
		} else {
			locals = append(locals, p)
		}
	}

	// Local providers.
	if len(locals) > 0 {
		b.WriteString("Local Providers\n")
		for _, p := range locals {
			if p.BinaryOK {
				ver := p.Version
				if ver == "" {
					ver = "unknown version"
				}
				b.WriteString(fmt.Sprintf("  %-10s  OK  %s %s    %d agents\n", p.Name, p.BinaryPath, ver, p.Agents))
			} else {
				b.WriteString(fmt.Sprintf("  %-10s  --  not installed\n", p.Name))
			}
		}
	}

	// Infra providers.
	for _, p := range infras {
		b.WriteString(fmt.Sprintf("\nInfrastructure (%s)\n", p.Name))
		if p.Infra == nil {
			b.WriteString("  not configured\n")
			continue
		}
		h := p.Infra
		if !h.Configured {
			b.WriteString("  not configured\n")
			continue
		}

		// Coordination layer.
		if h.CoordOK {
			b.WriteString("  Coordination:  OK\n")
		} else {
			msg := h.CoordErr
			if msg == "" {
				msg = "unreachable"
			}
			b.WriteString(fmt.Sprintf("  Coordination:  FAIL  %s\n", msg))
		}

		// Compute layer.
		if h.ComputeOK {
			b.WriteString(fmt.Sprintf("  Compute:       OK    %d workloads\n", len(h.Workloads)))
			for _, w := range h.Workloads {
				b.WriteString(fmt.Sprintf("    - %s\n", w))
			}
		} else {
			msg := h.ComputeErr
			if msg == "" {
				msg = "unreachable"
			}
			b.WriteString(fmt.Sprintf("  Compute:       FAIL  %s\n", msg))
		}
	}

	return b.String()
}
