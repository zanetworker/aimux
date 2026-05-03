package views

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/history"
)

// --- Styles ---

var (
	launcherBoxBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#5F87FF"))
	launcherTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#5F87FF"))
	launcherLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF"))
	launcherSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E5E7EB")).
				Background(lipgloss.Color("#1E3A5F")).
				Bold(true)
	launcherOptionStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF"))
	launcherActiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#111827")).
				Background(lipgloss.Color("#5F87FF")).
				Bold(true).
				Padding(0, 1)
	launcherInactiveTabStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#9CA3AF")).
					Padding(0, 1)
	launcherPathStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#06B6D4"))
	launcherDimStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B7280"))
	launcherHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B7280"))
)

// --- Messages ---

// LaunchMsg is emitted when the user confirms the launch configuration.
type LaunchMsg struct {
	Provider    string
	Dir         string
	Model       string
	Mode        string
	Runtime     string
	OTELEnabled bool // true if OTEL tracing should be injected
}

// LaunchResumeMsg is emitted when the user picks a session to resume from the launcher.
type LaunchResumeMsg struct {
	SessionID string
	Dir       string
	FilePath  string
}

// LaunchCancelMsg is emitted when the user cancels the launcher.
type LaunchCancelMsg struct{}

// --- Types ---

// RecentDirEntry is a directory entry for the recent dirs list.
type RecentDirEntry struct {
	Path     string
	Display  string // shortened display name
	Provider string
	Age      string // "2m ago", "1h ago"
}

type launcherState int

const (
	statePickProvider launcherState = iota
	statePickDirectory
	statePickResume
	statePickOptions
)

// LauncherView renders the agent launcher overlay.
type LauncherView struct {
	state  launcherState
	width  int
	height int

	// Provider selection
	providers      []string
	providerCursor int

	// Directory selection
	recentDirs  []RecentDirEntry
	dirCursor   int
	browseMode  bool   // false=recent, true=browse
	browsePath  string // current browse directory
	browseItems []browseEntry
	filterText  string

	// Options selection
	models       []string
	modelCursor  int
	modes        []string
	modeCursor   int
	runtimes     []string
	runtimeCursor int
	otelEnabled  bool // toggle for OTEL tracing on spawned session
	otelAvailable bool // true if OTEL receiver is running
	optionField  int // 0=model, 1=mode, 2=runtime, 3=otel
	providerOpts map[string]ProviderOptions

	// Resume step
	resumeSessions []history.Session // recent sessions for selected directory
	resumeCursor   int               // 0 = "New session", 1+ = sessions
}

// ProviderOptions holds the models and modes for a specific provider.
type ProviderOptions struct {
	Models []string
	Modes  []string
}

type browseEntry struct {
	name  string
	isDir bool
}

// NewLauncherView creates a new launcher overlay. providerOpts maps provider
// names to their available models and modes. otelAvailable is true if the
// OTEL receiver is running (shows toggle in options).
func NewLauncherView(recentDirs []RecentDirEntry, providerOpts map[string]ProviderOptions, otelAvailable bool) *LauncherView {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/"
	}

	providers := make([]string, 0, len(providerOpts))
	for name := range providerOpts {
		providers = append(providers, name)
	}
	// Sort for consistent order
	sort.Strings(providers)

	// Default to first provider's options
	var models, modes []string
	if len(providers) > 0 {
		opts := providerOpts[providers[0]]
		models = opts.Models
		modes = opts.Modes
	}
	if len(models) == 0 {
		models = []string{"default"}
	}
	if len(modes) == 0 {
		modes = []string{"default"}
	}

	return &LauncherView{
		state:         statePickProvider,
		providers:     providers,
		recentDirs:    recentDirs,
		browsePath:    home,
		models:        models,
		modes:         modes,
		runtimes:      []string{"tmux", "iterm"},
		otelAvailable: otelAvailable,
		otelEnabled:   otelAvailable, // default on if receiver is running
		providerOpts:  providerOpts,
	}
}

