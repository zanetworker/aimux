package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- Messages ---

// NewSessionMsg is emitted when the user confirms a new session launch.
type NewSessionMsg struct {
	Where    string // "local", "local-k8s", "remote"
	Provider string // "claude", "codex", "gemini"
	Dir      string
}

// NewTaskMsg is emitted when the user confirms a new task launch.
type NewTaskMsg struct {
	Where    string // "local", "remote"
	Provider string // "claude", "gemini"
	Prompt   string
}

// NewPickerCancelMsg is emitted when the user closes the picker.
type NewPickerCancelMsg struct{}

// --- Styles ---

var (
	npBoxBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#5F87FF"))
	npTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#5F87FF"))
	npLabelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF"))
	npSelectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB")).
			Background(lipgloss.Color("#1E3A5F")).
			Bold(true)
	npOptionStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF"))
	npActiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#111827")).
				Background(lipgloss.Color("#5F87FF")).
				Bold(true).
				Padding(0, 1)
	npInactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF")).
				Padding(0, 1)
	npHintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
	npInputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06B6D4"))
	npDescStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).
			Italic(true)
	npDisabledStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4B5563"))
	npStatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F59E0B"))
)

// --- Types ---

type npLevel int

const (
	npLevelPicker  npLevel = iota // Level 1: Session or Task
	npLevelSession                // Level 2a: Session launcher
	npLevelTask                   // Level 2b: Task launcher
)

// ProviderSupport describes which modes a provider supports.
type ProviderSupport struct {
	Name          string
	LocalSession  bool // always true for all providers
	LocalK8s      bool // true for claude only (needs MCP)
	RemoteSession bool // true for claude only (session pod exists)
	RemoteTask    bool // true for claude, gemini (worker images exist)
}

// NewPickerView renders the :new picker overlay.
type NewPickerView struct {
	level  npLevel
	width  int
	height int
	k8s    bool // true when K8s is enabled in config

	// Level 1
	pickerCursor int // 0=Session, 1=Task

	// Session launcher fields
	sessionWhereOptions []string
	sessionWhereCursor  int
	sessionProviders    []string
	sessionProvCursor   int
	sessionField        int // 0=where, 1=provider

	// Task launcher fields
	taskWhereOptions []string
	taskWhereCursor  int
	taskProviders    []string
	taskProvCursor   int
	taskPrompt       string
	taskField        int // 0=where, 1=provider, 2=prompt

	// Provider support matrix
	providerSupport []ProviderSupport

	// Status message line
	statusMsg string

	// K8s health status (checked once when picker opens)
	k8sHealth *K8sHealth
}

// K8sHealth mirrors the provider health check result for display.
type K8sHealth struct {
	Configured  bool
	RedisOK     bool
	RedisErr    string
	ClusterOK   bool
	ClusterErr  string
	Deployments []string
}

// NewPickerConfig holds the parameters needed to create a NewPickerView.
type NewPickerConfig struct {
	K8sEnabled bool
	Providers  []ProviderSupport
	Health     *K8sHealth        // nil when K8s is not configured
	RecentDirs []RecentDirEntry  // recent directories for session launcher
}

// DefaultProviderSupport returns the default support matrix.
func DefaultProviderSupport() []ProviderSupport {
	return []ProviderSupport{
		{Name: "claude", LocalSession: true, LocalK8s: true, RemoteSession: true, RemoteTask: true},
		{Name: "codex", LocalSession: true, LocalK8s: false, RemoteSession: false, RemoteTask: false},
		{Name: "gemini", LocalSession: true, LocalK8s: false, RemoteSession: false, RemoteTask: true},
	}
}

