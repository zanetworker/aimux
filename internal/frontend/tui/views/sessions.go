package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/history"
)

// SortField identifies which column to sort by.
type SortField int

const (
	SortByAge         SortField = iota // default: most recent first
	SortByCost                        // highest cost first
	SortByTurns                       // most turns first
	SortByTitle                       // alphabetical by title/prompt
	SortByFailureMode                 // tagged sessions first
)

// sortFieldNames maps sort fields to display names.
var sortFieldNames = map[SortField]string{
	SortByAge:         "AGE",
	SortByCost:        "COST",
	SortByTurns:       "TURNS",
	SortByTitle:       "TITLE",
	SortByFailureMode: "FAIL",
}

// sortFieldOrder defines the cycle order when pressing 's'.
var sortFieldOrder = []SortField{SortByAge, SortByCost, SortByTurns, SortByTitle, SortByFailureMode}

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

// SessionDeleteMsg is emitted when the user confirms deleting a session.
type SessionDeleteMsg struct {
	Session history.Session
}

// SessionToggleScopeMsg is emitted when the user toggles between
// current directory and all projects.
type SessionToggleScopeMsg struct {
	ShowAll bool
}

// SessionBulkDeleteMsg is emitted when the user confirms bulk deletion.
type SessionBulkDeleteMsg struct {
	Sessions []history.Session
}

// SessionContentSearchResultMsg carries the results of a deep content search.
type SessionContentSearchResultMsg struct {
	Matches []history.ContentMatch
	Query   string
}

// cleanupItem represents a session flagged for potential cleanup.
type cleanupItem struct {
	session  history.Session
	reason   string // "duplicate" or "empty"
	selected bool
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
	filterInput TextInput
	filterText  string

	// Tag input
	tagMode     bool
	tagInput    TextInput
	tagVocab    []string // autocomplete vocabulary
	tagCursor   int      // selected suggestion index (-1 = typing custom)

	// Note input
	noteMode  bool
	noteInput TextInput

	// Sort
	sortField SortField // current sort column
	sortAsc   bool      // true = ascending, false = descending

	// Delete confirmation
	deleteMode bool // true when showing delete confirmation

	// Cleanup mode
	cleanupMode   bool
	cleanupItems  []cleanupItem
	cleanupCursor int

	// Content search (deep search inside JSONL files)
	contentSearchMode  bool
	contentSearchInput TextInput
	contentSearchIDs   map[string]string // session ID -> snippet (nil = no active search)

	// Subagent filtering
	showSubagents bool

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

// HasActiveInput returns true if the view has active text input or confirmation.
func (v *SessionsView) HasActiveInput() bool {
	return v.filterMode || v.tagMode || v.noteMode || v.deleteMode || v.cleanupMode || v.contentSearchMode
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
		if v.cleanupMode {
			return v.handleCleanupKey(msg)
		}
		if v.deleteMode {
			return v.handleDeleteKey(msg)
		}
		if v.tagMode {
			return v.handleTagKey(msg)
		}
		if v.noteMode {
			return v.handleNoteKey(msg)
		}
		if v.contentSearchMode {
			return v.handleContentSearchKey(msg)
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
			// Clear filter and content search together
			if v.filterText != "" || v.contentSearchIDs != nil {
				v.filterText = ""
				v.contentSearchIDs = nil
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
			v.filterInput.Reset()
			// Clear previous search results so new search starts fresh
			v.filterText = ""
			v.contentSearchIDs = nil
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
			v.tagCursor = -1
			if len(s.Tags) > 0 {
				v.tagInput.SetValue(strings.Join(s.Tags, ", "))
			} else {
				v.tagInput.Reset()
			}
		case "N":
			s := v.SelectedSession()
			if s == nil {
				return nil
			}
			v.noteMode = true
			v.noteInput.SetValue(s.Note)
		case "d":
			// Delete session (show confirmation)
			s := v.SelectedSession()
			if s == nil {
				return nil
			}
			v.deleteMode = true
		case "D":
			v.enterCleanupMode()
		case "s":
			// Cycle sort field; pressing again on same field toggles direction
			v.cycleSortField()
			v.cursor = 0
			v.previewLogs = nil
		case "p":
			// Toggle trace preview
			if v.previewLogs != nil {
				v.previewLogs = nil
			} else {
				v.loadPreview()
			}
		case "F":
			// Standalone deep content search inside session JSONL files
			v.contentSearchMode = true
			v.contentSearchInput.Reset()
		case "H":
			v.showSubagents = !v.showSubagents
			v.cursor = 0
			v.previewLogs = nil
		}
	}
	return nil
}

func (v *SessionsView) handleContentSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.contentSearchMode = false
		query := v.contentSearchInput.Value()
		if query == "" {
			return nil
		}
		return func() tea.Msg {
			matches, err := history.SearchContentWithSnippets(query, "")
			if err != nil {
				return SessionContentSearchResultMsg{Query: query}
			}
			return SessionContentSearchResultMsg{Matches: matches, Query: query}
		}
	case "esc":
		v.contentSearchMode = false
		v.contentSearchInput.Reset()
	default:
		v.contentSearchInput.HandleKey(msg)
	}
	return nil
}

