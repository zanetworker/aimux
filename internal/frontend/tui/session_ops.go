package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
	"github.com/zanetworker/aimux/internal/jump"
	"github.com/zanetworker/aimux/internal/terminal"
	"github.com/zanetworker/aimux/internal/trace"
)

func (a App) handleZoomedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	debuglog.Log("tui: zoomed key received: %q (bytes: %x)", key, []byte(key))

	// Clear status hints on any keypress (e.g., "Launched..." or export result)
	a.statusHint = ""
	a.stickyHint = false

	// Exit keys — always work regardless of mode/focus
	switch key {
	case "ctrl+]", "ctrl+\\", "ctrl+g", "ctrl+q":
		debuglog.Log("tui: exit zoom triggered by key %q", key)
		return a.exitZoom()
	}
	if len(key) == 1 && key[0] == 0x1d {
		debuglog.Log("tui: exit zoom triggered by raw 0x1d")
		return a.exitZoom()
	}

	// Esc in split mode: clear trace filter if active, otherwise forward to PTY.
	// Esc is NOT used to exit zoom — use Ctrl+]/g/\ instead.
	// This allows shell features like Ctrl+R (reverse search) to work normally.
	if key == "esc" {
		if a.splitMode && a.splitFocus == "trace" && a.splitTrace != nil && a.splitTrace.HasActiveFilter() {
			a.splitTrace.Update(msg)
			return a, nil
		}
		// Forward Esc to PTY (needed for Ctrl+R cancel, vim escape, etc.)
		a.sessionView.SendKey(key)
		return a, nil
	}

	// Ctrl+b toggles permission mode: close PTY, relaunch with toggled mode
	if key == "ctrl+b" {
		return a, func() tea.Msg { return views.SessionTogglePermsMsg{} }
	}

	// Ctrl+f toggles split/fullscreen — zooms whichever pane is focused
	if key == "ctrl+f" && a.splitTrace != nil {
		a.splitMode = !a.splitMode
		if !a.splitMode {
			// Full-screen the focused pane
			if a.splitFocus == "trace" {
				a.splitTrace.SetSize(a.width, a.height-1)
			} else {
				a.sessionView.SetSize(a.width, a.height)
			}
		} else {
			// Return to split
			leftW := a.width * 40 / 100
			rightW := a.width - leftW - 1
			a.sessionView.SetSize(rightW, a.height)
			a.splitTrace.SetSize(leftW, a.height-3)
		}
		return a, nil
	}

	// Tab switches focus
	if key == "tab" && a.splitMode {
		if a.splitFocus == "trace" {
			a.splitFocus = "session"
		} else {
			a.splitFocus = "trace"
		}
		return a, nil
	}
	if key == "tab" && a.currentView == viewAgents && !a.commandMode && !a.filterMode {
		a.previewFocused = !a.previewFocused
		if a.previewFocused {
			if a.previewSection == "" {
				a.previewSection = "trace"
			}
			a.statusHint = fmt.Sprintf("Preview [%s] (Tab:back, j/k:scroll, up/down:switch section)", a.previewSection)
		} else {
			a.statusHint = ""
		}
		return a, nil
	}

	// Command palette -- intercept ":" before routing to trace or PTY
	if key == ":" {
		a.commandMode = true
		a.commandInput.Reset()
		return a, nil
	}

	// Route keys to trace pane when focused (both split and fullscreen trace)
	if a.splitFocus == "trace" && a.splitTrace != nil {
		// Intercept "e" for export only when NOT in note/filter input mode
		if key == "e" && !a.splitTrace.HasActiveFilter() && !a.splitTrace.NoteMode() {
			a.exportConfirm = true
			a.statusHint = "Export: j:JSONL  o:OTEL  Esc:cancel"
			a.stickyHint = true
			return a, nil
		}
		// Intercept "$" for cost-per-turn toggle
		if key == "$" {
			a.splitTrace.ToggleCostPerTurn()
			return a, nil
		}
		if key == "*" {
			return a.starFromTrace(a.splitTrace.FilePath())
		}
		if key == "C" {
			return a.copySessionIDFromTrace()
		}
		cmd := a.splitTrace.Update(msg)
		return a, cmd
	}

	// Intercept scroll keys in session view
	if tv := a.sessionView.TermView(); tv != nil {
		switch key {
		case "pgup":
			tv.ScrollUp(tv.Height() / 2)
			return a, nil
		case "pgdown":
			tv.ScrollDown(tv.Height() / 2)
			return a, nil
		case "shift+up":
			tv.ScrollUp(1)
			return a, nil
		case "shift+down":
			tv.ScrollDown(1)
			return a, nil
		}
	}

	// Send to PTY session
	a.sessionView.SendKey(key)
	return a, nil
}

