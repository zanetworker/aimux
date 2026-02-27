package views

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- Styles ---

var (
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	textStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))

	// Turn header
	turnHeaderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#5F87FF")).Bold(true)
	turnRuleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#374151"))
	turnSelectedBg = lipgloss.NewStyle().
			Background(lipgloss.Color("#1E3A5F"))
	turnMetaStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF"))

	// Section labels
	inputLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	actionLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	outputLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#22C55E")).Bold(true)
	diffLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A78BFA")).Bold(true)

	// Content
	toolNameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	toolArrowStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))
	inputTextStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	outputTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#D1FAE5"))

	// Tool success/failure
	toolSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	toolFailStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))

	// Diff
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	diffRemoveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))

	// Stats bar
	statsStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF")).
			Background(lipgloss.Color("#1E293B")).
			Bold(true)

	// Annotation badges
	annotGoodStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#111827")).
			Background(lipgloss.Color("#22C55E")).
			Bold(true)
	annotBadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#111827")).
			Background(lipgloss.Color("#EF4444")).
			Bold(true)
	annotWasteStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#111827")).
			Background(lipgloss.Color("#F59E0B")).
			Bold(true)

	// Cost style
	costStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))

	// Filter
	filterActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#111827")).
				Background(lipgloss.Color("#06B6D4")).
				Bold(true)
)

// --- Messages ---

// AnnotationMsg is emitted when the user annotates a turn. app.go handles
// persistence; this view only tracks the label in memory.
type AnnotationMsg struct {
	Turn  int
	Label string // "" means remove
}

// --- Data model ---

// ToolSpan represents a single tool call within a turn.
type ToolSpan struct {
	Name      string
	Snippet   string // short description of the input
	Success   bool   // true if tool succeeded
	ErrorMsg  string // error message if failed
	OldString string // for Edit: the old text
	NewString string // for Edit: the new text
	toolUseID string // internal: for matching tool_result
}

// TraceTurn groups a user prompt with the assistant response into one logical
// unit -- the fundamental trace element for evaluation (input -> actions -> output).
type TraceTurn struct {
	Number      int
	Timestamp   time.Time
	EndTime     time.Time // timestamp of last entry in this turn (for duration)
	UserLines   []string  // full user input text
	Actions     []ToolSpan
	OutputLines []string
	TokensIn    int64
	TokensOut   int64
	CostUSD     float64 // calculated from tokens + model
	Model       string  // model used for this turn
}

// Duration returns the wall-clock duration of this turn.
func (t TraceTurn) Duration() time.Duration {
	if t.EndTime.IsZero() || t.Timestamp.IsZero() {
		return 0
	}
	d := t.EndTime.Sub(t.Timestamp)
	if d < 0 {
		return 0
	}
	return d
}

// ErrorCount returns the number of failed tool calls in this turn.
func (t TraceTurn) ErrorCount() int {
	n := 0
	for _, a := range t.Actions {
		if !a.Success {
			n++
		}
	}
	return n
}

// --- Cost estimation ---

func estimateTurnCost(model string, tokIn, tokOut int64) float64 {
	// Rough rates per million tokens
	var inRate, outRate float64
	switch {
	case strings.Contains(model, "opus"):
		inRate, outRate = 15.0, 75.0
	case strings.Contains(model, "sonnet"):
		inRate, outRate = 3.0, 15.0
	case strings.Contains(model, "haiku"):
		inRate, outRate = 0.25, 1.25
	default:
		inRate, outRate = 3.0, 15.0
	}
	return (float64(tokIn)*inRate + float64(tokOut)*outRate) / 1_000_000
}

// --- LogsView ---

// LogsView renders a structured conversation trace grouped by turns.
// Each turn shows INPUT (user), ACTIONS (tools), and OUTPUT (assistant)
// sections inspired by MLflow/Braintrust trace UIs.
type LogsView struct {
	turns        []TraceTurn
	filePath     string
	width        int
	height       int
	cursor       int
	expanded     map[int]bool
	pid          int
	filterText   string
	filterMode   bool
	filterInput  string
	annotations  map[int]string
	compact      bool // when true, hides the interactive status bar (for preview pane)
	scrollOffset int  // line-level scroll offset within the rendered view
}

// NewLogsView creates a new LogsView for the given PID and log file path.
func NewLogsView(pid int, filePath string) *LogsView {
	v := &LogsView{
		pid:         pid,
		filePath:    filePath,
		expanded:    make(map[int]bool),
		annotations: make(map[int]string),
	}
	v.Reload()
	return v
}

