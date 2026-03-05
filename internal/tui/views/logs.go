package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/trace"
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
	Note  string // free-text rationale (set via 'n' key)
}

// --- Data model ---

// TraceTurn is an alias for trace.Turn, kept for backward compatibility
// within the views package. All parsing now happens in the provider layer.
type TraceTurn = trace.Turn

// ToolSpan is an alias for trace.ToolSpan, kept for backward compatibility
// within the views package.
type ToolSpan = trace.ToolSpan

// --- LogsView ---

// TraceParser is a function that reads a file and returns parsed trace turns.
// Each provider supplies its own parser via Provider.ParseTrace.
type TraceParser func(filePath string) ([]trace.Turn, error)

// LogsView renders a structured conversation trace grouped by turns.
// Each turn shows INPUT (user), ACTIONS (tools), and OUTPUT (assistant)
// sections inspired by MLflow/Braintrust trace UIs.
type LogsView struct {
	turns        []TraceTurn
	filePath     string
	parser       TraceParser
	sessionCost  float64 // session-level cost from agent.EstCostUSD
	width        int
	height       int
	cursor       int
	expanded     map[int]bool
	pid          int
	filterText   string
	filterMode   bool
	filterInput  string
	annotations  map[int]string
	notes        map[int]string // free-text notes per turn
	noteMode     bool           // true when typing a note
	noteInput    string         // current note input text
	noteTurn     int            // which turn the note is for
	compact      bool           // when true, hides the interactive status bar (for preview pane)
	scrollOffset int            // line-level scroll offset within the rendered view
	warning      string         // provider-specific warning shown above turns
}

// NewLogsView creates a new LogsView for the given PID and log file path.
// The parser function is called during Reload() to parse the session file
// into structured trace turns. If parser is nil, Reload returns no turns.
func NewLogsView(pid int, filePath string, parser TraceParser) *LogsView {
	v := &LogsView{
		pid:         pid,
		filePath:    filePath,
		parser:      parser,
		expanded:    make(map[int]bool),
		annotations: make(map[int]string),
		notes:       make(map[int]string),
	}
	v.Reload()
	return v
}

// FilePath returns the current file path for the trace data source.
func (v *LogsView) FilePath() string {
	return v.filePath
}

// SetWarning sets a provider-specific warning shown above the trace turns.
func (v *LogsView) SetWarning(msg string) {
	v.warning = msg
}

// SetFilePath updates the file path for the trace data source.
// Used when the session file isn't available at creation time
// but is discovered later (e.g., newly launched agents).
func (v *LogsView) SetFilePath(path string) {
	v.filePath = path
}