func (a App) exitZoom() (tea.Model, tea.Cmd) {
	// Use splitTrace nil check for TUI-specific full-screen detection:
	// the Navigator only tracks state booleans, not TUI objects.
	canReturnToSplit := !a.splitMode && a.splitTrace != nil
	if canReturnToSplit {
		a.ctrl.Nav.SplitMode = false // ensure Navigator matches before ExitZoom
		a.ctrl.Nav.Zoomed = true
	}

	exitedFully := a.ctrl.Nav.ExitZoom()

	if !exitedFully {
		// Returned to split view
		a.splitMode = true
		a.splitFocus = a.ctrl.Nav.SplitFocus
		// Resize back to split layout
		leftW := a.width * 40 / 100
		rightW := a.width - leftW - 1
		a.sessionView.SetSize(rightW, a.height)
		a.splitTrace.SetSize(leftW, a.height-3)
		return a, nil
	}

	// Fully exited
	a.stopActiveTailer()
	a.zoomed = false
	a.splitMode = false
	a.splitTrace = nil
	a.splitLaunchTime = time.Time{}
	a.splitLoading = false
	a.layout.SetZoomed(false)
	a.sessionView.Close()
	return a, nil
}

// returnToAgentsIfZoomed exits zoom/split mode after a kill and returns to the
// agents list. If the user is already on the agents list, it's a no-op.

func (a App) returnToAgentsIfZoomed() (tea.Model, tea.Cmd) {
	if a.zoomed || a.splitMode || a.splitTrace != nil {
		a.stopActiveTailer()
		a.zoomed = false
		a.splitMode = false
		a.splitTrace = nil
		a.splitLaunchTime = time.Time{}
		a.splitLoading = false
		a.layout.SetZoomed(false)
		a.sessionView.Close()
	}
	a.currentView = viewAgents
	a.stickyHint = true
	return a, nil
}