// SetSize sets the available width and height.
func (v *LogsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// Turns returns the parsed trace turns for export.
func (v *LogsView) Turns() []TraceTurn {
	return v.turns
}

// HasActiveFilter returns true if the logs view has a filter or filter mode active.
func (v *LogsView) HasActiveFilter() bool {
	return v.filterMode || v.filterText != ""
}

// SetAnnotations loads a set of turn annotations from external storage.
func (v *LogsView) SetAnnotations(a map[int]string) {
	if a == nil {
		v.annotations = make(map[int]string)
	} else {
		v.annotations = a
	}
}

// Reload reads and parses the JSONL log file into turns. Preserves the
// cursor position unless new turns were added while the cursor was at the
// bottom (auto-follow mode).
func (v *LogsView) Reload() {
	data, err := os.ReadFile(v.filePath)
	if err != nil {
		v.turns = nil
		return
	}

	prevCount := len(v.turns)
	prevCursor := v.cursor
	atBottom := prevCursor >= prevCount-1

	v.turns = parseJSONLToTurns(string(data))

	if len(v.turns) == 0 {
		v.cursor = 0
		return
	}

	if atBottom && len(v.turns) > prevCount {
		// Was following -- stay at the new bottom
		v.cursor = len(v.turns) - 1
	} else if prevCursor < len(v.turns) {
		// Stay at the same position
		v.cursor = prevCursor
	} else {
		v.cursor = len(v.turns) - 1
	}
}

// visibleTurns returns the turns that match the current filter.
func (v *LogsView) visibleTurns() []TraceTurn {
	if v.filterText == "" {
		return v.turns
	}
	needle := strings.ToLower(v.filterText)
	var result []TraceTurn
	for _, t := range v.turns {
		if turnMatchesFilter(t, needle) {
			result = append(result, t)
		}
	}
	return result
}

func turnMatchesFilter(t TraceTurn, needle string) bool {
	for _, line := range t.UserLines {
		if strings.Contains(strings.ToLower(line), needle) {
			return true
		}
	}
	for _, a := range t.Actions {
		if strings.Contains(strings.ToLower(a.Name), needle) {
			return true
		}
		if strings.Contains(strings.ToLower(a.Snippet), needle) {
			return true
		}
	}
	for _, line := range t.OutputLines {
		if strings.Contains(strings.ToLower(line), needle) {
			return true
		}
	}
	return false
}

// annotation labels cycle: good -> bad -> wasteful -> (remove)
var annotationCycle = []string{"good", "bad", "wasteful", ""}

// Update handles key messages for turn navigation. Returns a tea.Cmd when
// an annotation change needs to propagate to app.go for persistence.
func (v *LogsView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Filter mode input
		if v.filterMode {
			return v.handleFilterKey(msg)
		}

		visible := v.visibleTurns()
		if len(visible) == 0 && msg.String() != "/" && msg.String() != "esc" {
			return nil
		}

		switch msg.String() {
		case "j", "down":
			if v.cursor < len(visible)-1 {
				v.cursor++
				v.scrollOffset = 0 // reset line scroll when moving to next turn
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
				v.scrollOffset = 0
			}
		case "c":
			// Collapse all expanded turns
			v.expanded = make(map[int]bool)
			v.scrollOffset = 0
		case "g":
			v.cursor = 0
			v.scrollOffset = 0
		case "G":
			v.cursor = len(visible) - 1
			v.scrollOffset = 0
		case "enter", " ":
			if len(visible) > 0 && v.cursor < len(visible) {
				turnNum := visible[v.cursor].Number
				v.expanded[turnNum] = !v.expanded[turnNum]
				v.scrollOffset = 0 // reset scroll when toggling
			}
		case "d":
			// Page down: scroll by half the visible height (lines, not turns)
			halfPage := v.height / 2
			if halfPage < 1 {
				halfPage = 5
			}
			v.scrollOffset += halfPage
		case "u":
			// Page up: scroll by half the visible height
			halfPage := v.height / 2
			if halfPage < 1 {
				halfPage = 5
			}
			v.scrollOffset -= halfPage
			if v.scrollOffset < 0 {
				v.scrollOffset = 0
			}
		case "/":
			v.filterMode = true
			v.filterInput = ""
			return nil
		case "esc":
			if v.filterText != "" {
				v.filterText = ""
				v.cursor = 0
				return nil
			}
		case "a":
			// Cycle annotation on current turn (a = annotate)
			if len(visible) == 0 || v.cursor >= len(visible) {
				return nil
			}
			turnNum := visible[v.cursor].Number
			current := v.annotations[turnNum]
			next := ""
			for i, label := range annotationCycle {
				if label == current {
					next = annotationCycle[(i+1)%len(annotationCycle)]
					break
				}
			}
			if next == "" && current == "" {
				// First press when no annotation: set to "good"
				next = "good"
			}
			if next == "" {
				delete(v.annotations, turnNum)
			} else {
				v.annotations[turnNum] = next
			}
			return func() tea.Msg {
				return AnnotationMsg{Turn: turnNum, Label: next}
			}
		}
	}
	return nil
}