// HandleContentSearchResult processes the async content search results.
// Called from app.go when a SessionContentSearchResultMsg is received.
func (v *SessionsView) HandleContentSearchResult(msg SessionContentSearchResultMsg) {
	v.contentSearchIDs = make(map[string]string)
	for _, m := range msg.Matches {
		v.contentSearchIDs[m.SessionID] = m.Snippet
	}
	v.cursor = 0
	v.previewLogs = nil
}

// ContentSearchSnippet returns the snippet for a session if one exists from
// an active content search.
func (v *SessionsView) ContentSearchSnippet(sessionID string) string {
	if v.contentSearchIDs == nil {
		return ""
	}
	return v.contentSearchIDs[sessionID]
}

// HasActiveContentSearch returns true if content search results are being displayed.
func (v *SessionsView) HasActiveContentSearch() bool {
	return v.contentSearchIDs != nil
}

func (v *SessionsView) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.filterMode = false
		v.filterText = v.filterInput.Value()
		v.cursor = 0
		// Also kick off async content search for deep matching
		query := v.filterInput.Value()
		if query != "" {
			return func() tea.Msg {
				matches, err := history.SearchContentWithSnippets(query, "")
				if err != nil {
					return SessionContentSearchResultMsg{Query: query}
				}
				return SessionContentSearchResultMsg{Matches: matches, Query: query}
			}
		}
	case "esc":
		v.filterMode = false
		v.filterInput.Reset()
	default:
		v.filterInput.HandleKey(msg)
	}
	return nil
}

func (v *SessionsView) handleTagKey(msg tea.KeyMsg) tea.Cmd {
	suggestions := v.tagSuggestions()

	switch msg.String() {
	case "enter":
		// If a suggestion is selected, use it
		if v.tagCursor >= 0 && v.tagCursor < len(suggestions) {
			v.applyTagSuggestion(suggestions[v.tagCursor])
			return nil
		}
		// Otherwise commit the current input
		v.tagMode = false
		s := v.SelectedSession()
		if s == nil {
			return nil
		}
		tags := parseTags(v.tagInput.Value())
		s.Tags = tags
		return func() tea.Msg {
			return SessionTagMsg{Session: *s, Tags: tags}
		}
	case "esc":
		v.tagMode = false
		v.tagInput.Reset()
		v.tagCursor = -1
	case "up":
		if v.tagCursor > 0 {
			v.tagCursor--
		} else if v.tagCursor <= 0 && len(suggestions) > 0 {
			v.tagCursor = 0
		}
	case "down":
		if v.tagCursor < len(suggestions)-1 {
			v.tagCursor++
		}
	case "tab":
		// Cycle through suggestions
		if len(suggestions) > 0 {
			v.tagCursor = (v.tagCursor + 1) % len(suggestions)
		}
	default:
		if v.tagInput.HandleKey(msg) {
			v.tagCursor = -1
		}
	}
	return nil
}

