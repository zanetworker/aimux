package views

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/history"
)

// --- Styles ---

var (
	sessHeaderStyle = lipgloss.NewStyle().
			Bold(true).Foreground(lipgloss.Color("#E5E7EB"))
	sessSelectedStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1E3A5F"))
	sessPromptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB"))
	sessAgeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF"))
	sessCostStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F59E0B"))
	sessTurnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06B6D4"))
	sessDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
	sessProjectStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#A78BFA"))
	sessTagStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F59E0B")).Italic(true)

	sessAnnotAchievedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#111827")).
				Background(lipgloss.Color("#22C55E")).Bold(true)
	sessAnnotPartialStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#111827")).
				Background(lipgloss.Color("#F59E0B")).Bold(true)
	sessAnnotFailedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#111827")).
				Background(lipgloss.Color("#EF4444")).Bold(true)
	sessAnnotAbandonedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#111827")).
				Background(lipgloss.Color("#6B7280")).Bold(true)

	sessViewOnlyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B7280")).Italic(true)
)

// --- Messages ---

// SessionResumeMsg is emitted when the user wants to resume a session.
type SessionResumeMsg struct {
	SessionID  string
	WorkingDir string
	FilePath   string // path to the session JSONL (for trace pane)
}

// SessionAnnotateMsg is emitted when the user changes a session annotation.
type SessionAnnotateMsg struct {
	Session    history.Session
	Annotation string
}

// SessionTagMsg is emitted when the user updates tags on a session.
type SessionTagMsg struct {
	Session history.Session
	Tags    []string
}

// SessionNoteMsg is emitted when the user updates the note on a session.
type SessionNoteMsg struct {
	Session history.Session
	Note    string
}

// SessionToggleScopeMsg is emitted when the user toggles between
// current directory and all projects.
type SessionToggleScopeMsg struct {
	ShowAll bool
}

// --- SessionsView ---

// SessionsView renders a browsable list of past sessions with trace preview.
type SessionsView struct {
	sessions   []history.Session
	width      int
	height     int
	cursor     int
	showAll    bool   // true = all projects, false = current dir only
	currentDir string // scoped directory (empty = all)

	// Filter
	filterMode  bool
	filterInput string
	filterText  string

	// Tag input
	tagMode     bool
	tagInput    string
	tagVocab    []string // autocomplete vocabulary

	// Note input
	noteMode  bool
	noteInput string

	// Trace preview (reused LogsView)
	previewLogs  *LogsView
	traceParser  TraceParser
}

// NewSessionsView creates a new sessions browser.
func NewSessionsView() *SessionsView {
	return &SessionsView{}
}