func (a App) handleEnter() (tea.Model, tea.Cmd) {
	if a.currentView != viewAgents {
		return a, nil
	}
	selected := a.agentsView.Selected()
	if selected == nil {
		return a, nil
	}

	// K8s session pods: attach via kubectl exec + tmux.
	if strings.HasPrefix(selected.SessionID, "pod-") {
		return a.openK8sSession(selected)
	}

	// Remote sandbox sessions: re-enter the tmux session.
	// Guard against racing an in-flight launch: while a remote launch is
	// still setting up (splitLoading), the sandbox already shows up as a
	// discovered agent. Opening it here would create a second tmux session
	// that competes with the launch's session, and the two stomp each other
	// (orphaned backends, killed sessions, "no current target" on keys).
	if selected.Location == "remote" {
		if a.splitLoading {
			a.statusHint = "Sandbox is still launching, please wait..."
			return a, nil
		}
		return a.openRemoteSession(selected)
	}

	p := a.providerFor(selected.ProviderName)
	if p == nil {
		a.statusHint = "No provider for " + selected.ProviderName
		return a, nil
	}

	// Resolve session file for the trace pane via the provider.
	sessionFile := selected.SessionFile
	if sessionFile == "" {
		sessionFile = p.FindSessionFile(*selected)
	}

	cmd := p.ResumeCommand(*selected)
	if cmd == nil {
		// No resume possible — fall back to trace-only view
		if sessionFile == "" {
			a.statusHint = "No trace data yet — agent may still be starting"
			return a, nil
		}
		return a.openLogsForAgent(selected, sessionFile)
	}

	// Size the session view for the right half
	rightW := a.width * 60 / 100
	a.sessionView.SetSize(rightW, a.height)

	contentH := a.height - 2
	if contentH < 1 {
		contentH = 24
	}
	contentW := rightW
	if contentW < 1 {
		contentW = 80
	}

	// Build OTEL env prefix for the provider (used by both PTY and tmux paths)
	otelEnvPrefix := ""
	if endpoint := a.cfg.OTELEndpoint(); endpoint != "" {
		otelEnvPrefix = p.OTELEnv(endpoint)
	}

	// Pick backend: direct PTY for embeddable providers, tmux mirror for others
	var backend terminal.SessionBackend
	if p.CanEmbed() {
		// Inject OTEL env vars into the command's environment
		if otelEnvPrefix != "" {
			cmd.Env = otelEnvForCmd(cmd, otelEnvPrefix)
		}
		sess, err := terminal.Start(cmd)
		if err != nil {
			a.statusHint = fmt.Sprintf("Error: %v", err)
			return a, nil
		}
		backend = sess
	} else {
		// Use tmux mirror — attach to existing session if available, else create
		var err error
		if selected.TMuxSession != "" {
			backend, err = terminal.AttachTmux(selected.TMuxSession, contentW, contentH)
		} else {
			backend, err = terminal.StartTmux(cmd, contentW, contentH, a.cfg.ResolveShell(), otelEnvPrefix)
		}
		if err != nil {
			a.statusHint = fmt.Sprintf("Tmux mirror failed: %v", err)
			return a, nil
		}
	}

	// Set perm mode indicator from the running agent's known mode
	permMode := selected.PermissionMode
	if permMode == "" || permMode == "default" {
		permMode = "default"
	}
	a.sessionView.SetPermMode(permMode)

	teaCmd, err := a.sessionView.Open(selected, backend)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error: %v", err)
		return a, nil
	}

	// Create live trace pane with annotations loaded
	if sessionFile != "" {
		leftW := a.width - rightW
		a.splitTrace = views.NewLogsView(selected.PID, sessionFile, a.parserForProvider(p))
		a.splitTrace.SetSessionCost(selected.EstCostUSD)
		a.splitTrace.SetSize(leftW, a.height-1)

		// Set up evaluation store and load annotations into split trace
		sessionID := selected.SessionID
		if sessionID == "" {
			sessionID = fmt.Sprintf("pid-%d", selected.PID)
		}
		a.evalSessionID = sessionID
		a.evalStore = evaluation.NewStore(sessionID)
		annotations, _ := a.evalStore.Load()
		annotMap := make(map[int]string)
		noteMap := make(map[int]string)
		for _, ann := range annotations {
			annotMap[ann.Turn] = ann.Label
			if ann.Note != "" {
				noteMap[ann.Turn] = ann.Note
			}
		}
		a.splitTrace.SetAnnotations(annotMap)
		a.splitTrace.SetNotes(noteMap)

		// Start live file tailer for real-time trace updates.
		a.activeTailer = startTraceTailer(sessionFile, a.traceRefresh)
	}

	a.zoomed = true
	a.splitMode = true
	a.splitFocus = "trace" // start with focus on the trace pane (left)
	a.splitLoading = true  // show loading placeholder until first PTY output
	a.layout.SetZoomed(true)
	cmds := []tea.Cmd{teaCmd}
	if a.activeTailer != nil {
		cmds = append(cmds, a.waitForTraceRefresh())
	}
	return a, tea.Batch(cmds...)
}

// openK8sSession attaches to a K8s session pod via kubectl exec + tmux.
// The pod runs `sleep infinity` with a tmux session named "main" inside.