func (v *SessionsView) handleDeleteKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "y", "Y":
		v.deleteMode = false
		s := v.SelectedSession()
		if s == nil {
			return nil
		}
		session := *s
		// Remove from the sessions list
		for i := range v.sessions {
			if v.sessions[i].ID == session.ID {
				v.sessions = append(v.sessions[:i], v.sessions[i+1:]...)
				break
			}
		}
		if v.cursor >= len(v.visibleSessions()) && v.cursor > 0 {
			v.cursor--
		}
		v.previewLogs = nil
		return func() tea.Msg {
			return SessionDeleteMsg{Session: session}
		}
	default:
		// Any other key cancels (default to No)
		v.deleteMode = false
	}
	return nil
}

// applyTagSuggestion replaces the current partial tag with the selected suggestion.
func (v *SessionsView) applyTagSuggestion(tag string) {
	parts := strings.Split(v.tagInput.Value(), ",")
	if len(parts) > 1 {
		parts[len(parts)-1] = " " + tag
		v.tagInput.SetValue(strings.Join(parts, ","))
	} else {
		v.tagInput.SetValue(tag)
	}
	v.tagCursor = -1
}

func (v *SessionsView) handleNoteKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.noteMode = false
		s := v.SelectedSession()
		if s == nil {
			return nil
		}
		note := v.noteInput.Value()
		s.Note = note
		return func() tea.Msg {
			return SessionNoteMsg{Session: *s, Note: note}
		}
	case "esc":
		v.noteMode = false
		v.noteInput.Reset()
	default:
		v.noteInput.HandleKey(msg)
	}
	return nil
}