// NewNewPickerView creates a new :new picker overlay.
func NewNewPickerView(cfg NewPickerConfig) *NewPickerView {
	providers := cfg.Providers
	if len(providers) == 0 {
		providers = DefaultProviderSupport()
	}

	providerNames := make([]string, len(providers))
	for i, p := range providers {
		providerNames[i] = p.Name
	}

	sessionWhere := []string{"Local"}
	if cfg.K8sEnabled {
		sessionWhere = append(sessionWhere, "Local+K8s", "Remote (pod)")
	}

	taskWhere := []string{"Local"}
	if cfg.K8sEnabled {
		taskWhere = append(taskWhere, "Remote")
	}

	// Task providers: only those that support local tasks (all) or remote tasks
	// We include all providers in the list; greying is handled at render/emit time.
	taskProviders := make([]string, 0, len(providers))
	for _, p := range providers {
		if p.LocalSession { // all providers can run local tasks
			taskProviders = append(taskProviders, p.Name)
		}
	}
	if len(taskProviders) == 0 {
		taskProviders = []string{"claude"}
	}

	return &NewPickerView{
		level:               npLevelPicker,
		k8s:                 cfg.K8sEnabled,
		sessionWhereOptions: sessionWhere,
		sessionProviders:    providerNames,
		taskWhereOptions:    taskWhere,
		taskProviders:       taskProviders,
		providerSupport:     providers,
		k8sHealth:           cfg.Health,
	}
}

// k8sReady returns true when both Redis and cluster are accessible.
func (v *NewPickerView) k8sReady() bool {
	return v.k8sHealth != nil && v.k8sHealth.RedisOK && v.k8sHealth.ClusterOK
}

// k8sBlockReason returns a user-facing reason why K8s options are unavailable,
// or empty string if everything is fine.
func (v *NewPickerView) k8sBlockReason() string {
	if v.k8sHealth == nil {
		return "K8s not configured"
	}
	if !v.k8sHealth.RedisOK {
		return "Redis unreachable: " + v.k8sHealth.RedisErr
	}
	if !v.k8sHealth.ClusterOK {
		return "K8s cluster unreachable: " + v.k8sHealth.ClusterErr
	}
	return ""
}

// SetStatus sets a status/error message displayed in the picker.
func (v *NewPickerView) SetStatus(msg string) {
	v.statusMsg = msg
}

// SetSize sets the available dimensions for the overlay.
func (v *NewPickerView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// Init satisfies the tea.Model interface (no-op).
func (v *NewPickerView) Init() tea.Cmd {
	return nil
}

// Update handles key messages and returns a tea.Cmd if the picker emits
// a result message or cancellation.
func (v *NewPickerView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()

		if key == "esc" {
			return v, v.handleEsc()
		}
		if key == "q" {
			return v, func() tea.Msg { return NewPickerCancelMsg{} }
		}

		switch v.level {
		case npLevelPicker:
			return v, v.updatePicker(key)
		case npLevelSession:
			return v, v.updateSession(key)
		case npLevelTask:
			return v, v.updateTask(key)
		}
	}
	return v, nil
}

func (v *NewPickerView) handleEsc() tea.Cmd {
	switch v.level {
	case npLevelPicker:
		return func() tea.Msg { return NewPickerCancelMsg{} }
	case npLevelSession, npLevelTask:
		v.level = npLevelPicker
		v.statusMsg = ""
		return nil
	}
	return nil
}

func (v *NewPickerView) updatePicker(key string) tea.Cmd {
	switch key {
	case "j", "down":
		if v.pickerCursor < 1 {
			v.pickerCursor++
		}
	case "k", "up":
		if v.pickerCursor > 0 {
			v.pickerCursor--
		}
	case "s", "S":
		v.level = npLevelSession
		v.sessionField = 0
		v.statusMsg = ""
		return nil
	case "t", "T":
		v.level = npLevelTask
		v.taskField = 0
		v.statusMsg = ""
		return nil
	case "enter":
		if v.pickerCursor == 0 {
			v.level = npLevelSession
			v.sessionField = 0
		} else {
			v.level = npLevelTask
			v.taskField = 0
		}
		v.statusMsg = ""
	}
	return nil
}