func (a App) openK8sSession(selected *agent.Agent) (tea.Model, tea.Cmd) {
	// Don't try to exec into unhealthy pods.
	if selected.Status == agent.StatusError {
		a.statusHint = fmt.Sprintf("Cannot attach: pod is unhealthy (%s)", selected.LastAction)
		a.stickyHint = true
		return a, nil
	}

	// Extract pod name and namespace from SessionID and WorkingDir.
	podName := strings.TrimPrefix(selected.SessionID, "pod-")
	namespace := "agents"
	if parts := strings.SplitN(strings.TrimPrefix(selected.WorkingDir, "k8s://"), "/", 2); len(parts) == 2 {
		namespace = parts[0]
	}

	// K8s sessions are zoomed full-screen (not split), so use full width.
	contentW := a.width
	contentH := a.height - 2
	if contentW < 1 {
		contentW = 80
	}
	if contentH < 1 {
		contentH = 24
	}

	backend, err := terminal.NewKubectlExec(podName, namespace, "", contentW, contentH)
	if err != nil {
		a.statusHint = fmt.Sprintf("kubectl exec failed: %v", err)
		return a, nil
	}

	a.sessionView.SetSize(a.width, a.height)
	teaCmd, err := a.sessionView.Open(selected, backend)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error: %v", err)
		return a, nil
	}

	// Set up the remote session: env vars, tmux, then claude.
	// kubectl exec gives us a bash shell. We set env vars, start tmux
	// (for session persistence), then launch claude inside tmux.
	go func() {
		time.Sleep(500 * time.Millisecond)

		// Set TERM for color support.
		if _, err := backend.Write([]byte("export TERM=xterm-256color\n")); err != nil {
			debuglog.Log("k8s setup: write TERM failed: %v", err)
		}
		time.Sleep(100 * time.Millisecond)

		// Forward non-secret config env vars from local shell.
		// Credentials (API keys, GCP ADC) are NOT forwarded here.
		// They're injected via K8s secrets (created by auto-provisioning
		// in ensureAuthSecrets or manually via kubectl create secret).
		// Only non-sensitive configuration values are sent via terminal.
		configEnvVars := []string{
			"CLAUDE_CODE_USE_VERTEX",
			"CLOUD_ML_REGION",
			"ANTHROPIC_VERTEX_PROJECT_ID",
			"ANTHROPIC_VERTEX_REGION",
		}
		for _, key := range configEnvVars {
			if val := os.Getenv(key); val != "" {
				_, _ = fmt.Fprintf(backend, "export %s=%q\n", key, val)
				time.Sleep(50 * time.Millisecond)
			}
		}

		// Launch claude. Use exec to replace the shell so there's no
		// command echo or leftover shell prompt. The clear removes
		// any env export output from the screen.
		if _, err := backend.Write([]byte("cd /workspace 2>/dev/null\n")); err != nil {
			debuglog.Log("k8s setup: write cd failed: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		if _, err := backend.Write([]byte("clear && exec claude\n")); err != nil {
			debuglog.Log("k8s setup: write claude launch failed: %v", err)
		}
	}()

	a.zoomed = true
	a.splitMode = false
	a.layout.SetZoomed(true)
	return a, teaCmd
}

// openRemoteSession re-enters a remote sandbox by opening a fresh
// "openshell sandbox connect" PTY (no tmux). The sandbox is a gateway
// resource that persists across connects, so a new connection reattaches to
// the same sandbox each time.

func (a App) openRemoteSession(selected *agent.Agent) (tea.Model, tea.Cmd) {
	rightW := a.width * 60 / 100
	a.sessionView.SetSize(rightW, a.height)

	contentH := a.height - 2
	if contentH < 1 {
		contentH = 24
	}
	contentW := rightW
	if contentW < 1 {
		contentW = 80
	}

	sandboxName := selected.SandboxName
	if sandboxName == "" {
		sandboxName = selected.Name
	}

	backend, err := terminal.NewOpenShellExec(sandboxName, "", false, contentW, contentH)
	if err != nil {
		debuglog.Log("remote session: openshell connect FAILED for %s: %v", sandboxName, err)
		a.statusHint = fmt.Sprintf("Cannot connect to %s: %v", selected.Name, err)
		return a, nil
	}
	debuglog.Log("remote session: connected to sandbox %s", sandboxName)

	// Resolve the pinned Claude session UUID. The selected agent is usually the
	// orchestrator-discovered record, which lacks it, so recover it from the
	// launch-time map keyed by sandbox name.
	sessionID := selected.SessionID
	if !controller.UUIDValid(sessionID) {
		if mapped := a.remoteSessionIDs.Get(sandboxName); mapped != "" {
			sessionID = mapped
			debuglog.Log("remote session: recovered pinned session id %s for %s", sessionID, sandboxName)
		}
	}

	teaCmd, err := a.sessionView.Open(selected, backend)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error: %v", err)
		return a, nil
	}

	// A fresh connect gives a bare shell (the previous agent process ended
	// when the last connection closed). Resume the same Claude session so the
	// conversation and telemetry session.id continue, keeping the trace pane's
	// history. With the pinned UUID we resume it explicitly; without it we fall
	// back to --continue (Claude resumes its most recent conversation on disk).
	resumeCmd := controller.RemoteAgentCommand(selected.ProviderName, sessionID, true)
	if resumeCmd == selected.ProviderName && selected.ProviderName == "claude" {
		resumeCmd = "claude --continue"
	}
	go sendAgentCommand(backend, resumeCmd)

	// Reattaching to a full-screen agent leaves a stale screen; nudge a
	// redraw once the reconnect settles.
	go nudgeRedraw(backend, contentW, contentH)

	// Set up split view with trace pane on the left
	leftW := a.width - rightW - 1
	a.splitLaunchTime = time.Now()
	a.evalSessionID = sessionID

	// Use OTEL parser for remote sessions (no local session file)
	remoteParser := a.parserForRemote(sessionID, sandboxName)
	a.splitTrace = views.NewLogsView(0, "", remoteParser)
	a.splitTrace.SetSize(leftW, a.height-1)

	a.zoomed = true
	a.splitMode = true
	a.splitFocus = "session"
	a.splitLoading = true
	a.layout.SetZoomed(true)
	a.statusHint = fmt.Sprintf("Attached to %s", selected.Name)
	return a, teaCmd
}