// SkipToDirectory pre-selects a provider and jumps directly to the directory
// step, skipping the provider selection screen. Used by the :new picker flow.
func (l *LauncherView) SkipToDirectory(providerName string) {
	for i, p := range l.providers {
		if p == providerName {
			l.providerCursor = i
			break
		}
	}
	l.state = statePickDirectory
}

// SetSize sets the available dimensions for the overlay.
func (l *LauncherView) SetSize(w, h int) {
	l.width = w
	l.height = h
}

// Update handles key messages and returns a tea.Cmd if the launcher emits
// a LaunchMsg or LaunchCancelMsg.
func (l *LauncherView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()

		// Esc cancels at any step
		if key == "esc" {
			return func() tea.Msg { return LaunchCancelMsg{} }
		}

		switch l.state {
		case statePickProvider:
			return l.updateProvider(key)
		case statePickDirectory:
			return l.updateDirectory(key)
		case statePickResume:
			return l.updateResume(key)
		case statePickOptions:
			return l.updateOptions(key)
		}
	}
	return nil
}

func (l *LauncherView) updateProvider(key string) tea.Cmd {
	switch key {
	case "j", "down":
		if l.providerCursor < len(l.providers)-1 {
			l.providerCursor++
		}
	case "k", "up":
		if l.providerCursor > 0 {
			l.providerCursor--
		}
	case "enter":
		// Update models/modes for the selected provider
		selected := l.providers[l.providerCursor]
		if opts, ok := l.providerOpts[selected]; ok {
			l.models = opts.Models
			l.modes = opts.Modes
		}
		l.modelCursor = 0
		l.modeCursor = 0
		l.state = statePickDirectory
		if l.browseMode {
			l.loadBrowseDir()
		}
	}
	return nil
}

func (l *LauncherView) updateDirectory(key string) tea.Cmd {
	switch key {
	case "tab":
		l.browseMode = !l.browseMode
		l.dirCursor = 0
		l.filterText = ""
		if l.browseMode {
			l.loadBrowseDir()
		}
	case "j", "down":
		max := l.dirListLen() - 1
		if l.dirCursor < max {
			l.dirCursor++
		}
	case "k", "up":
		if l.dirCursor > 0 {
			l.dirCursor--
		}
	case "enter":
			if l.browseMode {
			return l.handleBrowseEnter()
		}
		// Recent mode: select directory and advance
		if l.dirCursor < len(l.filteredRecent()) {
			l.advanceFromDir()
		}
	case "s":
		// Select the current browse directory as the project dir
		if l.browseMode {
			l.advanceFromDir()
			return nil
		}
	case "backspace":
		if l.browseMode && l.filterText == "" {
			// Go up one directory
			l.browsePath = filepath.Dir(l.browsePath)
			l.dirCursor = 0
			l.loadBrowseDir()
		} else if len(l.filterText) > 0 {
			l.filterText = l.filterText[:len(l.filterText)-1]
		}
	default:
		if len(key) == 1 && key >= " " {
			l.filterText += key
			l.dirCursor = 0
		}
	}
	return nil
}

func (l *LauncherView) handleBrowseEnter() tea.Cmd {
	items := l.filteredBrowse()
	if l.dirCursor >= len(items) {
		return nil
	}
	entry := items[l.dirCursor]
	if entry.name == "." {
		// Select this directory
		l.advanceFromDir()
		return nil
	}
	if entry.name == ".." {
		l.browsePath = filepath.Dir(l.browsePath)
		l.dirCursor = 0
		l.filterText = ""
		l.loadBrowseDir()
		return nil
	}
	fullPath := filepath.Join(l.browsePath, entry.name)
	if entry.isDir {
		l.browsePath = fullPath
		l.dirCursor = 0
		l.filterText = ""
		l.loadBrowseDir()
	}
	return nil
}