func (v *LogsView) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.filterMode = false
		v.filterText = v.filterInput
		v.cursor = 0
		return nil
	case "esc":
		v.filterMode = false
		v.filterInput = ""
		return nil
	case "backspace":
		if len(v.filterInput) > 0 {
			v.filterInput = v.filterInput[:len(v.filterInput)-1]
		}
		return nil
	default:
		if len(msg.String()) == 1 {
			v.filterInput += msg.String()
		}
		return nil
	}
}

// View renders the trace view with structured turns.
func (v *LogsView) View() string {
	visible := v.visibleTurns()

	if len(visible) == 0 && len(v.turns) == 0 {
		return dimStyle.Render("  No trace entries found for this session.")
	}

	// Render all lines, tracking where the cursor turn starts
	var allLines []string

	// Stats summary at the top
	allLines = append(allLines, v.renderStats())
	allLines = append(allLines, "") // spacer

	// Filter indicator
	if v.filterText != "" {
		allLines = append(allLines, "  "+filterActiveStyle.Render(" FILTER: "+v.filterText+" ")+
			dimStyle.Render(fmt.Sprintf("  %d/%d turns", len(visible), len(v.turns))))
		allLines = append(allLines, "") // spacer
	}

	if len(visible) == 0 {
		allLines = append(allLines, dimStyle.Render("  No turns match the current filter."))
	}

	cursorLineStart := len(allLines)

	for i, turn := range visible {
		if i == v.cursor {
			cursorLineStart = len(allLines)
		}

		isSelected := i == v.cursor
		isExpanded := v.expanded[turn.Number]

		lines := v.renderTurn(turn, isSelected, isExpanded)
		allLines = append(allLines, lines...)
	}

	// Scroll window: start at cursor turn header, then apply line-level offset
	visibleHeight := v.height - 1
	if visibleHeight < 1 {
		visibleHeight = len(allLines)
	}

	// Base position: show the selected turn's header at the top
	start := cursorLineStart + v.scrollOffset
	if start < 0 {
		start = 0
		v.scrollOffset = -cursorLineStart // clamp
	}

	end := start + visibleHeight
	if end > len(allLines) {
		end = len(allLines)
		start = end - visibleHeight
		if start < 0 {
			start = 0
		}
	}

	// Clamp scroll offset so it doesn't go past the content
	maxOffset := len(allLines) - cursorLineStart - visibleHeight
	if maxOffset < 0 {
		maxOffset = 0
	}
	if v.scrollOffset > maxOffset {
		v.scrollOffset = maxOffset
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		b.WriteString(allLines[i])
		b.WriteString("\n")
	}

	// Status bar (hidden in compact/preview mode)
	if !v.compact {
		status := fmt.Sprintf(" Turn %d/%d", v.cursor+1, len(visible))
		if v.scrollOffset > 0 {
			status += fmt.Sprintf("  +%d lines", v.scrollOffset)
		}
		b.WriteString(dimStyle.Render(status))
		if v.filterMode {
			b.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true).Render("/") + v.filterInput + lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Render("|"))
		}
	}

	return b.String()
}

// renderStats produces the top-of-view session summary line.
func (v *LogsView) renderStats() string {
	totalTurns := len(v.turns)
	totalActions := 0
	totalErrors := 0
	totalCost := 0.0
	toolCounts := make(map[string]int)

	for _, t := range v.turns {
		for _, a := range t.Actions {
			totalActions++
			toolCounts[a.Name]++
			if !a.Success {
				totalErrors++
			}
		}
		totalCost += t.CostUSD
	}

	parts := []string{
		fmt.Sprintf("%d turns", totalTurns),
		fmt.Sprintf("%d actions", totalActions),
	}

	if totalErrors > 0 {
		parts = append(parts, toolFailStyle.Render(fmt.Sprintf("%d errors", totalErrors)))
	} else {
		parts = append(parts, fmt.Sprintf("%d errors", totalErrors))
	}

	parts = append(parts, costStyle.Render(fmt.Sprintf("$%.2f total", totalCost)))

	// Top tool counts
	type toolCount struct {
		name  string
		count int
	}
	var sorted []toolCount
	for name, count := range toolCounts {
		sorted = append(sorted, toolCount{name, count})
	}
	// Simple sort by count descending
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].count > sorted[i].count {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	var toolParts []string
	for i, tc := range sorted {
		if i >= 5 {
			break
		}
		toolParts = append(toolParts, fmt.Sprintf("%s:%d", shortToolName(tc.name), tc.count))
	}
	if len(toolParts) > 0 {
		parts = append(parts, strings.Join(toolParts, " "))
	}

	return "  " + statsStyle.Render(" "+strings.Join(parts, " | ")+" ")
}