// SetSessions updates the session list.
func (v *SessionsView) SetSessions(sessions []history.Session) {
	v.sessions = sessions
	if v.cursor >= len(sessions) {
		v.cursor = len(sessions) - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

// SetSize sets the available width and height.
func (v *SessionsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// CurrentDir returns the scoped directory.
func (v *SessionsView) CurrentDir() string {
	return v.currentDir
}

// SetCurrentDir sets the scoped directory for display.
func (v *SessionsView) SetCurrentDir(dir string) {
	v.currentDir = dir
}

// SetTraceParser sets the parser used for trace preview.
func (v *SessionsView) SetTraceParser(parser TraceParser) {
	v.traceParser = parser
}

// SetTagVocab sets the autocomplete vocabulary for tag input.
func (v *SessionsView) SetTagVocab(vocab []string) {
	v.tagVocab = vocab
}

// ShowAll returns whether the view is showing all projects.
func (v *SessionsView) ShowAll() bool {
	return v.showAll
}

// SelectedSession returns the currently selected session, if any.
// Returns a pointer into the original sessions slice so mutations persist.
func (v *SessionsView) SelectedSession() *history.Session {
	visible := v.visibleSessions()
	if len(visible) == 0 || v.cursor >= len(visible) {
		return nil
	}
	// Find the matching session in the original slice by ID
	targetID := visible[v.cursor].ID
	for i := range v.sessions {
		if v.sessions[i].ID == targetID {
			return &v.sessions[i]
		}
	}
	return nil
}

// HasActiveInput returns true if the view has active text input.
func (v *SessionsView) HasActiveInput() bool {
	return v.filterMode || v.tagMode || v.noteMode
}

// HasActiveFilter returns true if a search filter is currently applied.
func (v *SessionsView) HasActiveFilter() bool {
	return v.filterText != ""
}

// Annotation cycle for sessions
var sessAnnotationCycle = []string{"achieved", "partial", "failed", "abandoned", ""}

// Update handles key messages for session navigation.
func (v *SessionsView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if v.tagMode {
			return v.handleTagKey(msg)
		}
		if v.noteMode {
			return v.handleNoteKey(msg)
		}
		if v.filterMode {
			return v.handleFilterKey(msg)
		}

		visible := v.visibleSessions()

		switch msg.String() {
		case "j", "down":
			if v.cursor < len(visible)-1 {
				v.cursor++
				v.previewLogs = nil // reset preview
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
				v.previewLogs = nil
			}
		case "g":
			v.cursor = 0
			v.previewLogs = nil
		case "G":
			if len(visible) > 0 {
				v.cursor = len(visible) - 1
				v.previewLogs = nil
			}
		case "esc":
			// Clear filter first, then let app handle navigation
			if v.filterText != "" {
				v.filterText = ""
				v.cursor = 0
				return nil
			}
			// Return nil — app.go will handle navigateBack
			return nil
		case "A":
			v.showAll = !v.showAll
			v.cursor = 0
			v.previewLogs = nil
			return func() tea.Msg {
				return SessionToggleScopeMsg{ShowAll: v.showAll}
			}
		case "/":
			v.filterMode = true
			v.filterInput = ""
		case "enter":
			s := v.SelectedSession()
			if s != nil && s.Resumable {
				return func() tea.Msg {
					return SessionResumeMsg{
						SessionID:  s.ID,
						WorkingDir: s.Project,
						FilePath:   s.FilePath,
					}
				}
			}
		case "a", "v":
			// Cycle annotation
			s := v.SelectedSession()
			if s == nil {
				return nil
			}
			current := s.Annotation
			next := ""
			for i, label := range sessAnnotationCycle {
				if label == current {
					next = sessAnnotationCycle[(i+1)%len(sessAnnotationCycle)]
					break
				}
			}
			if next == "" && current == "" {
				next = "achieved"
			}
			s.Annotation = next
			return func() tea.Msg {
				return SessionAnnotateMsg{Session: *s, Annotation: next}
			}
		case "f":
			s := v.SelectedSession()
			if s == nil {
				return nil
			}
			v.tagMode = true
			if len(s.Tags) > 0 {
				v.tagInput = strings.Join(s.Tags, ", ")
			} else {
				v.tagInput = ""
			}
		case "N":
			s := v.SelectedSession()
			if s == nil {
				return nil
			}
			v.noteMode = true
			v.noteInput = s.Note
		case "p":
			// Toggle trace preview
			if v.previewLogs != nil {
				v.previewLogs = nil
			} else {
				v.loadPreview()
			}
		}
	}
	return nil
}

func (v *SessionsView) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.filterMode = false
		v.filterText = v.filterInput
		v.cursor = 0
	case "esc":
		v.filterMode = false
		v.filterInput = ""
	case "backspace":
		if len(v.filterInput) > 0 {
			v.filterInput = v.filterInput[:len(v.filterInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			v.filterInput += msg.String()
		}
	}
	return nil
}

func (v *SessionsView) handleTagKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.tagMode = false
		s := v.SelectedSession()
		if s == nil {
			return nil
		}
		tags := parseTags(v.tagInput)
		s.Tags = tags
		return func() tea.Msg {
			return SessionTagMsg{Session: *s, Tags: tags}
		}
	case "esc":
		v.tagMode = false
		v.tagInput = ""
	case "tab":
		// Autocomplete from vocabulary
		v.autocompleteTag()
	case "backspace":
		if len(v.tagInput) > 0 {
			v.tagInput = v.tagInput[:len(v.tagInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			v.tagInput += msg.String()
		}
	}
	return nil
}

func (v *SessionsView) handleNoteKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.noteMode = false
		s := v.SelectedSession()
		if s == nil {
			return nil
		}
		s.Note = v.noteInput
		return func() tea.Msg {
			return SessionNoteMsg{Session: *s, Note: v.noteInput}
		}
	case "esc":
		v.noteMode = false
		v.noteInput = ""
	case "backspace":
		if len(v.noteInput) > 0 {
			v.noteInput = v.noteInput[:len(v.noteInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			v.noteInput += msg.String()
		}
	}
	return nil
}

// autocompleteTag completes the current tag word from the vocabulary.
func (v *SessionsView) autocompleteTag() {
	if len(v.tagVocab) == 0 {
		return
	}
	// Get the current partial tag (after the last comma)
	parts := strings.Split(v.tagInput, ",")
	current := strings.TrimSpace(parts[len(parts)-1])
	if current == "" {
		return
	}

	lower := strings.ToLower(current)
	for _, tag := range v.tagVocab {
		if strings.HasPrefix(strings.ToLower(tag), lower) && tag != current {
			parts[len(parts)-1] = " " + tag
			v.tagInput = strings.Join(parts, ",")
			return
		}
	}
}

// parseTags splits a comma-separated tag string into trimmed, non-empty tags.
func parseTags(input string) []string {
	parts := strings.Split(input, ",")
	var tags []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			tags = append(tags, p)
		}
	}
	return tags
}