func (v *NewPickerView) updateSession(key string) tea.Cmd {
	// Clear status on any navigation
	v.statusMsg = ""

	switch key {
	case "tab":
		v.sessionField = (v.sessionField + 1) % 2
	case "shift+tab":
		v.sessionField = (v.sessionField + 1) % 2
	case "j", "down":
		v.sessionField = (v.sessionField + 1) % 2
	case "k", "up":
		v.sessionField = (v.sessionField + 1) % 2
	case "l", "right":
		switch v.sessionField {
		case 0:
			if v.sessionWhereCursor < len(v.sessionWhereOptions)-1 {
				v.sessionWhereCursor++
			}
		case 1:
			if v.sessionProvCursor < len(v.sessionProviders)-1 {
				v.sessionProvCursor++
			}
		}
	case "h", "left":
		switch v.sessionField {
		case 0:
			if v.sessionWhereCursor > 0 {
				v.sessionWhereCursor--
			}
		case 1:
			if v.sessionProvCursor > 0 {
				v.sessionProvCursor--
			}
		}
	case "enter":
		return v.emitSession()
	}
	return nil
}

func (v *NewPickerView) updateTask(key string) tea.Cmd {
	// Clear status on any navigation
	v.statusMsg = ""

	// When on the text input field, handle typing first
	if v.taskField == 2 {
		switch key {
		case "tab":
			v.taskField = 0
			return nil
		case "shift+tab":
			v.taskField = (v.taskField + 2) % 3
			return nil
		case "j", "down":
			v.taskField = 0
		case "k", "up":
			v.taskField--
		case "backspace":
			if len(v.taskPrompt) > 0 {
				v.taskPrompt = v.taskPrompt[:len(v.taskPrompt)-1]
			}
		case "enter":
			return v.emitTask()
		default:
			if len(key) == 1 && key >= " " {
				v.taskPrompt += key
			}
		}
		return nil
	}

	switch key {
	case "tab":
		v.taskField = (v.taskField + 1) % 3
	case "shift+tab":
		v.taskField = (v.taskField + 2) % 3
	case "j", "down":
		if v.taskField < 2 {
			v.taskField++
		}
	case "k", "up":
		if v.taskField > 0 {
			v.taskField--
		}
	case "l", "right":
		switch v.taskField {
		case 0:
			if v.taskWhereCursor < len(v.taskWhereOptions)-1 {
				v.taskWhereCursor++
			}
		case 1:
			if v.taskProvCursor < len(v.taskProviders)-1 {
				v.taskProvCursor++
			}
		}
	case "h", "left":
		switch v.taskField {
		case 0:
			if v.taskWhereCursor > 0 {
				v.taskWhereCursor--
			}
		case 1:
			if v.taskProvCursor > 0 {
				v.taskProvCursor--
			}
		}
	case "enter":
		return v.emitTask()
	}
	return nil
}

// isSessionProviderSupported checks if the current provider+where combination
// is supported for sessions.
func (v *NewPickerView) isSessionProviderSupported(providerName, whereLabel string) bool {
	ps := v.findProviderSupport(providerName)
	if ps == nil {
		return false
	}
	switch whereLabel {
	case "Local":
		return ps.LocalSession
	case "Local+K8s":
		return ps.LocalK8s
	case "Remote (pod)":
		return ps.RemoteSession
	default:
		return ps.LocalSession
	}
}

// isTaskProviderSupported checks if the current provider+where combination
// is supported for tasks.
func (v *NewPickerView) isTaskProviderSupported(providerName, whereLabel string) bool {
	ps := v.findProviderSupport(providerName)
	if ps == nil {
		return false
	}
	switch whereLabel {
	case "Local":
		return true // all providers support local tasks
	case "Remote":
		return ps.RemoteTask
	default:
		return true
	}
}

func (v *NewPickerView) findProviderSupport(name string) *ProviderSupport {
	for i := range v.providerSupport {
		if v.providerSupport[i].Name == name {
			return &v.providerSupport[i]
		}
	}
	return nil
}

func (v *NewPickerView) currentSessionProviderSupported() bool {
	if v.sessionProvCursor >= len(v.sessionProviders) {
		return false
	}
	provider := v.sessionProviders[v.sessionProvCursor]
	where := v.sessionWhereOptions[v.sessionWhereCursor]
	return v.isSessionProviderSupported(provider, where)
}

func (v *NewPickerView) currentTaskProviderSupported() bool {
	if v.taskProvCursor >= len(v.taskProviders) {
		return false
	}
	provider := v.taskProviders[v.taskProvCursor]
	where := v.taskWhereOptions[v.taskWhereCursor]
	return v.isTaskProviderSupported(provider, where)
}