// --- Turn rendering ---

func (v *LogsView) renderTurn(t TraceTurn, selected, expanded bool) []string {
	w := v.width
	if w < 20 {
		w = 80
	}

	var lines []string

	// Turn header
	lines = append(lines, v.renderTurnHeader(t, selected, expanded, w))

	if !expanded {
		return lines
	}

	innerW := w - 4 // indentation

	// -- INPUT section --
	lines = append(lines, v.sectionHeader("INPUT", inputLabelStyle, innerW))
	if len(t.UserLines) > 0 {
		for _, line := range t.UserLines {
			if len(line) > innerW {
				line = line[:innerW-3] + "..."
			}
			lines = append(lines, "    "+inputTextStyle.Render(line))
		}
	} else {
		lines = append(lines, "    "+dimStyle.Render("(no input)"))
	}
	lines = append(lines, "") // spacer

	// -- ACTIONS section --
	if len(t.Actions) > 0 {
		lines = append(lines, v.sectionHeader(fmt.Sprintf("ACTIONS (%d)", len(t.Actions)), actionLabelStyle, innerW))
		for i, action := range t.Actions {
			connector := "├─"
			if i == len(t.Actions)-1 {
				connector = "└─"
			}

			// Success/failure indicator
			var indicator string
			if action.Success {
				indicator = toolSuccessStyle.Render("✓")
			} else {
				indicator = toolFailStyle.Render("✗")
			}

			line := "    " + toolArrowStyle.Render(connector) + " " +
				indicator + " " +
				toolNameStyle.Render(padRight(action.Name, 8))
			if action.Snippet != "" {
				remaining := innerW - lipgloss.Width(line) - 1
				snippet := action.Snippet
				if remaining > 0 && len(snippet) > remaining {
					snippet = snippet[:remaining-3] + "..."
				}
				line += " " + dimStyle.Render(snippet)
			}
			// Show error message for failed tools
			if !action.Success && action.ErrorMsg != "" {
				errMsg := action.ErrorMsg
				maxErr := innerW - 16
				if maxErr > 0 && len(errMsg) > maxErr {
					errMsg = errMsg[:maxErr-3] + "..."
				}
				line += "  " + toolFailStyle.Render("\""+errMsg+"\"")
			}
			lines = append(lines, line)
		}
		lines = append(lines, "") // spacer
	}

	// -- DIFF section (for Edit actions with old/new strings) --
	hasEdits := false
	for _, a := range t.Actions {
		if a.OldString != "" && a.NewString != "" {
			hasEdits = true
			break
		}
	}
	if hasEdits {
		lines = append(lines, v.sectionHeader("DIFF", diffLabelStyle, innerW))
		for _, a := range t.Actions {
			if a.OldString == "" && a.NewString == "" {
				continue
			}
			// Show which Edit action
			label := "Ed"
			snippet := a.Snippet
			if len(snippet) > 40 {
				snippet = snippet[:37] + "..."
			}
			lines = append(lines, "    "+toolNameStyle.Render(label)+" "+dimStyle.Render(snippet))

			// Removed lines
			maxDiffLen := innerW - 6
			oldStr := a.OldString
			if len(oldStr) > maxDiffLen {
				oldStr = oldStr[:maxDiffLen-3] + "..."
			}
			for _, dl := range strings.Split(oldStr, "\n") {
				dl = strings.TrimRight(dl, "\r")
				if len(dl) > maxDiffLen {
					dl = dl[:maxDiffLen-3] + "..."
				}
				lines = append(lines, "    "+diffRemoveStyle.Render("- "+dl))
			}

			// Added lines
			newStr := a.NewString
			if len(newStr) > maxDiffLen {
				newStr = newStr[:maxDiffLen-3] + "..."
			}
			for _, dl := range strings.Split(newStr, "\n") {
				dl = strings.TrimRight(dl, "\r")
				if len(dl) > maxDiffLen {
					dl = dl[:maxDiffLen-3] + "..."
				}
				lines = append(lines, "    "+diffAddStyle.Render("+ "+dl))
			}
		}
		lines = append(lines, "") // spacer
	}

	// -- OUTPUT section --
	lines = append(lines, v.sectionHeader("OUTPUT", outputLabelStyle, innerW))
	if len(t.OutputLines) > 0 {
		for _, line := range t.OutputLines {
			if len(line) > innerW {
				line = line[:innerW-3] + "..."
			}
			lines = append(lines, "    "+outputTextStyle.Render(line))
		}
	} else {
		lines = append(lines, "    "+dimStyle.Render("(no output)"))
	}

	// Bottom rule
	lines = append(lines, "  "+turnRuleStyle.Render(strings.Repeat("─", w-4)))
	lines = append(lines, "") // spacer between turns

	return lines
}