// visibleSessions returns sessions matching the current filter.
// Near-empty sessions (1 turn, $0 cost) are hidden unless a filter is active.
func (v *SessionsView) visibleSessions() []history.Session {
	var result []history.Session
	for _, s := range v.sessions {
		// Hide near-empty sessions (auto-memory, system operations) unless searching
		if v.filterText == "" && s.CostUSD == 0 && s.TurnCount <= 5 {
			continue
		}
		// Hide sessions with no timestamps (broken/incomplete files)
		if v.filterText == "" && s.LastActive.IsZero() {
			continue
		}
		if v.filterText != "" {
			needle := strings.ToLower(v.filterText)
			if !sessionMatchesFilter(s, needle) {
				continue
			}
		}
		result = append(result, s)
	}
	return result
}

func sessionMatchesFilter(s history.Session, needle string) bool {
	if strings.Contains(strings.ToLower(s.FirstPrompt), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(s.Project), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(s.Annotation), needle) {
		return true
	}
	for _, tag := range s.Tags {
		if strings.Contains(strings.ToLower(tag), needle) {
			return true
		}
	}
	return false
}

// loadPreview creates a LogsView for the selected session's trace.
func (v *SessionsView) loadPreview() {
	s := v.SelectedSession()
	if s == nil || v.traceParser == nil || s.FilePath == "" {
		return
	}
	lv := NewLogsView(0, s.FilePath, v.traceParser)
	lv.compact = true
	previewH := v.height / 2
	if previewH < 5 {
		previewH = 5
	}
	lv.SetSize(v.width-4, previewH)
	// Expand the last turn for preview
	if len(lv.Turns()) > 0 {
		last := lv.Turns()[len(lv.Turns())-1]
		lv.expanded = map[int]bool{last.Number: true}
		lv.cursor = len(lv.Turns()) - 1
	}
	v.previewLogs = lv
}