func (v *NewPickerView) emitSession() tea.Cmd {
	where := v.resolveSessionWhere()

	if !v.currentSessionProviderSupported() {
		provider := v.sessionProviders[v.sessionProvCursor]
		whereLabel := v.sessionWhereOptions[v.sessionWhereCursor]
		v.statusMsg = provider + " is not supported for " + whereLabel + " sessions yet"
		return nil
	}

	provider := v.sessionProviders[v.sessionProvCursor]

	msg := NewSessionMsg{
		Where:    where,
		Provider: provider,
	}
	return func() tea.Msg { return msg }
}

func (v *NewPickerView) emitTask() tea.Cmd {
	where := v.resolveTaskWhere()

	if !v.currentTaskProviderSupported() {
		provider := v.taskProviders[v.taskProvCursor]
		whereLabel := v.taskWhereOptions[v.taskWhereCursor]
		v.statusMsg = provider + " is not supported for " + whereLabel + " tasks yet"
		return nil
	}

	if strings.TrimSpace(v.taskPrompt) == "" {
		v.statusMsg = "Enter a prompt to launch"
		return nil
	}

	provider := v.taskProviders[v.taskProvCursor]
	prompt := v.taskPrompt

	msg := NewTaskMsg{
		Where:    where,
		Provider: provider,
		Prompt:   prompt,
	}
	return func() tea.Msg { return msg }
}

func (v *NewPickerView) resolveSessionWhere() string {
	label := v.sessionWhereOptions[v.sessionWhereCursor]
	switch label {
	case "Local":
		return "local"
	case "Local+K8s":
		return "local-k8s"
	case "Remote (pod)":
		return "remote"
	default:
		return "local"
	}
}

func (v *NewPickerView) resolveTaskWhere() string {
	label := v.taskWhereOptions[v.taskWhereCursor]
	switch label {
	case "Local":
		return "local"
	case "Remote":
		return "remote"
	default:
		return "local"
	}
}

// --- Description helpers ---

func sessionWhereDescription(label string) string {
	switch label {
	case "Local":
		return "Claude Code on your laptop with local agents"
	case "Local+K8s":
		return "Claude Code on your laptop, tasks run on K8s pods"
	case "Remote (pod)":
		return "Full Claude Code runs in a K8s pod"
	default:
		return ""
	}
}

func taskWhereDescription(label string) string {
	switch label {
	case "Local":
		return "Run task on your machine"
	case "Remote":
		return "Run task on a K8s pod"
	default:
		return ""
	}
}

// --- View rendering ---

// View renders the picker overlay.
func (v *NewPickerView) View() string {
	var content string
	switch v.level {
	case npLevelPicker:
		content = v.viewPicker()
	case npLevelSession:
		content = v.viewSession()
	case npLevelTask:
		content = v.viewTask()
	}

	return v.wrapInBox(content)
}

func (v *NewPickerView) viewPicker() string {
	var b strings.Builder
	b.WriteString(npTitleStyle.Render("Launch New Agent"))
	b.WriteString("\n")
	b.WriteString(npHintStyle.Render(strings.Repeat("─", 56)) + "\n\n")

	type pickerItem struct {
		label string
		desc  string
	}
	items := []pickerItem{
		{"[S]ession", "Interactive AI agent — you chat with it live"},
		{"[T]ask", "Fire-and-forget — agent works, reports results"},
	}
	for i, item := range items {
		cursor := "  "
		style := npOptionStyle
		if i == v.pickerCursor {
			cursor = "▸ "
			style = npSelectedStyle
		}
		b.WriteString(cursor + style.Render(item.label))
		b.WriteString("\n")
		b.WriteString("    " + npDescStyle.Render(item.desc))
		b.WriteString("\n\n")
	}

	b.WriteString(npHintStyle.Render("↑↓:navigate  Enter:select  Esc:close"))
	return b.String()
}