func (v *LogsView) renderTurnHeader(t TraceTurn, selected, expanded bool, w int) string {
	arrow := "▸"
	if expanded {
		arrow = "▾"
	}

	num := turnHeaderStyle.Render(fmt.Sprintf(" %s Turn %d", arrow, t.Number))

	var meta []string
	if !t.Timestamp.IsZero() {
		meta = append(meta, t.Timestamp.Format("15:04"))
	}

	// Cost
	if t.CostUSD > 0 {
		meta = append(meta, costStyle.Render(fmt.Sprintf("$%.2f", t.CostUSD)))
	}

	// Duration
	dur := t.Duration()
	if dur > 0 {
		meta = append(meta, dimStyle.Render(fmt.Sprintf("+%ds", int(dur.Seconds()))))
	}

	if t.TokensIn > 0 || t.TokensOut > 0 {
		meta = append(meta, fmt.Sprintf("%s/%s tok",
			formatTokenCount(t.TokensIn), formatTokenCount(t.TokensOut)))
	}

	// Action bar: colored blocks showing tool types used
	if len(t.Actions) > 0 {
		bar := renderActionBar(t.Actions)
		meta = append(meta, bar)
	}

	// Annotation badge
	if label, ok := v.annotations[t.Number]; ok && label != "" {
		badge := renderAnnotationBadge(label)
		meta = append(meta, badge)
	}

	metaStr := ""
	if len(meta) > 0 {
		metaStr = turnMetaStyle.Render(" ") + strings.Join(meta, turnMetaStyle.Render(" "))
	}

	// User prompt preview in collapsed mode
	prompt := ""
	if !expanded && len(t.UserLines) > 0 {
		p := t.UserLines[0]
		maxP := w - lipgloss.Width(num) - lipgloss.Width(metaStr) - 6
		if maxP > 10 {
			if len(p) > maxP {
				p = p[:maxP-3] + "..."
			}
			prompt = " " + inputTextStyle.Render(p)
		}
	}

	line := num + metaStr + prompt

	// Fill with rule
	lineW := lipgloss.Width(line)
	if lineW < w-2 {
		line += " " + turnRuleStyle.Render(strings.Repeat("─", w-lineW-2))
	}

	if selected {
		return turnSelectedBg.Render(padRight(line, w))
	}
	return line
}

func renderAnnotationBadge(label string) string {
	switch label {
	case "good":
		return annotGoodStyle.Render("[GOOD]")
	case "bad":
		return annotBadStyle.Render("[BAD]")
	case "wasteful":
		return annotWasteStyle.Render("[WASTE]")
	default:
		return dimStyle.Render("[" + strings.ToUpper(label) + "]")
	}
}

// renderActionBar creates a compact visual summary of tool calls.
// Shows abbreviated tool names as colored tags, e.g.: Read Edit Bash
func renderActionBar(actions []ToolSpan) string {
	readStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4"))
	writeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	bashStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA"))
	searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399"))
	otherStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))

	// Deduplicate and count tool types
	counts := make(map[string]int)
	order := make([]string, 0)
	for _, a := range actions {
		if counts[a.Name] == 0 {
			order = append(order, a.Name)
		}
		counts[a.Name]++
	}

	var parts []string
	for _, name := range order {
		count := counts[name]
		label := shortToolName(name)
		if count > 1 {
			label += fmt.Sprintf("x%d", count)
		}

		var style lipgloss.Style
		switch name {
		case "Read":
			style = readStyle
		case "Write", "Edit":
			style = writeStyle
		case "Bash":
			style = bashStyle
		case "Grep", "Glob", "WebSearch":
			style = searchStyle
		default:
			style = otherStyle
		}
		parts = append(parts, style.Render(label))

		if len(parts) >= 5 {
			remaining := len(order) - 5
			if remaining > 0 {
				parts = append(parts, otherStyle.Render(fmt.Sprintf("+%d", remaining)))
			}
			break
		}
	}

	return strings.Join(parts, " ")
}

func shortToolName(name string) string {
	switch name {
	case "Read":
		return "Rd"
	case "Write":
		return "Wr"
	case "Edit":
		return "Ed"
	case "Bash":
		return "Sh"
	case "Grep":
		return "Gr"
	case "Glob":
		return "Gl"
	case "Task":
		return "Tk"
	case "WebSearch":
		return "Ws"
	case "WebFetch":
		return "Wf"
	default:
		if len(name) > 3 {
			return name[:3]
		}
		return name
	}
}