// View renders the sessions browser.
func (v *SessionsView) View() string {
	visible := v.visibleSessions()
	w := v.width
	if w < 20 {
		w = 80
	}

	var b strings.Builder

	// Header
	scope := v.currentDir
	if v.showAll || scope == "" {
		scope = "all projects"
	} else if len(scope) > 40 {
		scope = "..." + scope[len(scope)-37:]
	}
	headerLine := fmt.Sprintf("  Sessions ─ %s", scope)
	countStr := fmt.Sprintf("  %d sessions", len(visible))
	if len(visible) < len(v.sessions) {
		countStr += sessDimStyle.Render(fmt.Sprintf("  (%d total)", len(v.sessions)))
	}
	if !v.showAll && v.currentDir != "" {
		countStr += sessDimStyle.Render("  press A for all projects")
	}
	b.WriteString(sessHeaderStyle.Render(headerLine) + "\n")
	b.WriteString(sessDimStyle.Render(countStr) + "\n")
	b.WriteString("\n")

	// Filter indicator
	if v.filterText != "" {
		b.WriteString("  " + filterActiveStyle.Render(" FILTER: "+v.filterText+" ") + "\n\n")
	}

	if len(visible) == 0 {
		b.WriteString(sessDimStyle.Render("  No sessions found.") + "\n")
		return b.String()
	}

	// Session list - calculate how many rows fit
	listHeight := v.height - 6 // header + padding
	if v.previewLogs != nil {
		listHeight = v.height / 2
	}
	if listHeight < 3 {
		listHeight = 3
	}

	// Scroll window around cursor
	start := 0
	if v.cursor >= listHeight {
		start = v.cursor - listHeight + 1
	}
	end := start + listHeight
	if end > len(visible) {
		end = len(visible)
	}

	for i := start; i < end; i++ {
		s := visible[i]
		selected := i == v.cursor

		line := v.renderSessionRow(s, selected, w)
		b.WriteString(line + "\n")
	}

	// Preview pane
	if v.previewLogs != nil {
		b.WriteString("\n")
		sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))
		b.WriteString(sepStyle.Render("  " + strings.Repeat("─", w-4)) + "\n")
		b.WriteString(v.previewLogs.View())
	}

	// Input modes
	if v.filterMode {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true).Render("/") + v.filterInput + lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Render("|"))
	}
	if v.tagMode {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true).Render("Tags: ") + v.tagInput + lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render("|"))
		// Show autocomplete suggestions
		suggestions := v.tagSuggestions()
		if len(suggestions) > 0 {
			b.WriteString("\n  " + sessDimStyle.Render("  "+strings.Join(suggestions, "  ")))
		}
	}
	if v.noteMode {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true).Render("Note: ") + v.noteInput + lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render("|"))
	}

	return b.String()
}

// tagSuggestions returns matching tags from the vocabulary for autocomplete.
func (v *SessionsView) tagSuggestions() []string {
	if len(v.tagVocab) == 0 {
		return nil
	}
	parts := strings.Split(v.tagInput, ",")
	current := strings.TrimSpace(parts[len(parts)-1])
	if current == "" {
		// Show all vocabulary when no partial input
		if len(v.tagVocab) > 8 {
			return v.tagVocab[:8]
		}
		return v.tagVocab
	}

	lower := strings.ToLower(current)
	var matches []string
	for _, tag := range v.tagVocab {
		if strings.Contains(strings.ToLower(tag), lower) {
			matches = append(matches, tag)
			if len(matches) >= 5 {
				break
			}
		}
	}
	return matches
}