// autocompleteTag completes the current tag word from the vocabulary.
func (v *SessionsView) autocompleteTag() {
	if len(v.tagVocab) == 0 {
		return
	}
	// Get the current partial tag (after the last comma)
	parts := strings.Split(v.tagInput.Value(), ",")
	current := strings.TrimSpace(parts[len(parts)-1])
	if current == "" {
		return
	}

	lower := strings.ToLower(current)
	for _, tag := range v.tagVocab {
		if strings.HasPrefix(strings.ToLower(tag), lower) && tag != current {
			parts[len(parts)-1] = " " + tag
			v.tagInput.SetValue(strings.Join(parts, ","))
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

// cycleSortField advances to the next sort field, or toggles direction
// if the current field is pressed again.
func (v *SessionsView) cycleSortField() {
	// Find current position in the cycle
	for i, f := range sortFieldOrder {
		if f == v.sortField {
			next := sortFieldOrder[(i+1)%len(sortFieldOrder)]
			if next == v.sortField {
				v.sortAsc = !v.sortAsc
			} else {
				v.sortField = next
				// Default direction per field
				switch next {
				case SortByAge:
					v.sortAsc = false // newest first
				case SortByCost:
					v.sortAsc = false // highest first
				case SortByTurns:
					v.sortAsc = false // most turns first
				case SortByTitle:
					v.sortAsc = true // A-Z
				case SortByFailureMode:
					v.sortAsc = false // tagged first
				}
			}
			return
		}
	}
}

// sessionTitle returns the display title for sorting purposes.
func sessionTitle(s history.Session) string {
	if s.Title != "" {
		return strings.ToLower(s.Title)
	}
	return strings.ToLower(s.FirstPrompt)
}

// visibleSessions returns sessions matching the current filter, sorted
// by the active sort field.
// Near-empty sessions (1 turn, $0 cost) are hidden unless a filter is active.
func (v *SessionsView) visibleSessions() []history.Session {
	isSearching := v.filterText != "" || v.contentSearchIDs != nil
	var result []history.Session
	for _, s := range v.sessions {
		if !v.showSubagents && !isSearching && s.IsSubagent {
			continue
		}
		// Hide near-empty sessions (auto-memory, system operations) unless searching
		if !isSearching && s.CostUSD == 0 && s.TurnCount <= 5 {
			continue
		}
		// Hide sessions with no timestamps (broken/incomplete files)
		if !isSearching && s.LastActive.IsZero() {
			continue
		}
		// When searching, a session passes if it matches metadata OR content
		if isSearching {
			metaMatch := false
			contentMatch := false
			if v.filterText != "" {
				needle := strings.ToLower(v.filterText)
				metaMatch = sessionMatchesFilter(s, needle)
			}
			if v.contentSearchIDs != nil {
				_, contentMatch = v.contentSearchIDs[s.ID]
			}
			// If only metadata filter (no content results yet), use metadata
			// If both active, pass on either match
			if v.filterText != "" && v.contentSearchIDs == nil {
				if !metaMatch {
					continue
				}
			} else if !metaMatch && !contentMatch {
				continue
			}
		}
		result = append(result, s)
	}

	// Apply sort
	sort.SliceStable(result, func(i, j int) bool {
		less := v.compareSessions(result[i], result[j])
		if v.sortAsc {
			return less
		}
		return !less
	})

	return result
}

// compareSessions returns true if a should appear before b in ascending order.
func (v *SessionsView) compareSessions(a, b history.Session) bool {
	switch v.sortField {
	case SortByCost:
		return a.CostUSD < b.CostUSD
	case SortByTurns:
		return a.TurnCount < b.TurnCount
	case SortByTitle:
		return sessionTitle(a) < sessionTitle(b)
	case SortByFailureMode:
		aHas := len(a.Tags) > 0
		bHas := len(b.Tags) > 0
		if aHas != bHas {
			return !aHas // tagged sorts before untagged in ascending
		}
		aTag, bTag := "", ""
		if len(a.Tags) > 0 {
			aTag = strings.ToLower(a.Tags[0])
		}
		if len(b.Tags) > 0 {
			bTag = strings.ToLower(b.Tags[0])
		}
		return aTag < bTag
	default: // SortByAge
		return a.LastActive.Before(b.LastActive)
	}
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
	if !v.showSubagents {
		hiddenCount := 0
		for _, s := range v.sessions {
			if s.IsSubagent {
				hiddenCount++
			}
		}
		if hiddenCount > 0 {
			countStr += sessDimStyle.Render(fmt.Sprintf("  (+%d agent)", hiddenCount))
		}
	}
	if !v.showAll && v.currentDir != "" {
		countStr += sessDimStyle.Render("  press A for all projects")
	}
	b.WriteString(sessHeaderStyle.Render(headerLine) + "\n")
	b.WriteString(sessDimStyle.Render(countStr) + "\n")
	b.WriteString("\n")

	// Search/filter indicators
	if v.filterText != "" {
		var parts []string
		parts = append(parts, filterActiveStyle.Render(" /"+v.filterText+" "))
		if v.contentSearchIDs != nil {
			searchBadge := lipgloss.NewStyle().Background(lipgloss.Color("#6D28D9")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
			parts = append(parts, searchBadge.Render(fmt.Sprintf(" +%d from content ", len(v.contentSearchIDs))))
		}
		b.WriteString("  " + strings.Join(parts, " ") + sessDimStyle.Render("  Esc:clear") + "\n\n")
	} else if v.contentSearchIDs != nil {
		searchBadge := lipgloss.NewStyle().Background(lipgloss.Color("#6D28D9")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
		count := len(v.contentSearchIDs)
		label := fmt.Sprintf(" CONTENT SEARCH: %d matches ", count)
		b.WriteString("  " + searchBadge.Render(label) + sessDimStyle.Render("  Esc:clear") + "\n\n")
	}

	if v.cleanupMode {
		b.WriteString(v.renderCleanupView(w))
		return b.String()
	}

	if len(visible) == 0 {
		b.WriteString(sessDimStyle.Render("  No sessions found.") + "\n")
		return b.String()
	}

	// Column header with sort indicator
	cols := v.columnWidths(w)
	colHeader := func(name string, field SortField, width int, leftAlign bool) string {
		label := name
		if v.sortField == field {
			if v.sortAsc {
				label += " \u25b2"
			} else {
				label += " \u25bc"
			}
		}
		if leftAlign {
			return fmt.Sprintf("%-*s", width, label)
		}
		return fmt.Sprintf("%*s", width, label)
	}

	var headerParts []string
	headerParts = append(headerParts, " ")
	headerParts = append(headerParts, colHeader("AGE", SortByAge, cols.age+2, true))
	if v.showAll {
		headerParts = append(headerParts, "  ")
		headerParts = append(headerParts, fmt.Sprintf("%-*s", cols.project, "PROJECT"))
	}
	headerParts = append(headerParts, "  ")
	headerParts = append(headerParts, colHeader("TITLE", SortByTitle, cols.prompt, true))
	headerParts = append(headerParts, "  ")
	headerParts = append(headerParts, colHeader("TURNS", SortByTurns, cols.turns+2, false))
	headerParts = append(headerParts, "  ")
	headerParts = append(headerParts, colHeader("COST", SortByCost, cols.cost, false))
	header := strings.Join(headerParts, "")
	b.WriteString(sessDimStyle.Render(header) + "\n")

	// Session list - calculate how many rows fit
	listHeight := v.height - 7 // header + column header + padding
	if v.previewLogs != nil {
		listHeight = v.height / 2
	}
	// Reserve space for tag input + suggestions dropdown
	if v.tagMode {
		sugCount := len(v.tagSuggestions())
		if sugCount > 10 {
			sugCount = 10 // cap visible suggestions
		}
		listHeight -= sugCount + 3 // suggestions + input line + hint line + padding
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

		// Show snippet from content search below the selected row
		if selected && v.contentSearchIDs != nil {
			if snippet, ok := v.contentSearchIDs[s.ID]; ok && snippet != "" {
				snippetStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Italic(true)
				b.WriteString("     " + snippetStyle.Render(truncate(snippet, w-8)) + "\n")
			}
		}
	}

	// Preview pane
	if v.previewLogs != nil {
		b.WriteString("\n")
		sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))
		b.WriteString(sepStyle.Render("  " + strings.Repeat("─", w-4)) + "\n")
		b.WriteString(v.previewLogs.View())
	}

	// Input modes
	if v.contentSearchMode {
		searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
		cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA"))
		b.WriteString("\n  " + searchStyle.Render("SEARCH CONTENT: ") + v.contentSearchInput.BeforeCursor() + cursorStyle.Render("█") + v.contentSearchInput.AfterCursor())
		b.WriteString("\n  " + sessDimStyle.Render("  Enter:search  Esc:cancel — searches inside session files using ripgrep"))
	}
	if v.filterMode {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true).Render("/") + v.filterInput.BeforeCursor() + lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Render("█") + v.filterInput.AfterCursor())
	}
	if v.tagMode {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true).Render("Failure-mode: ") + v.tagInput.BeforeCursor() + lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render("█") + v.tagInput.AfterCursor())
		b.WriteString("\n  " + sessDimStyle.Render("  ↑/↓:select  Tab:cycle  Enter:pick  type to filter"))
		// Show selectable suggestions (max 10 visible)
		suggestions := v.tagSuggestions()
		maxVisible := 10
		if len(suggestions) < maxVisible {
			maxVisible = len(suggestions)
		}
		for i := 0; i < maxVisible; i++ {
			tag := suggestions[i]
			prefix := "    "
			if i == v.tagCursor {
				selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
				b.WriteString("\n" + prefix + selectedStyle.Render("▸ " + tag))
			} else {
				b.WriteString("\n" + prefix + sessDimStyle.Render("  " + tag))
			}
		}
		if len(suggestions) > maxVisible {
			b.WriteString("\n    " + sessDimStyle.Render(fmt.Sprintf("  ... and %d more (type to filter)", len(suggestions)-maxVisible)))
		}
		if len(suggestions) == 0 && v.tagInput.Value() != "" {
			b.WriteString("\n    " + sessDimStyle.Render("(new tag: \"" + v.tagInput.Value() + "\")"))
		}
	}
	if v.noteMode {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true).Render("Note: ") + v.noteInput.BeforeCursor() + lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render("█") + v.noteInput.AfterCursor())
	}
	if v.deleteMode {
		s := v.SelectedSession()
		if s != nil {
			title := s.Title
			if title == "" {
				title = s.FirstPrompt
			}
			if len(title) > 50 {
				title = title[:47] + "..."
			}
			warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
			dimWarn := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
			b.WriteString("\n\n")
			b.WriteString("  " + warnStyle.Render(fmt.Sprintf("Delete \"%s\" (%d turns)?", title, s.TurnCount)) + "\n")
			b.WriteString("  " + dimWarn.Render("This permanently removes the session from Claude. Cannot be undone.") + "\n")
			b.WriteString("  " + warnStyle.Render("y") + dimWarn.Render(" to confirm, any other key to cancel"))
		}
	}

	return b.String()
}