func (l *LauncherView) updateOptions(key string) tea.Cmd {
	maxField := 2
	if l.otelAvailable {
		maxField = 3
	}
	switch key {
	case "j", "down":
		if l.optionField < maxField {
			l.optionField++
		}
	case "k", "up":
		if l.optionField > 0 {
			l.optionField--
		}
	case "l", "right":
		switch l.optionField {
		case 0:
			if l.modelCursor < len(l.models)-1 {
				l.modelCursor++
			}
		case 1:
			if l.modeCursor < len(l.modes)-1 {
				l.modeCursor++
			}
		case 2:
			if l.runtimeCursor < len(l.runtimes)-1 {
				l.runtimeCursor++
			}
		case 3:
			l.otelEnabled = !l.otelEnabled
		}
	case "h", "left":
		switch l.optionField {
		case 0:
			if l.modelCursor > 0 {
				l.modelCursor--
			}
		case 1:
			if l.modeCursor > 0 {
				l.modeCursor--
			}
		case 2:
			if l.runtimeCursor > 0 {
				l.runtimeCursor--
			}
		case 3:
			l.otelEnabled = !l.otelEnabled
		}
	case " ":
		// Space toggles OTEL when on that field
		if l.optionField == 3 {
			l.otelEnabled = !l.otelEnabled
		}
	case "enter":
		return l.emitLaunch()
	}
	return nil
}

// advanceFromDir transitions from directory selection to the resume step.
// Looks up recent sessions for the selected directory; if none exist,
// skips straight to options (new session).
func (l *LauncherView) advanceFromDir() {
	dir := l.selectedDir()
	provider := ""
	if l.providerCursor < len(l.providers) {
		provider = l.providers[l.providerCursor]
	}

	// Only Claude supports resume for now
	if provider != "claude" || dir == "" {
		l.state = statePickOptions
		return
	}

	sessions, _ := history.Discover(history.DiscoverOpts{Dir: dir, Limit: 5}, "")
	// Filter out near-empty sessions
	var meaningful []history.Session
	for _, s := range sessions {
		if s.TurnCount > 5 || s.CostUSD > 0 {
			meaningful = append(meaningful, s)
		}
	}

	if len(meaningful) == 0 {
		l.state = statePickOptions
		return
	}

	l.resumeSessions = meaningful
	l.resumeCursor = 0 // "New session" is default
	l.state = statePickResume
}

func (l *LauncherView) updateResume(key string) tea.Cmd {
	maxIdx := len(l.resumeSessions) // 0 = new session, 1..N = resume options
	switch key {
	case "j", "down":
		if l.resumeCursor < maxIdx {
			l.resumeCursor++
		}
	case "k", "up":
		if l.resumeCursor > 0 {
			l.resumeCursor--
		}
	case "esc":
		l.state = statePickDirectory
		l.resumeCursor = 0
	case "enter":
		if l.resumeCursor == 0 {
			// New session
			l.state = statePickOptions
			return nil
		}
		// Resume selected session
		s := l.resumeSessions[l.resumeCursor-1]
		return func() tea.Msg {
			return LaunchResumeMsg{
				SessionID: s.ID,
				Dir:       s.Project,
				FilePath:  s.FilePath,
			}
		}
	}
	return nil
}

func (l *LauncherView) emitLaunch() tea.Cmd {
	dir := l.selectedDir()
	if dir == "" {
		return nil
	}

	model := l.models[l.modelCursor]
	if model == "default" {
		model = ""
	}
	mode := l.modes[l.modeCursor]
	if mode == "default" {
		mode = ""
	}

	msg := LaunchMsg{
		Provider:    l.providers[l.providerCursor],
		Dir:         dir,
		Model:       model,
		Mode:        mode,
		Runtime:     l.runtimes[l.runtimeCursor],
		OTELEnabled: l.otelEnabled,
	}
	return func() tea.Msg { return msg }
}

func (l *LauncherView) selectedDir() string {
	if l.browseMode {
		return l.browsePath
	}
	filtered := l.filteredRecent()
	if l.dirCursor < len(filtered) {
		return filtered[l.dirCursor].Path
	}
	return ""
}

// --- View rendering ---