// sendAgentCommand waits briefly for the sandbox shell to be ready, then types
// the given command into the PTY. Runs in its own goroutine so the TUI stays
// responsive while the connection establishes.

func sendAgentCommand(backend terminal.SessionBackend, cmd string) {
	time.Sleep(3 * time.Second)
	if _, err := backend.Write([]byte(cmd + "\n")); err != nil {
		debuglog.Log("remote: failed to send agent command %q: %v", cmd, err)
		return
	}
	debuglog.Log("remote: sent agent command %q", cmd)
}

// nudgeRedraw forces a full repaint of a full-screen TUI (e.g. claude) that is
// being reattached to after a reconnect. Reattaching to a running TUI leaves a
// stale/garbled screen until the app receives a window-size change; toggling
// the PTY size by one column and back delivers two SIGWINCHs that trigger a
// clean redraw. Runs in its own goroutine after the connection settles.

func nudgeRedraw(backend terminal.SessionBackend, cols, rows int) {
	if cols < 2 || rows < 1 {
		return
	}
	time.Sleep(1500 * time.Millisecond)
	_ = backend.Resize(cols-1, rows)
	time.Sleep(150 * time.Millisecond)
	_ = backend.Resize(cols, rows)
	debuglog.Log("remote: sent redraw nudge (%dx%d)", cols, rows)
}

// providerFor returns the full provider.Provider whose Name() matches, or nil.

func (a App) handleJump() (tea.Model, tea.Cmd) {
	selected := a.agentsView.Selected()
	if selected == nil {
		return a, nil
	}
	// J always opens a zoomed session (same as Enter)
	return a.handleEnter()
}

func (a App) jumpToSession() (tea.Model, tea.Cmd) {
	selected := a.agentsView.Selected()
	if selected == nil {
		a.statusHint = "No agent selected"
		return a, nil
	}

	p := a.providerFor(selected.ProviderName)
	if p == nil {
		a.statusHint = "No provider for " + selected.ProviderName
		return a, nil
	}

	cmd := p.ResumeCommand(*selected)
	if cmd == nil {
		a.statusHint = "Cannot resume this session"
		return a, nil
	}

	// Build the command string for the external terminal
	cmdStr := strings.Join(cmd.Args, " ")
	if cmd.Dir != "" {
		cmdStr = fmt.Sprintf("cd %q && %s", cmd.Dir, cmdStr)
	}

	if jump.IsITerm2() {
		if err := jump.ITerm2SplitPane(cmdStr); err != nil {
			a.statusHint = fmt.Sprintf("iTerm split failed: %v", err)
		} else {
			a.statusHint = "Opened in iTerm split pane"
		}
	} else if jump.IsInsideTmux() {
		// Create a tmux split pane
		tmuxCmd := exec.Command("tmux", "split-window", "-h", cmdStr) // #nosec G204
		if err := tmuxCmd.Run(); err != nil {
			a.statusHint = fmt.Sprintf("tmux split failed: %v", err)
		} else {
			a.statusHint = "Opened in tmux split pane"
		}
	} else {
		a.statusHint = fmt.Sprintf("Run manually: %s", cmdStr)
	}

	return a, nil
}

