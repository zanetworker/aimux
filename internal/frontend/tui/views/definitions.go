package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/config"
)

// DefinitionsView shows agent configs loaded from ~/.aimux/agents.yaml.
type DefinitionsView struct {
	configs []config.AgentConfig
	width   int
	height  int
	cursor  int
}

// NewDefinitionsView creates a new agent definitions view.
func NewDefinitionsView() *DefinitionsView {
	return &DefinitionsView{}
}

// SetConfigs updates the agent config list.
func (v *DefinitionsView) SetConfigs(configs []config.AgentConfig) {
	v.configs = configs
	if v.cursor >= len(configs) {
		v.cursor = len(configs) - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

// SetSize sets the available width and height.
func (v *DefinitionsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// CursorUp moves the cursor up.
func (v *DefinitionsView) CursorUp() {
	if v.cursor > 0 {
		v.cursor--
	}
}

// CursorDown moves the cursor down.
func (v *DefinitionsView) CursorDown() {
	if v.cursor < len(v.configs)-1 {
		v.cursor++
	}
}

// Selected returns the currently selected config, if any.
func (v *DefinitionsView) Selected() *config.AgentConfig {
	if v.cursor < 0 || v.cursor >= len(v.configs) {
		return nil
	}
	return &v.configs[v.cursor]
}

// View renders the definitions table.
func (v *DefinitionsView) View() string {
	w := v.width
	if w < 40 {
		w = 80
	}

	var b strings.Builder

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("#1E3A5F"))

	b.WriteString(headerStyle.Render("  Agent Definitions") + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %d configs", len(v.configs))) + "\n\n")

	if len(v.configs) == 0 {
		b.WriteString(dimStyle.Render("  No agent definitions found.") + "\n")
		b.WriteString(dimStyle.Render("  Add configs to ~/.aimux/agents.yaml") + "\n")
		return b.String()
	}

	colName := 20
	colHarness := 12
	colModel := 20
	colPrompt := w - colName - colHarness - colModel - 16
	if colPrompt < 20 {
		colPrompt = 20
	}

	tableHeader := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#5F87FF")).
		Background(lipgloss.Color("#1E293B"))

	header := fmt.Sprintf("  %-*s   %-*s   %-*s   %-*s",
		colName, "NAME",
		colHarness, "HARNESS",
		colModel, "MODEL",
		colPrompt, "PROMPT")
	b.WriteString(tableHeader.Render(padToWidth(header, w)) + "\n")

	maxRows := v.height - 6
	if maxRows < 1 {
		maxRows = 10
	}

	start := 0
	if v.cursor >= maxRows {
		start = v.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(v.configs) {
		end = len(v.configs)
	}

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB")).Bold(true)
	harnessStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA"))
	modelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4"))
	promptStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))

	for i := start; i < end; i++ {
		ac := v.configs[i]

		prompt := ac.Prompt
		if len(prompt) > colPrompt {
			prompt = prompt[:colPrompt-3] + "..."
		}
		prompt = strings.ReplaceAll(prompt, "\n", " ")

		model := ac.Model
		if model == "" {
			model = "-"
		}

		row := "  " +
			nameStyle.Render(padRight(truncate(ac.Name, colName), colName)) + "   " +
			harnessStyle.Render(padRight(truncate(ac.Harness, colHarness), colHarness)) + "   " +
			modelStyle.Render(padRight(truncate(model, colModel), colModel)) + "   " +
			promptStyle.Render(padRight(truncate(prompt, colPrompt), colPrompt))

		if i == v.cursor {
			row = selectedStyle.Render(padToWidth(row, w))
		}

		b.WriteString(row + "\n")
	}

	return b.String()
}

func padToWidth(s string, w int) string {
	sw := lipgloss.Width(s)
	if sw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-sw)
}