func (v *NewPickerView) renderHealthBar() string {
	if v.k8sHealth == nil || !v.k8s {
		return ""
	}
	var b strings.Builder
	h := v.k8sHealth

	ok := lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))   // green
	fail := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")) // red
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))  // grey

	b.WriteString(dim.Render("K8s: "))
	if h.RedisOK {
		b.WriteString(ok.Render("✓ Redis"))
	} else {
		b.WriteString(fail.Render("✗ Redis"))
	}
	b.WriteString(dim.Render("  "))
	if h.ClusterOK {
		b.WriteString(ok.Render("✓ Cluster"))
		if len(h.Deployments) > 0 {
			b.WriteString(dim.Render(fmt.Sprintf(" (%d deploys)", len(h.Deployments))))
		}
	} else {
		b.WriteString(fail.Render("✗ Cluster"))
	}
	b.WriteString("\n")
	return b.String()
}

func (v *NewPickerView) viewSession() string {
	var b strings.Builder
	b.WriteString(npTitleStyle.Render("New Session"))
	b.WriteString("\n")

	// K8s health status bar
	if healthBar := v.renderHealthBar(); healthBar != "" {
		b.WriteString(healthBar)
	}

	// Separator
	b.WriteString(npHintStyle.Render(strings.Repeat("─", 56)) + "\n\n")

	// Where row
	whereLabel := v.sessionWhereOptions[v.sessionWhereCursor]
	b.WriteString(v.renderProviderAwareTabRow(
		"Where:", v.sessionWhereOptions, v.sessionWhereCursor, v.sessionField == 0, nil,
	))

	// Description for current where selection
	desc := sessionWhereDescription(whereLabel)
	if desc != "" {
		b.WriteString("            " + npDescStyle.Render(desc) + "\n")
	}
	b.WriteString("\n")

	// Provider row with support awareness
	b.WriteString(v.renderSessionProviderRow())
	b.WriteString("\n")

	// Note: directory picking happens in the next step (old launcher)
	b.WriteString(npDescStyle.Render("  Enter to pick directory and launch") + "\n")

	// Status / error line
	b.WriteString("\n")
	if v.statusMsg != "" {
		b.WriteString(npStatusStyle.Render("  " + v.statusMsg) + "\n\n")
	}
	b.WriteString(npHintStyle.Render("↑↓:field  ←→:option  Enter:launch  Esc:back"))
	return b.String()
}

func (v *NewPickerView) viewTask() string {
	var b strings.Builder
	b.WriteString(npTitleStyle.Render("New Task"))
	b.WriteString("\n")

	// K8s health status bar
	if healthBar := v.renderHealthBar(); healthBar != "" {
		b.WriteString(healthBar)
	}

	// Separator
	b.WriteString(npHintStyle.Render(strings.Repeat("─", 56)) + "\n\n")

	// Where row
	whereLabel := v.taskWhereOptions[v.taskWhereCursor]
	b.WriteString(v.renderProviderAwareTabRow(
		"Where:", v.taskWhereOptions, v.taskWhereCursor, v.taskField == 0, nil,
	))

	// Description for current where selection
	desc := taskWhereDescription(whereLabel)
	if desc != "" {
		b.WriteString("            " + npDescStyle.Render(desc) + "\n")
	}
	b.WriteString("\n")

	// Provider row with support awareness
	b.WriteString(v.renderTaskProviderRow())
	b.WriteString("\n")

	// Prompt input
	promptLabel := npLabelStyle.Render("Prompt:     ")
	promptValue := v.taskPrompt
	if promptValue == "" {
		promptValue = "(enter task prompt)"
	}
	if v.taskField == 2 {
		promptLabel = npSelectedStyle.Render("Prompt:     ")
		b.WriteString(promptLabel + npInputStyle.Render(promptValue+"_") + "\n")
	} else {
		b.WriteString(promptLabel + npInputStyle.Render(promptValue) + "\n")
	}

	// Status / error line
	b.WriteString("\n")
	if v.statusMsg != "" {
		b.WriteString(npStatusStyle.Render("  " + v.statusMsg) + "\n\n")
	}
	b.WriteString(npHintStyle.Render("↑↓:field  ←→:option  Enter:launch  Esc:back"))
	return b.String()
}