// resumeSession opens a past session in split view: trace on left, live Claude on right.
// Mirrors handleEnter() but builds the command from session history instead of a running agent.
// The mode parameter controls permission mode ("bypass", "plan", etc.); empty or "default" means no flag.

func (a App) resumeSession(sessionID, workingDir, sessionFilePath, mode string) (tea.Model, tea.Cmd) {
	debuglog.Log("tui: resumeSession start: id=%q dir=%q file=%q mode=%q", sessionID, workingDir, sessionFilePath, mode)

	claudeBin := "claude"
	if path, err := exec.LookPath("claude"); err == nil {
		claudeBin = path
	}

	if workingDir != "" {
		if info, err := os.Stat(workingDir); err == nil && info.IsDir() {
			// valid
		} else {
			debuglog.Log("tui: resumeSession: workingDir %q not found", workingDir)
			a.statusHint = "Cannot resolve project directory for resume"
			return a, nil
		}
	}
	cmd := controller.ResumeCommand(claudeBin, sessionID, workingDir, mode)

	// Size the session view for the right half
	rightW := a.width * 60 / 100
	a.sessionView.SetSize(rightW, a.height)

	// Start embedded PTY (Claude supports embedding)
	debuglog.Log("tui: resumeSession: starting PTY for %q", claudeBin)
	sess, err := terminal.Start(cmd)
	if err != nil {
		debuglog.Log("tui: resumeSession: PTY start failed: %v", err)
		a.statusHint = fmt.Sprintf("Resume failed: %v", err)
		return a, nil
	}
	debuglog.Log("tui: resumeSession: PTY started, opening session view")

	// Build a minimal agent for the session view
	resumeAgent := &agent.Agent{
		ProviderName: "claude",
		SessionID:    sessionID,
		WorkingDir:   workingDir,
	}

	a.sessionView.SetPermMode(mode)
	teaCmd, err := a.sessionView.Open(resumeAgent, sess)
	if err != nil {
		debuglog.Log("tui: resumeSession: session view open failed: %v", err)
		a.statusHint = fmt.Sprintf("Error opening session: %v", err)
		return a, nil
	}

	// Create trace pane on the left from the session file
	if sessionFilePath != "" {
		debuglog.Log("tui: resumeSession: parsing trace file %q", sessionFilePath)
		leftW := a.width - rightW
		claudeProvider := a.providerFor("claude")
		var parser func(string) ([]trace.Turn, error)
		if claudeProvider != nil {
			parser = claudeProvider.ParseTrace
		}
		a.splitTrace = views.NewLogsView(0, sessionFilePath, parser)
		a.splitTrace.SetSize(leftW, a.height-1)
		debuglog.Log("tui: resumeSession: trace loaded, splitTrace is set")

		// Load existing annotations
		a.evalSessionID = sessionID
		a.evalStore = evaluation.NewStore(sessionID)
		annotations, _ := a.evalStore.Load()
		annotMap := make(map[int]string)
		noteMap := make(map[int]string)
		for _, ann := range annotations {
			annotMap[ann.Turn] = ann.Label
			if ann.Note != "" {
				noteMap[ann.Turn] = ann.Note
			}
		}
		a.splitTrace.SetAnnotations(annotMap)
		a.splitTrace.SetNotes(noteMap)

		// Start live file tailer for real-time trace updates.
		a.activeTailer = startTraceTailer(sessionFilePath, a.traceRefresh)
	} else {
		debuglog.Log("tui: resumeSession: no session file, splitTrace will be nil")
	}

	a.zoomed = true
	a.splitMode = true
	a.splitFocus = "session" // start with focus on the live session (right)
	a.splitLoading = true    // show loading placeholder until first PTY output
	a.layout.SetZoomed(true)
	debuglog.Log("tui: resumeSession complete: zoomed=%v splitMode=%v splitFocus=%q splitTrace=%v", a.zoomed, a.splitMode, a.splitFocus, a.splitTrace != nil)
	cmds := []tea.Cmd{teaCmd}
	if a.activeTailer != nil {
		cmds = append(cmds, a.waitForTraceRefresh())
	}
	return a, tea.Batch(cmds...)
}

// promptKill shows a confirmation prompt before killing the selected agent.
// For session-only entries (PID=0), offers to remove and delete trace files.