// tagSuggestions returns all tags from vocabulary, with matches ranked first.
func (v *SessionsView) tagSuggestions() []string {
	if len(v.tagVocab) == 0 {
		return nil
	}
	parts := strings.Split(v.tagInput.Value(), ",")
	current := strings.TrimSpace(parts[len(parts)-1])
	if current == "" {
		return v.tagVocab
	}

	// Show matches first, then remaining tags
	lower := strings.ToLower(current)
	var matches, rest []string
	for _, tag := range v.tagVocab {
		if strings.Contains(strings.ToLower(tag), lower) {
			matches = append(matches, tag)
		} else {
			rest = append(rest, tag)
		}
	}
	return append(matches, rest...)
}

// colLayout holds the computed column widths for consistent alignment.
type colLayout struct {
	age     int
	project int
	prompt  int
	turns   int
	cost    int
}

// columnWidths computes fixed column widths based on available width.
func (v *SessionsView) columnWidths(w int) colLayout {
	c := colLayout{
		age:   7,
		turns: 5,
		cost:  7,
	}
	if v.showAll {
		c.project = 12
	}
	// marker(3) + spacing between columns(2*4=8 for non-project, 2*5=10 for project)
	fixed := 3 + c.age + c.turns + c.cost + 8
	if v.showAll {
		fixed += c.project + 2
	}
	c.prompt = w - fixed
	if c.prompt < 15 {
		c.prompt = 15
	}
	return c
}