func (v *LogsView) sectionHeader(label string, style lipgloss.Style, w int) string {
	rendered := style.Render("  " + label + " ")
	ruleLen := w - lipgloss.Width(rendered)
	if ruleLen < 0 {
		ruleLen = 0
	}
	return rendered + turnRuleStyle.Render(strings.Repeat("─", ruleLen))
}

// --- JSONL Parsing ---

// parseJSONLToTurns detects the format (Claude or Codex) and dispatches
// to the appropriate parser.
func parseJSONLToTurns(data string) []TraceTurn {
	// Detect format by checking the first non-empty line
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, `"session_meta"`) || strings.Contains(line, `"response_item"`) || strings.Contains(line, `"event_msg"`) {
			return parseCodexJSONL(data)
		}
		break
	}
	return parseClaudeJSONL(data)
}

func parseClaudeJSONL(data string) []TraceTurn {
	var entries []jsonlEntry
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		e := parseRawEntry(line)
		if e.entryType != "" {
			entries = append(entries, e)
		}
	}

	var turns []TraceTurn
	var current *TraceTurn
	turnNum := 0

	// Index of tool_use_id -> pointer to the ToolSpan (across turns).
	// We build this as we go so tool_result entries can look up their tool.
	pendingTools := make(map[string]*ToolSpan)

	for _, e := range entries {
		switch e.entryType {
		case "user":
			if e.isHumanMessage && e.textContent != "" {
				if current != nil {
					turns = append(turns, *current)
				}
				turnNum++
				current = &TraceTurn{
					Number:    turnNum,
					Timestamp: e.timestamp,
				}
				// Split user input into lines
				for _, line := range strings.Split(e.textContent, "\n") {
					trimmed := strings.TrimSpace(line)
					if trimmed != "" {
						current.UserLines = append(current.UserLines, trimmed)
					}
				}
			} else if e.hasToolResults {
				// This is a user message with tool_result blocks.
				// Match each result to its pending tool_use_id.
				for _, tr := range e.toolResults {
					if span, ok := pendingTools[tr.toolUseID]; ok {
						span.Success = !tr.isError
						if tr.isError {
							// Take first 200 chars of error content
							errMsg := tr.content
							if len(errMsg) > 200 {
								errMsg = errMsg[:200]
							}
							span.ErrorMsg = errMsg
						}
						delete(pendingTools, tr.toolUseID)
					}
				}
			}

		case "assistant":
			if current == nil {
				turnNum++
				current = &TraceTurn{
					Number:    turnNum,
					Timestamp: e.timestamp,
				}
			}
			current.TokensIn += e.tokensIn
			current.TokensOut += e.tokensOut

			// Track end time for duration calculation
			if !e.timestamp.IsZero() {
				current.EndTime = e.timestamp
			}

			// Parse model name
			if e.model != "" && current.Model == "" {
				current.Model = e.model
			}

			for _, block := range e.blocks {
				switch block.blockType {
				case "text":
					for _, line := range strings.Split(block.text, "\n") {
						trimmed := strings.TrimSpace(line)
						if trimmed != "" {
							current.OutputLines = append(current.OutputLines, trimmed)
						}
					}
				case "tool_use":
					span := ToolSpan{
						Name:      block.toolName,
						Snippet:   block.toolSnippet,
						Success:   true, // default to success; overwritten by tool_result
						toolUseID: block.toolUseID,
						OldString: block.editOldString,
						NewString: block.editNewString,
					}
					current.Actions = append(current.Actions, span)
					// Register for tool_result matching
					if block.toolUseID != "" {
						// Store pointer to the last appended span
						idx := len(current.Actions) - 1
						pendingTools[block.toolUseID] = &current.Actions[idx]
					}
				}
			}
		}
	}

	if current != nil {
		turns = append(turns, *current)
	}

	// Calculate per-turn cost
	for i := range turns {
		turns[i].CostUSD = estimateTurnCost(turns[i].Model, turns[i].TokensIn, turns[i].TokensOut)
	}

	return turns
}

// --- Raw JSONL entry parsing ---

type contentBlock struct {
	blockType     string
	text          string
	toolName      string
	toolSnippet   string
	toolUseID     string
	editOldString string
	editNewString string
}

type toolResultEntry struct {
	toolUseID string
	content   string
	isError   bool
}

type jsonlEntry struct {
	entryType      string
	timestamp      time.Time
	isHumanMessage bool
	textContent    string
	blocks         []contentBlock
	tokensIn       int64
	tokensOut      int64
	model          string
	hasToolResults bool
	toolResults    []toolResultEntry
}

func parseRawEntry(line string) jsonlEntry {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return jsonlEntry{}
	}

	var e jsonlEntry
	json.Unmarshal(raw["type"], &e.entryType)

	var tsStr string
	if err := json.Unmarshal(raw["timestamp"], &tsStr); err == nil {
		e.timestamp, _ = time.Parse(time.RFC3339Nano, tsStr)
	}

	switch e.entryType {
	case "user":
		e.parseUser(raw)
	case "assistant":
		e.parseAssistant(raw)
	}

	return e
}

