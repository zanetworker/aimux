package views

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/claudetopus/internal/model"
)

// Column widths for the instance table.
const (
	colPID    = 8
	colStatus = 10
	colModel  = 14
	colProject = 20
	colPerm   = 10
	colMem    = 8
	colCostW  = 8
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB"))
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("#374151"))
	activeIcon    = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	idleIcon      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	waitingIcon   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	mutedIcon     = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
)

// InstancesView renders the main instance table.
type InstancesView struct {
	instances   []model.Instance
	cursor      int
	selectedPID int // track selection by PID across refreshes
	width       int
	height      int
	filter      string
}

// NewInstancesView creates a new InstancesView.
func NewInstancesView() *InstancesView {
	return &InstancesView{}
}

// SetInstances updates the list of instances with stable sort order.
// Preserves cursor position by tracking the selected PID across refreshes.
func (v *InstancesView) SetInstances(instances []model.Instance) {
	// Sort by PID for stable ordering
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].PID < instances[j].PID
	})
	v.instances = instances

	// Restore cursor to the same PID if it still exists
	if v.selectedPID != 0 {
		f := v.filtered()
		for i, inst := range f {
			if inst.PID == v.selectedPID {
				v.cursor = i
				return
			}
		}
	}
	// PID gone or no previous selection — clamp cursor
	if v.cursor >= len(v.filtered()) {
		v.cursor = max(0, len(v.filtered())-1)
	}
}

// SetSize sets the available width and height.
func (v *InstancesView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// SetFilter sets a filter string for instances.
func (v *InstancesView) SetFilter(f string) {
	v.filter = f
	v.cursor = 0
}

// Selected returns the currently selected instance, or nil.
func (v *InstancesView) Selected() *model.Instance {
	f := v.filtered()
	if v.cursor >= 0 && v.cursor < len(f) {
		return &f[v.cursor]
	}
	return nil
}

// Cursor returns the current cursor position.
func (v *InstancesView) Cursor() int {
	return v.cursor
}

// Update handles key messages for navigation.
func (v *InstancesView) Update(msg tea.Msg) {
	f := v.filtered()
	if len(f) == 0 {
		return
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if v.cursor < len(f)-1 {
				v.cursor++
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
			}
		case "g":
			v.cursor = 0
		case "G":
			v.cursor = len(f) - 1
		}
	}
	// Track selected PID for cursor preservation across refreshes
	if v.cursor >= 0 && v.cursor < len(f) {
		v.selectedPID = f[v.cursor].PID
	}
}

// View renders the instance table.
func (v *InstancesView) View() string {
	var b strings.Builder

	// Header
	header := fmt.Sprintf("%-*s %-*s %-*s %-*s %-*s %-*s %-*s",
		colPID, "PID",
		colStatus, "STATUS",
		colModel, "MODEL",
		colProject, "PROJECT",
		colPerm, "PERM",
		colMem, "MEM",
		colCostW, "COST",
	)
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")

	f := v.filtered()
	if len(f) == 0 {
		b.WriteString(mutedIcon.Render("  No instances found."))
		return b.String()
	}

	// Determine visible range based on height (reserve 2 for header + border).
	visibleHeight := v.height - 2
	if visibleHeight < 1 {
		visibleHeight = len(f)
	}
	start := 0
	if v.cursor >= visibleHeight {
		start = v.cursor - visibleHeight + 1
	}
	end := start + visibleHeight
	if end > len(f) {
		end = len(f)
	}

	for idx := start; idx < end; idx++ {
		inst := f[idx]
		icon := v.renderStatusIcon(inst.Status)
		row := fmt.Sprintf("%-*d %s %-*s %-*s %-*s %-*s %-*s %-*s",
			colPID, inst.PID,
			icon,
			colStatus-2, inst.Status.String(),
			colModel, inst.ShortModel(),
			colProject, truncate(inst.ShortProject(), colProject),
			colPerm, truncate(inst.PermissionMode, colPerm),
			colMem, inst.FormatMemory(),
			colCostW, inst.FormatCost(),
		)
		if idx == v.cursor {
			b.WriteString(selectedStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (v *InstancesView) renderStatusIcon(s model.Status) string {
	icon := s.Icon()
	switch s {
	case model.StatusActive:
		return activeIcon.Render(icon)
	case model.StatusIdle:
		return idleIcon.Render(icon)
	case model.StatusWaitingPermission:
		return waitingIcon.Render(icon)
	default:
		return mutedIcon.Render(icon)
	}
}

func (v *InstancesView) filtered() []model.Instance {
	if v.filter == "" {
		return v.instances
	}
	f := strings.ToLower(v.filter)
	var out []model.Instance
	for _, inst := range v.instances {
		if strings.Contains(strings.ToLower(inst.ShortProject()), f) ||
			strings.Contains(strings.ToLower(inst.ShortModel()), f) ||
			strings.Contains(strings.ToLower(inst.Status.String()), f) ||
			strings.Contains(strings.ToLower(inst.Source.String()), f) {
			out = append(out, inst)
		}
	}
	return out
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