// View renders the launcher overlay as a full-screen replacement.
func (l *LauncherView) View() string {
	var content string
	switch l.state {
	case statePickProvider:
		content = l.viewProvider()
	case statePickDirectory:
		content = l.viewDirectory()
	case statePickResume:
		content = l.viewResume()
	case statePickOptions:
		content = l.viewOptions()
	}

	// Render content lines with left padding for centering
	contentLines := strings.Split(content, "\n")
	maxContentW := 0
	for _, line := range contentLines {
		if w := lipgloss.Width(line); w > maxContentW {
			maxContentW = w
		}
	}

	leftPad := (l.width - maxContentW) / 2
	if leftPad < 2 {
		leftPad = 2
	}
	topPad := (l.height - len(contentLines)) / 3
	if topPad < 1 {
		topPad = 1
	}

	var b strings.Builder

	// Top padding (blank lines fill screen above the content)
	for i := 0; i < topPad; i++ {
		b.WriteString(strings.Repeat(" ", l.width) + "\n")
	}

	// Top border
	borderW := maxContentW + 4
	pad := strings.Repeat(" ", leftPad-1)
	b.WriteString(pad + launcherBoxBorderStyle.Render("┌"+strings.Repeat("─", borderW)+"┐") + "\n")

	// Content lines with border
	for _, line := range contentLines {
		lineW := lipgloss.Width(line)
		rightFill := maxContentW - lineW + 2
		if rightFill < 0 {
			rightFill = 0
		}
		b.WriteString(pad + launcherBoxBorderStyle.Render("│") + "  " + line + strings.Repeat(" ", rightFill) + launcherBoxBorderStyle.Render("│") + "\n")
	}

	// Bottom border
	b.WriteString(pad + launcherBoxBorderStyle.Render("└"+strings.Repeat("─", borderW)+"┘") + "\n")

	// Fill remaining height
	rendered := topPad + len(contentLines) + 2 // +2 for top/bottom borders
	for i := rendered; i < l.height; i++ {
		b.WriteString(strings.Repeat(" ", l.width) + "\n")
	}

	return b.String()
}