func (e *jsonlEntry) parseUser(raw map[string]json.RawMessage) {
	msgRaw := raw["message"]
	if msgRaw == nil {
		return
	}

	var msgObj map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msgObj); err != nil {
		return
	}

	contentRaw := msgObj["content"]
	if contentRaw == nil {
		return
	}

	// Try as simple string (human message)
	var contentStr string
	if err := json.Unmarshal(contentRaw, &contentStr); err == nil {
		e.isHumanMessage = true
		e.textContent = contentStr
		return
	}

	// Try as array (could contain tool_result blocks)
	var contentArr []map[string]interface{}
	if err := json.Unmarshal(contentRaw, &contentArr); err == nil {
		for _, item := range contentArr {
			itemType, _ := item["type"].(string)
			if itemType == "tool_result" {
				e.hasToolResults = true
				tr := toolResultEntry{}
				tr.toolUseID, _ = item["tool_use_id"].(string)
				// is_error can be bool
				if isErr, ok := item["is_error"].(bool); ok {
					tr.isError = isErr
				}
				// content can be string or array
				switch c := item["content"].(type) {
				case string:
					tr.content = c
				case []interface{}:
					// Array of content blocks; extract text
					var parts []string
					for _, block := range c {
						if bm, ok := block.(map[string]interface{}); ok {
							if text, ok := bm["text"].(string); ok {
								parts = append(parts, text)
							}
						}
					}
					tr.content = strings.Join(parts, "\n")
				}
				e.toolResults = append(e.toolResults, tr)
			}
		}
	}

	e.isHumanMessage = false
}

func (e *jsonlEntry) parseAssistant(raw map[string]json.RawMessage) {
	msgRaw := raw["message"]
	if msgRaw == nil {
		return
	}

	var msgObj map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msgObj); err != nil {
		return
	}

	// Parse model name
	if modelRaw := msgObj["model"]; modelRaw != nil {
		json.Unmarshal(modelRaw, &e.model)
	}

	if usageRaw := msgObj["usage"]; usageRaw != nil {
		var usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		}
		json.Unmarshal(usageRaw, &usage)
		e.tokensIn = usage.InputTokens
		e.tokensOut = usage.OutputTokens
	}

	var blocks []map[string]interface{}
	if err := json.Unmarshal(msgObj["content"], &blocks); err != nil {
		return
	}

	for _, block := range blocks {
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			text, _ := block["text"].(string)
			text = strings.TrimSpace(text)
			if text != "" {
				e.blocks = append(e.blocks, contentBlock{
					blockType: "text",
					text:      text,
				})
			}
		case "tool_use":
			name, _ := block["name"].(string)
			id, _ := block["id"].(string)
			snippet := ""
			var editOld, editNew string
			if input, ok := block["input"].(map[string]interface{}); ok {
				snippet = toolInputSnippet(name, input)
				// Extract Edit diff data
				if name == "Edit" {
					if old, ok := input["old_string"].(string); ok {
						editOld = old
					}
					if ns, ok := input["new_string"].(string); ok {
						editNew = ns
					}
				}
			}
			e.blocks = append(e.blocks, contentBlock{
				blockType:     "tool_use",
				toolName:      name,
				toolSnippet:   snippet,
				toolUseID:     id,
				editOldString: editOld,
				editNewString: editNew,
			})
		}
	}
}

// --- Helpers ---

func formatTokenCount(n int64) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func toolInputSnippet(toolName string, input map[string]interface{}) string {
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 60 {
				cmd = cmd[:57] + "..."
			}
			return "$ " + cmd
		}
	case "Read":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Write":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Edit":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Grep":
		if pattern, ok := input["pattern"].(string); ok {
			return "/" + pattern + "/"
		}
	case "Glob":
		if pattern, ok := input["pattern"].(string); ok {
			return pattern
		}
	case "Task":
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	case "WebSearch":
		if query, ok := input["query"].(string); ok {
			return query
		}
	case "WebFetch":
		if url, ok := input["url"].(string); ok {
			if len(url) > 50 {
				url = url[:47] + "..."
			}
			return url
		}
	}
	return ""
}

// --- Codex JSONL parsing ---