// renderSessionProviderRow renders the provider row with greyed-out unsupported providers.
func (v *NewPickerView) renderSessionProviderRow() string {
	whereLabel := v.sessionWhereOptions[v.sessionWhereCursor]
	supportedFn := func(name string) bool {
		return v.isSessionProviderSupported(name, whereLabel)
	}
	return v.renderProviderAwareTabRow(
		"Provider:", v.sessionProviders, v.sessionProvCursor, v.sessionField == 1, supportedFn,
	)
}

// renderTaskProviderRow renders the provider row with greyed-out unsupported providers.
func (v *NewPickerView) renderTaskProviderRow() string {
	whereLabel := v.taskWhereOptions[v.taskWhereCursor]
	supportedFn := func(name string) bool {
		return v.isTaskProviderSupported(name, whereLabel)
	}
	return v.renderProviderAwareTabRow(
		"Provider:", v.taskProviders, v.taskProvCursor, v.taskField == 1, supportedFn,
	)
}

// renderProviderAwareTabRow renders a row of tab options. If supportedFn is non-nil,
// unsupported options are rendered in dim grey with "(coming soon)" suffix.
func (v *NewPickerView) renderProviderAwareTabRow(label string, options []string, cursor int, active bool, supportedFn func(string) bool) string {
	row := npLabelStyle.Render(padRight(label, 12))
	for i, opt := range options {
		supported := supportedFn == nil || supportedFn(opt)
		displayOpt := opt
		if !supported {
			displayOpt = opt + " (coming soon)"
		}

		if i == cursor {
			if !supported {
				// Selected but unsupported: dim style
				s := npDisabledStyle.Bold(true).Padding(0, 1)
				row += s.Render(displayOpt)
			} else if active {
				row += npActiveTabStyle.Render(displayOpt)
			} else {
				row += lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB")).Padding(0, 1).Render(displayOpt)
			}
		} else {
			if !supported {
				s := npDisabledStyle.Padding(0, 1)
				row += s.Render(displayOpt)
			} else {
				row += npInactiveTabStyle.Render(displayOpt)
			}
		}
		row += " "
	}
	return row + "\n"
}

func (v *NewPickerView) wrapInBox(content string) string {
	contentLines := strings.Split(content, "\n")
	maxContentW := 0
	for _, line := range contentLines {
		if w := lipgloss.Width(line); w > maxContentW {
			maxContentW = w
		}
	}

	// Minimum box width: 60 chars or 60% of terminal, whichever is larger
	minW := v.width * 60 / 100
	if minW < 60 {
		minW = 60
	}
	if maxContentW < minW {
		maxContentW = minW
	}

	leftPad := (v.width - maxContentW - 4) / 2
	if leftPad < 2 {
		leftPad = 2
	}
	topPad := (v.height - len(contentLines)) / 3
	if topPad < 1 {
		topPad = 1
	}

	var b strings.Builder

	for i := 0; i < topPad; i++ {
		b.WriteString(strings.Repeat(" ", v.width) + "\n")
	}

	borderW := maxContentW + 4
	pad := strings.Repeat(" ", leftPad-1)
	b.WriteString(pad + npBoxBorderStyle.Render("\u250c"+strings.Repeat("\u2500", borderW)+"\u2510") + "\n")

	for _, line := range contentLines {
		lineW := lipgloss.Width(line)
		rightFill := maxContentW - lineW + 2
		if rightFill < 0 {
			rightFill = 0
		}
		b.WriteString(pad + npBoxBorderStyle.Render("\u2502") + "  " + line + strings.Repeat(" ", rightFill) + npBoxBorderStyle.Render("\u2502") + "\n")
	}

	b.WriteString(pad + npBoxBorderStyle.Render("\u2514"+strings.Repeat("\u2500", borderW)+"\u2518") + "\n")

	rendered := topPad + len(contentLines) + 2
	for i := rendered; i < v.height; i++ {
		b.WriteString(strings.Repeat(" ", v.width) + "\n")
	}

	return b.String()
}

// Level returns the current picker level (for testing).
func (v *NewPickerView) Level() npLevel {
	return v.level
}

// StatusMsg returns the current status message (for testing).
func (v *NewPickerView) StatusMsg() string {
	return v.statusMsg
}