func (l *LauncherView) viewProvider() string {
	var b strings.Builder
	b.WriteString(launcherTitleStyle.Render("Launch Agent"))
	b.WriteString("\n\n")
	b.WriteString(launcherLabelStyle.Render("Provider:"))
	b.WriteString("\n")

	for i, p := range l.providers {
		cursor := "  "
		style := launcherOptionStyle
		if i == l.providerCursor {
			cursor = "▸ "
			style = launcherSelectedStyle
		}
		b.WriteString(cursor + style.Render(p) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(launcherHintStyle.Render("j/k:select  Enter:next  Esc:cancel"))
	return b.String()
}

func (l *LauncherView) viewDirectory() string {
	var b strings.Builder
	b.WriteString(launcherTitleStyle.Render("Launch Agent"))
	b.WriteString("  ")
	b.WriteString(launcherLabelStyle.Render(l.providers[l.providerCursor]))
	b.WriteString("\n\n")

	// Tabs
	recentTab := launcherInactiveTabStyle.Render("Recent")
	browseTab := launcherInactiveTabStyle.Render("Browse")
	if !l.browseMode {
		recentTab = launcherActiveTabStyle.Render("Recent")
	} else {
		browseTab = launcherActiveTabStyle.Render("Browse")
	}
	b.WriteString(launcherLabelStyle.Render("Directory: ") + recentTab + " " + browseTab)
	b.WriteString("\n\n")

	if l.browseMode {
		b.WriteString(l.viewBrowse())
	} else {
		b.WriteString(l.viewRecent())
	}

	b.WriteString("\n")
	if l.filterText != "" {
		b.WriteString(launcherPathStyle.Render("/" + l.filterText))
		b.WriteString("\n")
	}

	hints := "j/k:select  Enter:pick  Tab:browse  Esc:cancel"
	if l.browseMode {
		hints = "j/k:nav  Enter:open  .:select  Backspace:up  Tab:recent  Esc:cancel"
	}
	b.WriteString(launcherHintStyle.Render(hints))
	return b.String()
}

func (l *LauncherView) viewRecent() string {
	var b strings.Builder
	filtered := l.filteredRecent()

	if len(filtered) == 0 {
		b.WriteString(launcherDimStyle.Render("  No recent directories found.\n"))
		b.WriteString(launcherDimStyle.Render("  Press Tab to browse.\n"))
		return b.String()
	}

	maxVisible := 10
	start := 0
	if l.dirCursor >= maxVisible {
		start = l.dirCursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(filtered) {
		end = len(filtered)
	}

	for i := start; i < end; i++ {
		d := filtered[i]
		cursor := "  "
		style := launcherOptionStyle
		if i == l.dirCursor {
			cursor = "▸ "
			style = launcherSelectedStyle
		}

		line := style.Render(d.Display)
		if d.Age != "" {
			line += "  " + launcherDimStyle.Render(d.Age)
		}
		b.WriteString(cursor + line + "\n")
	}
	return b.String()
}

func (l *LauncherView) viewBrowse() string {
	var b strings.Builder

	// Show current path
	b.WriteString(launcherPathStyle.Render(l.browsePath))
	b.WriteString("\n\n")

	items := l.filteredBrowse()
	if len(items) == 0 {
		b.WriteString(launcherDimStyle.Render("  Empty directory\n"))
		return b.String()
	}

	maxVisible := 10
	start := 0
	if l.dirCursor >= maxVisible {
		start = l.dirCursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(items) {
		end = len(items)
	}

	for i := start; i < end; i++ {
		entry := items[i]
		cursor := "  "
		style := launcherOptionStyle
		if i == l.dirCursor {
			cursor = "▸ "
			style = launcherSelectedStyle
		}

		var label string
		if entry.name == "." {
			label = "✓ SELECT THIS DIRECTORY"
		} else if entry.isDir {
			label = "📁 " + entry.name
		} else {
			label = "  " + entry.name
		}
		b.WriteString(cursor + style.Render(label) + "\n")
	}
	return b.String()
}

func (l *LauncherView) viewResume() string {
	dir := l.selectedDir()
	shortDir := dir
	if len(shortDir) > 40 {
		shortDir = "..." + shortDir[len(shortDir)-37:]
	}
	provider := l.providers[l.providerCursor]

	var lines []string
	lines = append(lines, launcherTitleStyle.Render(fmt.Sprintf("  Launch %s in %s", provider, shortDir)))
	lines = append(lines, "")

	// "New session" option
	marker := "  ○ "
	style := launcherOptionStyle
	if l.resumeCursor == 0 {
		marker = "  ● "
		style = launcherSelectedStyle
	}
	lines = append(lines, style.Render(marker+"New session"))
	lines = append(lines, "")

	// Recent sessions to resume
	for i, s := range l.resumeSessions {
		marker = "  ○ "
		style = launcherOptionStyle
		if l.resumeCursor == i+1 {
			marker = "  ● "
			style = launcherSelectedStyle
		}

		prompt := s.FirstPrompt
		if prompt == "" {
			prompt = "(no prompt)"
		}
		if len(prompt) > 50 {
			prompt = prompt[:47] + "..."
		}
		age := formatAge(s.LastActive)
		label := fmt.Sprintf("Resume: %s  (%s)", prompt, age)
		lines = append(lines, style.Render(marker+label))
	}

	lines = append(lines, "")
	lines = append(lines, launcherHintStyle.Render("  ↑↓ select  Enter launch  Esc back"))

	return strings.Join(lines, "\n")
}

func (l *LauncherView) viewOptions() string {
	var b strings.Builder
	b.WriteString(launcherTitleStyle.Render("Launch Agent"))
	b.WriteString("  ")
	b.WriteString(launcherLabelStyle.Render(l.providers[l.providerCursor]))
	b.WriteString("\n")

	dir := l.selectedDir()
	if len(dir) > 40 {
		dir = "..." + dir[len(dir)-37:]
	}
	b.WriteString(launcherPathStyle.Render(dir))
	b.WriteString("\n\n")

	// Model row
	b.WriteString(l.renderOptionRow("Model:", l.models, l.modelCursor, l.optionField == 0))
	// Mode row
	b.WriteString(l.renderOptionRow("Mode:", l.modes, l.modeCursor, l.optionField == 1))
	// Runtime row
	b.WriteString(l.renderOptionRow("Runtime:", l.runtimes, l.runtimeCursor, l.optionField == 2))

	// OTEL tracing toggle (only if receiver is available)
	if l.otelAvailable {
		otelLabel := launcherLabelStyle.Render(fmt.Sprintf("%-10s", "Tracing:"))
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
		var otelValue string
		if l.otelEnabled {
			onText := "ON"
			warn := warnStyle.Render(" (no assistant responses in trace view)")
			if l.optionField == 3 {
				otelValue = launcherSelectedStyle.Render(" "+onText+" ") + warn
			} else {
				otelValue = launcherOptionStyle.Render(onText) + warn
			}
		} else {
			if l.optionField == 3 {
				otelValue = launcherSelectedStyle.Render(" OFF ")
			} else {
				otelValue = launcherOptionStyle.Render("OFF")
			}
		}
		b.WriteString(otelLabel + otelValue + "\n")
	}

	b.WriteString("\n")
	b.WriteString(launcherHintStyle.Render("j/k:field  h/l:option  Space:toggle  Enter:launch  Esc:cancel"))
	return b.String()
}

func (l *LauncherView) renderOptionRow(label string, options []string, cursor int, active bool) string {
	row := launcherLabelStyle.Render(fmt.Sprintf("%-10s", label))
	for i, opt := range options {
		if i == cursor {
			if active {
				row += launcherSelectedStyle.Render(" [" + opt + "] ")
			} else {
				row += lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB")).Render(" [" + opt + "] ")
			}
		} else {
			row += launcherOptionStyle.Render("  " + opt + "  ")
		}
	}
	return row + "\n"
}

// --- Helpers ---

func (l *LauncherView) dirListLen() int {
	if l.browseMode {
		return len(l.filteredBrowse())
	}
	return len(l.filteredRecent())
}

func (l *LauncherView) filteredRecent() []RecentDirEntry {
	if l.filterText == "" {
		return l.recentDirs
	}
	needle := strings.ToLower(l.filterText)
	var result []RecentDirEntry
	for _, d := range l.recentDirs {
		if strings.Contains(strings.ToLower(d.Display), needle) ||
			strings.Contains(strings.ToLower(d.Path), needle) {
			result = append(result, d)
		}
	}
	return result
}

func (l *LauncherView) filteredBrowse() []browseEntry {
	if l.filterText == "" {
		return l.browseItems
	}
	needle := strings.ToLower(l.filterText)
	var result []browseEntry
	for _, e := range l.browseItems {
		if strings.Contains(strings.ToLower(e.name), needle) {
			result = append(result, e)
		}
	}
	return result
}

func (l *LauncherView) loadBrowseDir() {
	l.browseItems = nil

	// "." means select this directory
	l.browseItems = append(l.browseItems, browseEntry{name: ".", isDir: true})

	// ".." to go up
	if l.browsePath != "/" {
		l.browseItems = append(l.browseItems, browseEntry{name: "..", isDir: true})
	}

	entries, err := os.ReadDir(l.browsePath)
	if err != nil {
		return
	}

	// Sort: directories first, then alphabetical
	sort.Slice(entries, func(i, j int) bool {
		iDir := entries[i].IsDir()
		jDir := entries[j].IsDir()
		if iDir != jDir {
			return iDir
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	for _, e := range entries {
		// Skip hidden files/dirs
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		l.browseItems = append(l.browseItems, browseEntry{
			name:  e.Name(),
			isDir: e.IsDir(),
		})
	}
}