// renderSessionRow renders a single session row as a clean, columnar line.
func (v *SessionsView) renderSessionRow(s history.Session, selected bool, w int) string {
	isEmpty := s.TurnCount <= 1 && s.CostUSD == 0
	cols := v.columnWidths(w)

	marker := "  "
	if selected {
		marker = " \u25b8"
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
	prompt = strings.TrimLeft(prompt, "# ")

	// Prepend annotation and failure-mode tag to the title so they're always visible
	var prefixes []string
	if s.Annotation != "" {
		prefixes = append(prefixes, strings.ToUpper(s.Annotation))
	}
	if len(s.Tags) > 0 {
		tag := s.Tags[0]
		if len(tag) > 20 {
			tag = tag[:18] + ".."
		}
		prefixes = append(prefixes, tag)
	}
	if s.IsSubagent {
		prefixes = append([]string{"agent"}, prefixes...)
	}
	if len(prefixes) > 0 {
		prompt = "[" + strings.Join(prefixes, "|") + "] " + prompt
	}

	var b strings.Builder
	b.WriteString(marker + " ")

	// Age column (fixed width)
	ageStr := fmt.Sprintf("%-*s", cols.age, age)
	if isEmpty {
		b.WriteString(sessDimStyle.Render(ageStr))
	} else {
		b.WriteString(sessAgeStyle.Render(ageStr))
	}
	b.WriteString("  ")

	// Project column (all-projects mode only, fixed width)
	if v.showAll {
		proj := shortProject(s.Project)
		projStr := fmt.Sprintf("%-*s", cols.project, truncate(proj, cols.project))
		b.WriteString(sessProjectStyle.Render(projStr))
		b.WriteString("  ")
	}

	// Prompt column (fixed width, padded)
	// If there's a prefix [ANNOTATION|tag], render it with color
	if len(prefixes) > 0 && strings.HasPrefix(prompt, "[") {
		tagEnd := strings.Index(prompt, "] ")
		if tagEnd > 0 {
			tagPart := prompt[:tagEnd+1]
			restPart := prompt[tagEnd+2:]
			prefixLen := len(tagPart) + 1 // +1 for space after bracket
			truncRest := truncate(restPart, cols.prompt-prefixLen)
			padded := fmt.Sprintf("%-*s", cols.prompt-prefixLen, truncRest)
			// Color the prefix based on content
			prefixStyle := sessAnnotStyle(s.Annotation, len(s.Tags) > 0)
			b.WriteString(prefixStyle.Render(tagPart) + " " + sessPromptStyle.Render(padded))
		} else {
			b.WriteString(sessPromptStyle.Render(fmt.Sprintf("%-*s", cols.prompt, truncate(prompt, cols.prompt))))
		}
	} else {
		truncPrompt := fmt.Sprintf("%-*s", cols.prompt, truncate(prompt, cols.prompt))
		if isEmpty {
			b.WriteString(sessDimStyle.Render(truncPrompt))
		} else {
			b.WriteString(sessPromptStyle.Render(truncPrompt))
		}
	}
	b.WriteString("  ")

	// Turns column (right-aligned, fixed width)
	turnStr := fmt.Sprintf("%*s", cols.turns, fmt.Sprintf("%dt", s.TurnCount))
	if isEmpty {
		b.WriteString(sessDimStyle.Render(turnStr))
	} else {
		b.WriteString(sessTurnStyle.Render(turnStr))
	}
	b.WriteString("  ")

	// Cost column (right-aligned, fixed width)
	costStr := fmt.Sprintf("%*s", cols.cost, fmt.Sprintf("$%.2f", s.CostUSD))
	if isEmpty {
		b.WriteString(sessDimStyle.Render(costStr))
	} else {
		b.WriteString(sessCostStyle.Render(costStr))
	}

	// Badges (annotation, view-only) after fixed columns
	var badges []string
	if s.Annotation != "" {
		badges = append(badges, renderSessionAnnotation(s.Annotation))
	}
	if !s.Resumable {
		badges = append(badges, sessViewOnlyStyle.Render("(view)"))
	}
	if len(badges) > 0 {
		b.WriteString("  ")
		b.WriteString(strings.Join(badges, " "))
	}

	line := b.String()

	if selected {
		return sessSelectedStyle.Render(padRight(line, w))
	}
	return line
}

// sessAnnotStyle returns a lipgloss style for the combined prefix badge
// based on annotation type and whether failure tags are present.
func sessAnnotStyle(annotation string, hasTags bool) lipgloss.Style {
	if hasTags {
		// Red for failure-mode tags (takes priority visually)
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
	}
	switch annotation {
	case "achieved":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true)
	case "partial":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	case "failed":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
	case "abandoned":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	}
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

// enterCleanupMode scans visible sessions for duplicates and empties,
// then enters cleanup mode if any candidates are found.
func (v *SessionsView) enterCleanupMode() {
	visible := v.visibleSessions()
	dupes := history.FindDuplicates(visible)
	empty := history.FindEmpty(visible)

	dupeIDs := make(map[string]bool)
	for _, s := range dupes {
		dupeIDs[s.ID] = true
	}

	var items []cleanupItem
	for _, s := range dupes {
		items = append(items, cleanupItem{session: s, reason: "duplicate", selected: true})
	}
	for _, s := range empty {
		if !dupeIDs[s.ID] {
			items = append(items, cleanupItem{session: s, reason: "empty", selected: true})
		}
	}

	if len(items) == 0 {
		return
	}
	v.cleanupMode = true
	v.cleanupItems = items
	v.cleanupCursor = 0
}

// handleCleanupKey processes key events while in cleanup mode.
func (v *SessionsView) handleCleanupKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "j", "down":
		if v.cleanupCursor < len(v.cleanupItems)-1 {
			v.cleanupCursor++
		}
	case "k", "up":
		if v.cleanupCursor > 0 {
			v.cleanupCursor--
		}
	case " ":
		v.cleanupItems[v.cleanupCursor].selected = !v.cleanupItems[v.cleanupCursor].selected
	case "a":
		allSelected := true
		for _, item := range v.cleanupItems {
			if !item.selected {
				allSelected = false
				break
			}
		}
		for i := range v.cleanupItems {
			v.cleanupItems[i].selected = !allSelected
		}
	case "enter":
		var toDelete []history.Session
		for _, item := range v.cleanupItems {
			if item.selected {
				toDelete = append(toDelete, item.session)
			}
		}
		v.cleanupMode = false
		v.cleanupItems = nil
		if len(toDelete) == 0 {
			return nil
		}
		deleteIDs := make(map[string]bool)
		for _, s := range toDelete {
			deleteIDs[s.ID] = true
		}
		var kept []history.Session
		for _, s := range v.sessions {
			if !deleteIDs[s.ID] {
				kept = append(kept, s)
			}
		}
		v.sessions = kept
		v.cursor = 0
		v.previewLogs = nil
		sessions := toDelete
		return func() tea.Msg {
			return SessionBulkDeleteMsg{Sessions: sessions}
		}
	case "esc":
		v.cleanupMode = false
		v.cleanupItems = nil
	}
	return nil
}

