package views

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/config"
)

type defFormStep int

const (
	defFormNone defFormStep = iota
	defFormName
	defFormModel
	defFormPrompt
	defFormConfirmDelete
)

// DefinitionsView shows agent configs loaded from ~/.aimux/agents.yaml.
type DefinitionsView struct {
	configs   []config.AgentConfig
	width     int
	height    int
	cursor    int
	configDir string

	formStep  defFormStep
	formInput TextInput
	formData  map[string]string
	status    string
}

// NewDefinitionsView creates a new agent definitions view.
func NewDefinitionsView() *DefinitionsView {
	return &DefinitionsView{
		formData: make(map[string]string),
	}
}

// SetConfigDir sets the directory for agent config file CRUD operations.
func (v *DefinitionsView) SetConfigDir(dir string) {
	v.configDir = dir
}

// InForm returns true when the view is in a form input state.
func (v *DefinitionsView) InForm() bool {
	return v.formStep != defFormNone
}

// Status returns and clears the last status message.
func (v *DefinitionsView) Status() string {
	s := v.status
	v.status = ""
	return s
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

// HandleKey processes key events for the definitions view. Returns true if consumed.
func (v *DefinitionsView) HandleKey(msg tea.KeyMsg) bool {
	if v.formStep != defFormNone {
		return v.handleFormKey(msg)
	}

	switch msg.String() {
	case "j", "down":
		v.CursorDown()
		return true
	case "k", "up":
		v.CursorUp()
		return true
	case "c":
		v.formStep = defFormName
		v.formInput.Reset()
		v.formData = make(map[string]string)
		return true
	case "d":
		sel := v.Selected()
		if sel == nil {
			return true
		}
		v.formStep = defFormConfirmDelete
		v.formInput.Reset()
		return true
	case "enter":
		sel := v.Selected()
		if sel != nil {
			v.status = fmt.Sprintf("Use CLI: aimux sessions start %s", sel.Name)
		}
		return true
	}
	return false
}

func (v *DefinitionsView) handleFormKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "esc":
		v.formStep = defFormNone
		v.formInput.Reset()
		return true
	case "enter":
		return v.advanceForm()
	default:
		v.formInput.HandleKey(msg)
		return true
	}
}

func (v *DefinitionsView) advanceForm() bool {
	switch v.formStep {
	case defFormName:
		name := strings.TrimSpace(v.formInput.Value())
		if name == "" {
			v.status = "Name cannot be empty"
			v.formStep = defFormNone
			return true
		}
		if err := config.ValidateAgentName(name); err != nil {
			v.status = err.Error()
			v.formStep = defFormNone
			return true
		}
		v.formData["name"] = name
		v.formStep = defFormModel
		v.formInput.Reset()
	case defFormModel:
		v.formData["model"] = strings.TrimSpace(v.formInput.Value())
		v.formStep = defFormPrompt
		v.formInput.Reset()
	case defFormPrompt:
		v.formData["prompt"] = strings.TrimSpace(v.formInput.Value())
		ac := config.AgentConfig{
			Name:    v.formData["name"],
			Harness: "claude",
			Model:   v.formData["model"],
			Prompt:  v.formData["prompt"],
		}
		dir := v.agentsDir()
		if err := config.SaveAgent(dir, ac); err != nil {
			v.status = fmt.Sprintf("Error: %s", err)
		} else {
			v.reloadConfigs()
			v.status = fmt.Sprintf("Created agent '%s'", ac.Name)
		}
		v.formStep = defFormNone
		v.formInput.Reset()
	case defFormConfirmDelete:
		answer := strings.TrimSpace(strings.ToLower(v.formInput.Value()))
		if answer == "y" || answer == "yes" {
			sel := v.Selected()
			if sel != nil {
				dir := v.agentsDir()
				if err := config.DeleteAgent(dir, sel.Name); err != nil {
					v.status = fmt.Sprintf("Error: %s", err)
				} else {
					v.status = fmt.Sprintf("Deleted agent '%s'", sel.Name)
					v.reloadConfigs()
				}
			}
		} else {
			v.status = "Delete cancelled"
		}
		v.formStep = defFormNone
		v.formInput.Reset()
	}
	return true
}

func (v *DefinitionsView) agentsDir() string {
	if v.configDir != "" {
		return filepath.Join(v.configDir, "agents")
	}
	return filepath.Join(".aimux", "agents")
}

func (v *DefinitionsView) reloadConfigs() {
	dir := v.agentsDir()
	configs, err := config.LoadAgentDir(dir)
	if err != nil {
		return
	}
	v.SetConfigs(configs)
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
		if v.formStep != defFormNone {
			b.WriteString("\n")
			b.WriteString(v.renderForm())
		}
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

	if v.formStep != defFormNone {
		b.WriteString("\n")
		b.WriteString(v.renderForm())
	}

	return b.String()
}

func (v *DefinitionsView) renderForm() string {
	promptStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#5F87FF")).Bold(true)
	inputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))

	var label string
	switch v.formStep {
	case defFormName:
		label = "  Agent name: "
	case defFormModel:
		label = "  Model (e.g. haiku, opus, sonnet): "
	case defFormPrompt:
		label = "  Prompt: "
	case defFormConfirmDelete:
		sel := v.Selected()
		name := ""
		if sel != nil {
			name = sel.Name
		}
		label = fmt.Sprintf("  Delete agent '%s'? (y/n): ", name)
	}

	cursor := inputStyle.Render(v.formInput.BeforeCursor()) +
		lipgloss.NewStyle().Background(lipgloss.Color("#5F87FF")).Render(" ") +
		inputStyle.Render(v.formInput.AfterCursor())

	return promptStyle.Render(label) + cursor
}

func padToWidth(s string, w int) string {
	sw := lipgloss.Width(s)
	if sw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-sw)
}
