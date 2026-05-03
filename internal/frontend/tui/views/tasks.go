package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/task"
)

// Column widths for the tasks table.
const (
	colTaskStatus = 3
	colTaskPrompt = 28
	colTaskAgent  = 18
	colTaskLoc    = 6
	colTaskState  = 10
	colTaskAge    = 6
)

var (
	// Task status icon styles.
	taskActiveStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")) // yellow
	taskCompletedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")) // green
	taskPendingStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")) // dim
	taskFailedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")) // red

	// Summary line styles.
	taskSummaryLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB"))
)

// TasksView renders a table of tasks from both K8s and local sources.
type TasksView struct {
	tasks    []task.Task
	cursor   int
	width    int
	height   int
	selected *task.Task // currently highlighted task
}

// NewTasksView creates a new empty TasksView.
func NewTasksView() *TasksView {
	return &TasksView{}
}

// SetTasks updates the task list and sorts by priority:
// active (in_progress, claimed) first, then pending, then completed, then failed/dead.
func (v *TasksView) SetTasks(tasks []task.Task) {
	sorted := make([]task.Task, len(tasks))
	copy(sorted, tasks)

	sort.SliceStable(sorted, func(i, j int) bool {
		pi := taskSortPriority(sorted[i].Status)
		pj := taskSortPriority(sorted[j].Status)
		if pi != pj {
			return pi < pj
		}
		// Within the same priority, sort by creation time (oldest first).
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})

	v.tasks = sorted

	// Clamp cursor.
	if v.cursor >= len(v.tasks) {
		v.cursor = max(0, len(v.tasks)-1)
	}

	// Update selected pointer.
	v.updateSelected()
}