// SetSessionCost sets the session-level cost for the trace summary.
func (v *LogsView) SetSessionCost(cost float64) {
	v.sessionCost = cost
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

// HasActiveFilter returns true if the logs view has a filter, note input, or filter mode active.
func (v *LogsView) HasActiveFilter() bool {
	return v.filterMode || v.filterText != "" || v.noteMode
}

// SetNotes loads notes from external storage.
func (v *LogsView) SetNotes(n map[int]string) {
	if n == nil {
		v.notes = make(map[int]string)
	} else {
		v.notes = n
	}
}

// Notes returns the current notes map for export.
func (v *LogsView) Notes() map[int]string {
	return v.notes
}

// NoteMode returns true if the user is currently typing a note.
func (v *LogsView) NoteMode() bool {
	return v.noteMode
}

// NoteInput returns the current note input text and turn number.
func (v *LogsView) NoteInput() (string, int) {
	return v.noteInput, v.noteTurn
}

// Annotations returns the current annotations map.
func (v *LogsView) Annotations() map[int]string {
	return v.annotations
}

// SetAnnotations loads a set of turn annotations from external storage.
func (v *LogsView) SetAnnotations(a map[int]string) {
	if a == nil {
		v.annotations = make(map[int]string)
	} else {
		v.annotations = a
	}
}

// Reload reads and parses the session file into turns using the provider's
// parser. Preserves the cursor position unless new turns were added while
// the cursor was at the bottom (auto-follow mode).
func (v *LogsView) Reload() {
	if v.parser == nil {
		v.turns = nil
		return
	}

	turns, err := v.parser(v.filePath)
	if err != nil {
		v.turns = nil
		return
	}

	prevCount := len(v.turns)
	prevCursor := v.cursor
	atBottom := prevCursor >= prevCount-1

	v.turns = turns

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
		// Note input mode
		if v.noteMode {
			return v.handleNoteKey(msg)
		}
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
			// If current turn is expanded, scroll line-by-line within it
			if v.cursor < len(visible) && v.expanded[visible[v.cursor].Number] {
				v.scrollOffset++
				// Clamping happens in View()
			} else if v.cursor < len(visible)-1 {
				v.cursor++
				v.scrollOffset = 0
			} else {
				// Wrap to first turn
				v.cursor = 0
				v.scrollOffset = 0
			}
		case "k", "up":
			if v.cursor < len(visible) && v.expanded[visible[v.cursor].Number] && v.scrollOffset > 0 {
				v.scrollOffset--
			} else if v.cursor > 0 {
				v.cursor--
				v.scrollOffset = 0
			} else {
				// Wrap to last turn
				v.cursor = len(visible) - 1
				v.scrollOffset = 0
			}
		case "J":
			// Fast scroll down (10 lines) within expanded turn
			if v.cursor < len(visible) && v.expanded[visible[v.cursor].Number] {
				v.scrollOffset += 10
			}
		case "K":
			// Fast scroll up (10 lines) within expanded turn
			if v.cursor < len(visible) && v.expanded[visible[v.cursor].Number] && v.scrollOffset > 0 {
				v.scrollOffset -= 10
				if v.scrollOffset < 0 {
					v.scrollOffset = 0
				}
			}
		case "n":
			// Jump to next turn, wrap to first
			v.scrollOffset = 0
			if v.cursor < len(visible)-1 {
				v.cursor++
			} else {
				v.cursor = 0
			}
		case "p":
			// Jump to previous turn, wrap to last
			v.scrollOffset = 0
			if v.cursor > 0 {
				v.cursor--
			} else {
				v.cursor = len(visible) - 1
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
		case "enter":
			if len(visible) > 0 && v.cursor < len(visible) {
				turnNum := visible[v.cursor].Number
				v.expanded[turnNum] = !v.expanded[turnNum]
				v.scrollOffset = 0 // reset scroll when toggling
			}
		case " ":
			// Space jumps to next turn, wrap to first
			v.scrollOffset = 0
			if v.cursor < len(visible)-1 {
				v.cursor++
			} else {
				v.cursor = 0
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
		case "N":
			// Open note input for current turn
			if len(visible) == 0 || v.cursor >= len(visible) {
				return nil
			}
			turnNum := visible[v.cursor].Number
			// Must have an annotation label first
			if v.annotations[turnNum] == "" {
				return nil
			}
			v.noteMode = true
			v.noteTurn = turnNum
			v.noteInput = v.notes[turnNum] // pre-fill with existing note
			return nil
		}
	}
	return nil
}

// handleNoteKey processes keystrokes while in note input mode.
func (v *LogsView) handleNoteKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.noteMode = false
		turnNum := v.noteTurn
		note := v.noteInput
		if note != "" {
			v.notes[turnNum] = note
		} else {
			delete(v.notes, turnNum)
		}
		label := v.annotations[turnNum]
		return func() tea.Msg {
			return AnnotationMsg{Turn: turnNum, Label: label, Note: note}
		}
	case "esc":
		v.noteMode = false
		v.noteInput = ""
		return nil
	case "backspace":
		if len(v.noteInput) > 0 {
			v.noteInput = v.noteInput[:len(v.noteInput)-1]
		}
		return nil
	default:
		if len(msg.String()) == 1 {
			v.noteInput += msg.String()
		}
		return nil
	}
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

	// Provider warning (e.g., Gemini trace limitations)
	if v.warning != "" {
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
		allLines = append(allLines, warnStyle.Render("  "+v.warning))
		allLines = append(allLines, "") // spacer
	}

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
	toolCounts := make(map[string]int)

	for _, t := range v.turns {
		for _, a := range t.Actions {
			totalActions++
			toolCounts[a.Name]++
			if !a.Success {
				totalErrors++
			}
		}
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

	// Use session-level cost if available (includes cache pricing);
	// fall back to sum of per-turn costs.
	displayCost := v.sessionCost
	if displayCost <= 0 {
		for _, t := range v.turns {
			displayCost += t.CostUSD
		}
	}
	parts = append(parts, costStyle.Render(fmt.Sprintf("$%.2f total", displayCost)))

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
		lines = append(lines, renderMarkdownLines(t.OutputLines, innerW)...)
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

	// Annotation badge + note
	if label, ok := v.annotations[t.Number]; ok && label != "" {
		badge := renderAnnotationBadge(label)
		if note, hasNote := v.notes[t.Number]; hasNote && note != "" {
			noteStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Italic(true)
			truncNote := note
			if len(truncNote) > 30 {
				truncNote = truncNote[:27] + "..."
			}
			badge += " " + noteStyle.Render("\""+truncNote+"\"")
		}
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
