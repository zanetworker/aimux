package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/provider"
)

var (
	healthTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#5F87FF"))
	healthOKStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22C55E"))
	healthFailStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444"))
	healthDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
	healthLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E5E7EB"))
	healthSectionStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF")).
				Bold(true)
)

// HealthView renders the :health system status dashboard.
type HealthView struct {
	health provider.SystemHealth
	width  int
	height int
}

// NewHealthView creates a new health view.
func NewHealthView() *HealthView {
	return &HealthView{}
}

// SetHealth updates the health data to display.
func (v *HealthView) SetHealth(h provider.SystemHealth) {
	v.health = h
}

// SetSize sets the view dimensions.
func (v *HealthView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// View renders the health dashboard.
func (v *HealthView) View() string {
	var b strings.Builder

	b.WriteString(healthTitleStyle.Render("System Health"))
	b.WriteString("\n\n")

	// Group by kind.
	var locals, infras []provider.ProviderHealth
	for _, p := range v.health.Providers {
		if p.Kind == "infra" {
			infras = append(infras, p)
		} else {
			locals = append(locals, p)
		}
	}

	// Local providers.
	if len(locals) > 0 {
		b.WriteString(healthSectionStyle.Render("  Local Providers"))
		b.WriteString("\n")
		for _, p := range locals {
			b.WriteString("    ")
			name := healthLabelStyle.Render(fmt.Sprintf("%-10s", p.Name))
			if p.BinaryOK {
				ver := p.Version
				if ver == "" {
					ver = "?"
				}
				status := healthOKStyle.Render("✓")
				path := healthDimStyle.Render(p.BinaryPath)
				version := healthDimStyle.Render(ver)
				agents := ""
				if p.Agents > 0 {
					agents = healthOKStyle.Render(fmt.Sprintf("  %d active", p.Agents))
				} else {
					agents = healthDimStyle.Render("  0 agents")
				}
				b.WriteString(fmt.Sprintf("%s  %s  %s %s%s", name, status, path, version, agents))
			} else {
				status := healthFailStyle.Render("✗")
				b.WriteString(fmt.Sprintf("%s  %s  %s", name, status, healthDimStyle.Render("not installed")))
			}
			b.WriteString("\n")
		}
	}

	// Infra providers.
	for _, p := range infras {
		b.WriteString("\n")
		b.WriteString(healthSectionStyle.Render(fmt.Sprintf("  Infrastructure (%s)", p.Name)))
		b.WriteString("\n")

		if p.Infra == nil || !p.Infra.Configured {
			b.WriteString("    " + healthDimStyle.Render("not configured") + "\n")
			continue
		}
		h := p.Infra

		// Coordination.
		b.WriteString("    ")
		b.WriteString(healthLabelStyle.Render(fmt.Sprintf("%-15s", "Coordination:")))
		if h.CoordOK {
			b.WriteString(healthOKStyle.Render("✓ connected"))
		} else {
			msg := h.CoordErr
			if msg == "" {
				msg = "unreachable"
			}
			b.WriteString(healthFailStyle.Render("✗ " + msg))
		}
		b.WriteString("\n")

		// Compute.
		b.WriteString("    ")
		b.WriteString(healthLabelStyle.Render(fmt.Sprintf("%-15s", "Compute:")))
		if h.ComputeOK {
			b.WriteString(healthOKStyle.Render("✓ connected"))
			b.WriteString(healthDimStyle.Render(fmt.Sprintf("  %d workloads", len(h.Workloads))))
		} else {
			msg := h.ComputeErr
			if msg == "" {
				msg = "unreachable"
			}
			b.WriteString(healthFailStyle.Render("✗ " + msg))
		}
		b.WriteString("\n")

		// Workloads list.
		if h.ComputeOK && len(h.Workloads) > 0 {
			for _, w := range h.Workloads {
				b.WriteString("      " + healthDimStyle.Render("- "+w) + "\n")
			}
		}

		// Agent count for infra.
		if p.Agents > 0 {
			b.WriteString("    ")
			b.WriteString(healthLabelStyle.Render(fmt.Sprintf("%-15s", "Agents:")))
			b.WriteString(healthOKStyle.Render(fmt.Sprintf("%d active", p.Agents)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(healthDimStyle.Render("  Press Esc to return"))

	return b.String()
}