// renderCleanupView renders the bulk cleanup overlay.
func (v *SessionsView) renderCleanupView(w int) string {
	var b strings.Builder

	dupeCount, emptyCount, selectedCount := 0, 0, 0
	for _, item := range v.cleanupItems {
		if item.reason == "duplicate" {
			dupeCount++
		}
		if item.reason == "empty" {
			emptyCount++
		}
		if item.selected {
			selectedCount++
		}
	}

	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	headerStr := fmt.Sprintf("  Cleanup: %d duplicates, %d empty -- %d selected", dupeCount, emptyCount, selectedCount)
	b.WriteString(warnStyle.Render(headerStr) + "\n")
	b.WriteString(sessDimStyle.Render("  space:toggle  a:all  enter:delete  esc:cancel") + "\n\n")

	listHeight := v.height - 8
	if listHeight < 3 {
		listHeight = 3
	}

	start := 0
	if v.cleanupCursor >= listHeight {
		start = v.cleanupCursor - listHeight + 1
	}
	end := start + listHeight
	if end > len(v.cleanupItems) {
		end = len(v.cleanupItems)
	}

	for i := start; i < end; i++ {
		item := v.cleanupItems[i]
		selected := i == v.cleanupCursor

		check := "[ ]"
		if item.selected {
			check = "[x]"
		}

		reason := sessDimStyle.Render(fmt.Sprintf("(%s)", item.reason))
		prompt := item.session.Title
		if prompt == "" {
			prompt = item.session.FirstPrompt
		}
		if prompt == "" {
			prompt = "(no prompt)"
		}
		if len(prompt) > 50 {
			prompt = prompt[:47] + "..."
		}

		turnStr := sessTurnStyle.Render(fmt.Sprintf("%dt", item.session.TurnCount))
		age := sessAgeStyle.Render(formatAge(item.session.LastActive))
		costStr := sessCostStyle.Render(fmt.Sprintf("$%.2f", item.session.CostUSD))

		line := fmt.Sprintf("  %s %s  %s  %-50s  %s  %s", check, reason, age, prompt, turnStr, costStr)
		if selected {
			line = sessSelectedStyle.Render(padRight(line, w))
		}
		b.WriteString(line + "\n")
	}

	return b.String()
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

// SubagentCount returns the number of subagent sessions in the sessions list.
func (v *SessionsView) SubagentCount() int {
	count := 0
	for _, s := range v.sessions {
		if s.IsSubagent {
			count++
		}
	}
	return count
}

// ShowSubagents returns whether subagent sessions are currently visible.
func (v *SessionsView) ShowSubagents() bool {
	return v.showSubagents
}