// SetSize sets the available width and height for rendering.
func (v *TasksView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// Selected returns the currently highlighted task, or nil if empty.
func (v *TasksView) Selected() *task.Task {
	return v.selected
}

// HandleKey processes navigation keys. Returns true if the key was handled.
func (v *TasksView) HandleKey(key string) bool {
	if len(v.tasks) == 0 {
		return false
	}
	switch key {
	case "j", "down":
		if v.cursor < len(v.tasks)-1 {
			v.cursor++
			v.updateSelected()
		}
		return true
	case "k", "up":
		if v.cursor > 0 {
			v.cursor--
			v.updateSelected()
		}
		return true
	case "g":
		v.cursor = 0
		v.updateSelected()
		return true
	case "G":
		v.cursor = len(v.tasks) - 1
		v.updateSelected()
		return true
	}
	return false
}

// View renders the full tasks table with summary line, header, and rows.
func (v *TasksView) View() string {
	var b strings.Builder

	// Summary line.
	b.WriteString(v.renderSummary())
	b.WriteString("\n")

	// Separator.
	sep := lipgloss.NewStyle().Foreground(lipgloss.Color("#374151")).
		Render(strings.Repeat("\u2500", v.width))
	b.WriteString(sep)
	b.WriteString("\n")

	// Column header.
	header := " " +
		padRight("", colTaskStatus) +
		padRight("TASK", colTaskPrompt) + " " +
		padRight("AGENT", colTaskAgent) + " " +
		padRight("LOC", colTaskLoc) + " " +
		padRight("STATUS", colTaskState) + " " +
		padRight("AGE", colTaskAge)
	if lipgloss.Width(header) < v.width {
		header += strings.Repeat(" ", v.width-lipgloss.Width(header))
	}
	b.WriteString(tableHeaderStyle.Render(header))
	b.WriteString("\n")

	// Empty state.
	if len(v.tasks) == 0 {
		b.WriteString(taskPendingStyle.Render("  No tasks"))
		return b.String()
	}

	// Visible range (reserve 3 for summary + separator + header).
	visibleHeight := v.height - 3
	if visibleHeight < 1 {
		visibleHeight = len(v.tasks)
	}
	start := 0
	if v.cursor >= visibleHeight {
		start = v.cursor - visibleHeight + 1
	}
	end := start + visibleHeight
	if end > len(v.tasks) {
		end = len(v.tasks)
	}

	for idx := start; idx < end; idx++ {
		row := v.renderRow(v.tasks[idx])

		if idx == v.cursor {
			if lipgloss.Width(row) < v.width {
				row += strings.Repeat(" ", v.width-lipgloss.Width(row))
			}
			b.WriteString(agentSelectedStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// renderSummary renders the top summary line with status counts.
func (v *TasksView) renderSummary() string {
	running, done, pending, failed := 0, 0, 0, 0
	for _, t := range v.tasks {
		switch t.Status {
		case task.StatusInProgress, task.StatusClaimed:
			running++
		case task.StatusCompleted:
			done++
		case task.StatusPending:
			pending++
		case task.StatusFailed, task.StatusDead:
			failed++
		}
	}

	parts := []string{
		taskSummaryLabel.Render(" Tasks"),
	}
	if running > 0 {
		parts = append(parts, taskActiveStyle.Render(fmt.Sprintf("\u25cf %d running", running)))
	}
	if done > 0 {
		parts = append(parts, taskCompletedStyle.Render(fmt.Sprintf("\u2713 %d done", done)))
	}
	if pending > 0 {
		parts = append(parts, taskPendingStyle.Render(fmt.Sprintf("\u25cb %d pending", pending)))
	}
	if failed > 0 {
		parts = append(parts, taskFailedStyle.Render(fmt.Sprintf("\u2717 %d failed", failed)))
	}

	return strings.Join(parts, "  ")
}

// renderRow renders a single task row.
func (v *TasksView) renderRow(t task.Task) string {
	icon := taskStatusIcon(t.Status)
	prompt := truncate(t.Prompt, colTaskPrompt-1)
	assignee := t.Assignee
	if assignee == "" {
		assignee = "(pending)"
	}
	loc := string(t.Location)
	status := taskStatusText(t.Status)
	age := FormatTaskAge(t.CreatedAt)

	return " " +
		padRight(icon, colTaskStatus) +
		padRight(prompt, colTaskPrompt) + " " +
		padRight(truncate(assignee, colTaskAgent), colTaskAgent) + " " +
		padRight(loc, colTaskLoc) + " " +
		padRight(status, colTaskState) + " " +
		padRight(age, colTaskAge)
}

// taskStatusIcon returns the styled icon for a task status.
func taskStatusIcon(s task.Status) string {
	switch s {
	case task.StatusInProgress, task.StatusClaimed:
		return taskActiveStyle.Render("\u25cf")
	case task.StatusCompleted:
		return taskCompletedStyle.Render("\u2713")
	case task.StatusPending:
		return taskPendingStyle.Render("\u25cb")
	case task.StatusFailed, task.StatusDead:
		return taskFailedStyle.Render("\u2717")
	default:
		return taskPendingStyle.Render("\u25cb")
	}
}

// taskStatusText returns the display text for a task status.
func taskStatusText(s task.Status) string {
	switch s {
	case task.StatusInProgress:
		return "running"
	case task.StatusClaimed:
		return "claimed"
	case task.StatusCompleted:
		return "done"
	case task.StatusPending:
		return "waiting"
	case task.StatusFailed:
		return "failed"
	case task.StatusDead:
		return "dead"
	default:
		return string(s)
	}
}

// taskSortPriority returns a sort priority for task statuses.
// Lower values sort first: active > pending > completed > failed/dead.
func taskSortPriority(s task.Status) int {
	switch s {
	case task.StatusInProgress:
		return 0
	case task.StatusClaimed:
		return 1
	case task.StatusPending:
		return 2
	case task.StatusCompleted:
		return 3
	case task.StatusFailed:
		return 4
	case task.StatusDead:
		return 5
	default:
		return 6
	}
}

// FormatTaskAge formats a task's age from its creation time.
// Returns "\u2014" (em dash) if the time is zero.
func FormatTaskAge(created time.Time) string {
	if created.IsZero() {
		return "\u2014"
	}
	d := time.Since(created)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// updateSelected sets the selected pointer based on cursor position.
func (v *TasksView) updateSelected() {
	if v.cursor >= 0 && v.cursor < len(v.tasks) {
		v.selected = &v.tasks[v.cursor]
	} else {
		v.selected = nil
	}
}
