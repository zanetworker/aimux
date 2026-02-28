package views

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- Styles ---

var (
	launcherBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#5F87FF")).
				Padding(1, 2)
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
	Provider string
	Dir      string
	Model    string
	Mode     string
	Runtime  string
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
	optionField  int // 0=model, 1=mode, 2=runtime
}

type browseEntry struct {
	name  string
	isDir bool
}

// NewLauncherView creates a new launcher overlay.
func NewLauncherView(recentDirs []RecentDirEntry) *LauncherView {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/"
	}

	return &LauncherView{
		state:     statePickProvider,
		providers: []string{"claude", "codex", "gemini"},
		recentDirs: recentDirs,
		browsePath: home,
		models:    []string{"default", "opus", "sonnet", "haiku"},
		modes:     []string{"default", "bypass", "plan"},
		runtimes:  []string{"tmux", "iterm"},
	}
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
			l.state = statePickOptions
		}
	case "s":
		// Select the current browse directory as the project dir
		if l.browseMode {
			l.state = statePickOptions
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
		l.state = statePickOptions
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
	switch key {
	case "j", "down":
		if l.optionField < 2 {
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
		}
	case "enter":
		return l.emitLaunch()
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
		Provider: l.providers[l.providerCursor],
		Dir:      dir,
		Model:    model,
		Mode:     mode,
		Runtime:  l.runtimes[l.runtimeCursor],
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

// View renders the launcher overlay.
func (l *LauncherView) View() string {
	var content string
	switch l.state {
	case statePickProvider:
		content = l.viewProvider()
	case statePickDirectory:
		content = l.viewDirectory()
	case statePickOptions:
		content = l.viewOptions()
	}

	boxW := l.width * 55 / 100
	if boxW < 45 {
		boxW = 45
	}
	if boxW > l.width-8 {
		boxW = l.width - 8
	}

	// Content width is boxW minus border (2) and padding (4)
	box := launcherBoxStyle.Width(boxW).Render(content)

	// Center the box
	boxH := lipgloss.Height(box)
	topPad := (l.height - boxH) / 3
	if topPad < 0 {
		topPad = 0
	}
	leftPad := (l.width - lipgloss.Width(box)) / 2
	if leftPad < 0 {
		leftPad = 0
	}

	return strings.Repeat("\n", topPad) +
		strings.Repeat(" ", leftPad) + box
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

	b.WriteString("\n")
	b.WriteString(launcherHintStyle.Render("j/k:field  h/l:option  Enter:launch  Esc:cancel"))
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