// parseCodexJSONL parses Codex CLI session JSONL into TraceTurns.
// Codex format uses: session_meta, event_msg (user_message, token_count),
// response_item (message role=assistant, function_call, function_call_output).
func parseCodexJSONL(data string) []TraceTurn {
	var turns []TraceTurn
	var current *TraceTurn
	turnNum := 0
	pendingCalls := make(map[string]*ToolSpan) // call_id -> span

	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		var entryType string
		json.Unmarshal(raw["type"], &entryType)

		var ts time.Time
		var tsStr string
		if err := json.Unmarshal(raw["timestamp"], &tsStr); err == nil {
			ts, _ = time.Parse(time.RFC3339Nano, tsStr)
		}

		switch entryType {
		case "event_msg":
			var payload struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Info    *struct {
					TotalTokenUsage *struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			}
			json.Unmarshal(raw["payload"], &payload)

			if payload.Type == "user_message" && payload.Message != "" {
				// New user turn
				if current != nil {
					turns = append(turns, *current)
				}
				turnNum++
				current = &TraceTurn{
					Number:    turnNum,
					Timestamp: ts,
				}
				for _, l := range strings.Split(payload.Message, "\n") {
					trimmed := strings.TrimSpace(l)
					if trimmed != "" {
						current.UserLines = append(current.UserLines, trimmed)
					}
				}
			}

			if payload.Type == "token_count" && payload.Info != nil && payload.Info.TotalTokenUsage != nil && current != nil {
				current.TokensIn = payload.Info.TotalTokenUsage.InputTokens
				current.TokensOut = payload.Info.TotalTokenUsage.OutputTokens
			}

		case "response_item":
			var payload struct {
				Type      string `json:"type"`
				Role      string `json:"role"`
				Name      string `json:"name"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
				Output    string `json:"output"`
				Content   json.RawMessage `json:"content"`
			}
			json.Unmarshal(raw["payload"], &payload)

			if current == nil {
				turnNum++
				current = &TraceTurn{Number: turnNum, Timestamp: ts}
			}

			if !ts.IsZero() {
				current.EndTime = ts
			}

			switch payload.Type {
			case "message":
				if payload.Role == "assistant" {
					// Extract text from content blocks
					var contentBlocks []map[string]interface{}
					if err := json.Unmarshal(payload.Content, &contentBlocks); err == nil {
						for _, block := range contentBlocks {
							if blockType, _ := block["type"].(string); blockType == "output_text" {
								if text, _ := block["text"].(string); text != "" {
									for _, l := range strings.Split(text, "\n") {
										trimmed := strings.TrimSpace(l)
										if trimmed != "" {
											current.OutputLines = append(current.OutputLines, trimmed)
										}
									}
								}
							}
						}
					}
				}

			case "function_call":
				name := payload.Name
				snippet := ""
				// Try to parse arguments as JSON for a snippet
				if payload.Arguments != "" {
					var args map[string]interface{}
					if err := json.Unmarshal([]byte(payload.Arguments), &args); err == nil {
						if cmd, ok := args["cmd"].(string); ok {
							cmd = strings.TrimSpace(cmd)
							if len(cmd) > 60 {
								cmd = cmd[:57] + "..."
							}
							snippet = "$ " + cmd
						} else if path, ok := args["file_path"].(string); ok {
							snippet = path
						}
					} else {
						// Arguments is a plain string
						s := payload.Arguments
						if len(s) > 60 {
							s = s[:57] + "..."
						}
						snippet = s
					}
				}

				// Map Codex tool names to shorter display names
				displayName := codexToolName(name)

				span := ToolSpan{
					Name:      displayName,
					Snippet:   snippet,
					Success:   true,
					toolUseID: payload.CallID,
				}
				current.Actions = append(current.Actions, span)
				if payload.CallID != "" {
					idx := len(current.Actions) - 1
					pendingCalls[payload.CallID] = &current.Actions[idx]
				}

			case "function_call_output":
				if payload.CallID != "" {
					if span, ok := pendingCalls[payload.CallID]; ok {
						output := payload.Output
						if strings.Contains(strings.ToLower(output), "error") ||
							strings.Contains(output, "Process exited with code 1") {
							span.Success = false
							if len(output) > 200 {
								output = output[:200]
							}
							span.ErrorMsg = output
						}
						delete(pendingCalls, payload.CallID)
					}
				}
			}
		}
	}

	if current != nil {
		turns = append(turns, *current)
	}

	// Calculate per-turn cost (Codex uses GPT models)
	for i := range turns {
		turns[i].CostUSD = estimateTurnCost("gpt", turns[i].TokensIn, turns[i].TokensOut)
	}

	return turns
}

// codexToolName maps Codex function names to shorter display names.
func codexToolName(name string) string {
	switch name {
	case "exec_command", "shell":
		return "Bash"
	case "read_file":
		return "Read"
	case "write_file":
		return "Write"
	case "apply_patch", "edit_file":
		return "Edit"
	case "search_files", "grep":
		return "Grep"
	case "list_directory", "ls":
		return "Ls"
	default:
		if len(name) > 12 {
			return name[:12]
		}
		return name
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