// renderSessionRow renders a single session row as a clean, columnar line.
func (v *SessionsView) renderSessionRow(s history.Session, selected bool, w int) string {
	isEmpty := s.TurnCount <= 1 && s.CostUSD == 0

	marker := "  "
	if selected {
		marker = " ▸"
	}

	age := formatAge(s.LastActive)

	// Use LLM-generated title if available, fall back to first prompt
	prompt := s.Title
	if prompt == "" {
		prompt = s.FirstPrompt
	}
	if prompt == "" {
		prompt = "(no prompt)"
	}
	// Clean up prompts that start with markdown headers
	prompt = strings.TrimLeft(prompt, "# ")

	// Layout: marker | age | [project] | prompt ... | turns | cost | [annotation] [tags]
	// Fixed columns: marker(2) + age(8) + turns(5) + cost(7) + spacing(6) = ~28
	metaW := 28
	if v.showAll {
		metaW += 14 // project column
	}
	promptW := w - metaW
	if promptW < 15 {
		promptW = 15
	}

	var b strings.Builder
	b.WriteString(marker + " ")

	// Age column
	ageStr := fmt.Sprintf("%-7s", age)
	if isEmpty {
		b.WriteString(sessDimStyle.Render(ageStr))
	} else {
		b.WriteString(sessAgeStyle.Render(ageStr))
	}
	b.WriteString(" ")

	// Project column (all-projects mode only)
	if v.showAll {
		proj := shortProject(s.Project)
		projStr := fmt.Sprintf("%-12s", truncate(proj, 12))
		b.WriteString(sessProjectStyle.Render(projStr))
		b.WriteString(" ")
	}

	// Prompt — the main content
	truncPrompt := truncate(prompt, promptW)
	if isEmpty {
		b.WriteString(sessDimStyle.Render(truncPrompt))
	} else {
		b.WriteString(sessPromptStyle.Render(truncPrompt))
	}

	// Right-aligned metadata: turns + cost
	rightParts := []string{}
	turnStr := fmt.Sprintf("%dt", s.TurnCount)
	costStr := fmt.Sprintf("$%.2f", s.CostUSD)
	if isEmpty {
		rightParts = append(rightParts, sessDimStyle.Render(turnStr))
		rightParts = append(rightParts, sessDimStyle.Render(costStr))
	} else {
		rightParts = append(rightParts, sessTurnStyle.Render(turnStr))
		rightParts = append(rightParts, sessCostStyle.Render(costStr))
	}

	// Annotation badge
	if s.Annotation != "" {
		rightParts = append(rightParts, renderSessionAnnotation(s.Annotation))
	}

	// Tags (compact)
	if len(s.Tags) > 0 {
		tagStr := strings.Join(s.Tags, ",")
		if len(tagStr) > 20 {
			tagStr = tagStr[:17] + "..."
		}
		rightParts = append(rightParts, sessTagStyle.Render(tagStr))
	}

	// View-only badge
	if !s.Resumable {
		rightParts = append(rightParts, sessViewOnlyStyle.Render("(view)"))
	}

	b.WriteString("  ")
	b.WriteString(strings.Join(rightParts, " "))

	line := b.String()

	if selected {
		return sessSelectedStyle.Render(padRight(line, w))
	}
	return line
}

func renderSessionAnnotation(label string) string {
	tag := strings.ToUpper(label)
	switch label {
	case "achieved":
		return sessAnnotAchievedStyle.Render("[" + tag + "]")
	case "partial":
		return sessAnnotPartialStyle.Render("[" + tag + "]")
	case "failed":
		return sessAnnotFailedStyle.Render("[" + tag + "]")
	case "abandoned":
		return sessAnnotAbandonedStyle.Render("[" + tag + "]")
	default:
		return sessDimStyle.Render("[" + tag + "]")
	}
}

// formatAge returns a human-readable age string like "2h ago", "3d ago".
func formatAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	}
}

// shortProject extracts a meaningful short name from a project path.
// Handles real paths ("/Users/foo/myproject") and
// encoded paths ("/Users-foo-myproject" from decodeProjectDir).
func shortProject(path string) string {
	if path == "" {
		return "(unknown)"
	}
	// For real paths with multiple slashes, take the last path component
	if strings.Count(path, "/") > 1 {
		parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] != "" && parts[i] != "." {
				return parts[i]
			}
		}
	}
	// For encoded paths (hyphens instead of slashes), take last segment
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimPrefix(path, "-")
	parts := strings.Split(path, "-")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return path
}
