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

// RemoteHealthConfig carries enough info to check OpenShell gateway health
// without importing the config package.
type RemoteHealthConfig struct {
	Backend string // "openshell" or ""
	Gateway string // gateway endpoint URL
}

// GatherHealth collects health from all providers and an optional infra
// provider. Each local provider's binary is checked via exec.LookPath.
// Agent counts come from the most recent discovery results.
func GatherHealth(providers []Provider, infra InfraProvider, agentCounts map[string]int) SystemHealth {
	return GatherHealthWithRemote(providers, infra, agentCounts, RemoteHealthConfig{})
}

// GatherHealthWithRemote extends GatherHealth with OpenShell gateway status.
func GatherHealthWithRemote(providers []Provider, infra InfraProvider, agentCounts map[string]int, remote RemoteHealthConfig) SystemHealth {
	var sh SystemHealth

	for _, p := range providers {
		ph := ProviderHealth{
			Name:   p.Name(),
			Kind:   "local",
			Agents: agentCounts[p.Name()],
		}

		cmd := p.SpawnCommand(".", "", "")
		if cmd != nil && len(cmd.Args) > 0 {
			binary := cmd.Args[0]
			if path, err := exec.LookPath(binary); err == nil {
				ph.BinaryOK = true
				ph.BinaryPath = path
				ph.Version = getBinaryVersion(path)
			}
		}

		if infra != nil && p.Name() == infra.Name() {
			ph.Kind = "infra"
			h := infra.CheckHealth()
			ph.Infra = &h
		}

		sh.Providers = append(sh.Providers, ph)
	}

	if remote.Backend == "openshell" {
		sh.Providers = append(sh.Providers, checkOpenShellHealth(remote.Gateway))
	}

	return sh
}

func checkOpenShellHealth(gateway string) ProviderHealth {
	ph := ProviderHealth{
		Name: "openshell",
		Kind: "infra",
	}

	path, err := exec.LookPath("openshell")
	if err != nil {
		ph.Infra = &HealthStatus{
			Configured: true,
			CoordErr:   "openshell CLI not found in PATH",
		}
		return ph
	}
	ph.BinaryOK = true
	ph.BinaryPath = path
	ph.Version = getBinaryVersion(path)

	out, err := exec.Command("openshell", "status").CombinedOutput()
	if err != nil {
		ph.Infra = &HealthStatus{
			Configured: true,
			CoordErr:   "gateway unreachable",
		}
		return ph
	}

	output := strings.ToLower(string(out))
	connected := strings.Contains(output, "connected")

	if connected {
		// Get sandbox count
		listOut, _ := exec.Command("openshell", "sandbox", "list").CombinedOutput()
		lines := strings.Split(strings.TrimSpace(string(listOut)), "\n")
		sandboxCount := 0
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "NAME") && !strings.HasPrefix(line, "No sandbox") {
				sandboxCount++
			}
		}

		ph.Infra = &HealthStatus{
			Configured: true,
			CoordOK:    true,
			ComputeOK:  true,
		}
		if gateway != "" {
			ph.Infra.Workloads = []string{fmt.Sprintf("gateway: %s", gateway)}
		}
		if sandboxCount > 0 {
			ph.Infra.Workloads = append(ph.Infra.Workloads, fmt.Sprintf("%d sandbox(es) running", sandboxCount))
		}
		ph.Agents = sandboxCount
	} else {
		ph.Infra = &HealthStatus{
			Configured: true,
			CoordErr:   "gateway not connected",
		}
	}

	return ph
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
				fmt.Fprintf(&b, "  %-10s  OK  %s %s    %d agents\n", p.Name, p.BinaryPath, ver, p.Agents)
			} else {
				fmt.Fprintf(&b, "  %-10s  --  not installed\n", p.Name)
			}
		}
	}

	// Infra providers.
	for _, p := range infras {
		fmt.Fprintf(&b, "\nInfrastructure (%s)\n", p.Name)
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
			fmt.Fprintf(&b, "  Coordination:  FAIL  %s\n", msg)
		}

		// Compute layer.
		if h.ComputeOK {
			fmt.Fprintf(&b, "  Compute:       OK    %d workloads\n", len(h.Workloads))
			for _, w := range h.Workloads {
				fmt.Fprintf(&b, "    - %s\n", w)
			}
		} else {
			msg := h.ComputeErr
			if msg == "" {
				msg = "unreachable"
			}
			fmt.Fprintf(&b, "  Compute:       FAIL  %s\n", msg)
		}
	}

	return b.String()
}
