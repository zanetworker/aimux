package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/trace"
)

func (a App) parserForProvider(p provider.Provider) views.TraceParser {
	return a.fileThenOTEL(p.ParseTrace, a.otelSessionIDs)
}

// previewTailTurns is how many turns the preview pane reads from the end of
// a transcript: it shows the last turn and the last three actions.
const previewTailTurns = 20

// previewParserFor is parserForProvider for the preview pane, which parses
// on another goroutine: it reads only the end of the transcript when the
// provider can (tail reports that), and takes the OTEL session IDs now
// rather than reading the views from that goroutine.
func (a App) previewParserFor(p provider.Provider) (parser views.TraceParser, tail bool) {
	ids := a.otelSessionIDs()
	idsNow := func() []string { return ids }
	if tp, ok := p.(provider.TailParser); ok {
		parse := func(path string) ([]trace.Turn, error) { return tp.ParseTail(path, previewTailTurns) }
		return a.fileThenOTEL(parse, idsNow), true
	}
	return a.fileThenOTEL(p.ParseTrace, idsNow), false
}

// otelSessionIDs are the conversation IDs the OTEL fallback looks up: the
// selected agent's session and the open session view's.
func (a App) otelSessionIDs() []string {
	var sessionIDs []string
	if a.agentsView != nil {
		if selected := a.agentsView.Selected(); selected != nil && selected.SessionID != "" {
			sessionIDs = append(sessionIDs, selected.SessionID)
		}
	}
	if a.sessionView != nil && a.sessionView.Agent() != nil {
		ag := a.sessionView.Agent()
		if ag.SessionID != "" {
			sessionIDs = append(sessionIDs, ag.SessionID)
		}
		if ag.TMuxSession != "" {
			sessionIDs = append(sessionIDs, ag.TMuxSession)
		}
	}
	return sessionIDs
}

// fileThenOTEL returns a TraceParser that parses the file with parse and
// falls back to the OTEL store's conversations named by sessionIDs.
func (a App) fileThenOTEL(parse views.TraceParser, sessionIDs func() []string) views.TraceParser {
	return func(filePath string) ([]trace.Turn, error) {
		// File-based parsing for display (has full response text).
		// OTEL receiver still collects data for :export-otel to MLflow/Jaeger.
		if filePath != "" {
			turns, err := parse(filePath)
			if err == nil && len(turns) > 0 {
				// For :new launches, filter out turns from before the launch
				// to avoid showing traces from previous sessions in the same dir.
				if !a.splitLaunchTime.IsZero() {
					var filtered []trace.Turn
					for _, t := range turns {
						// Skip turns from before launch: either explicitly old
						// or missing timestamp (unparsed entries from old sessions)
						if t.Timestamp.IsZero() || t.Timestamp.Before(a.splitLaunchTime) {
							continue
						}
						filtered = append(filtered, t)
					}
					if len(filtered) > 0 {
						// Re-number turns
						for i := range filtered {
							filtered[i].Number = i + 1
						}
						return filtered, nil
					}
					// All turns are old, fall through to OTEL
				} else {
					return turns, nil
				}
			}
		}

		// Fall back to OTEL when file isn't available yet
		// (newly launched sessions before session file is created)
		if a.otelStore != nil && a.otelStore.HasData() {
			for _, id := range sessionIDs() {
				if root := a.otelStore.GetByConversation(id); root != nil {
					turns := aimuxotel.SpansToTurns(root)
					if len(turns) > 0 {
						return turns, views.ErrLiveTrace
					}
				}
			}
		}
		return nil, nil
	}
}

func (a App) parserForRemote(otelSessionID, sandboxName string) views.TraceParser {
	return a.remoteParser(otelSessionID, sandboxName, a.localSessionIDs)
}

// previewParserForRemote is parserForRemote for the preview pane, which
// parses on another goroutine: the local session IDs are taken now.
func (a App) previewParserForRemote(otelSessionID, sandboxName string) views.TraceParser {
	ids := a.localSessionIDs()
	return a.remoteParser(otelSessionID, sandboxName, func() map[string]bool { return ids })
}

// localSessionIDs are the session IDs of the local agents in the list.
func (a App) localSessionIDs() map[string]bool {
	localIDs := make(map[string]bool)
	if a.agentsView != nil {
		for _, ag := range a.agentsView.Agents() {
			if ag.Location != "remote" && ag.SessionID != "" {
				localIDs[ag.SessionID] = true
			}
		}
	}
	return localIDs
}

func (a App) remoteParser(otelSessionID, sandboxName string, localSessionIDs func() map[string]bool) views.TraceParser {
	return func(_ string) ([]trace.Turn, error) {
		// Shared path: direct OTEL lookup + session-file fallback.
		turns := controller.RemoteTraceParser(a.otelStore, otelSessionID, sandboxName)
		if len(turns) > 0 {
			return turns, views.ErrLiveTrace
		}

		// TUI-specific: when the session ID injection failed, scan all
		// conversations and exclude known local sessions to find the
		// remote one by elimination.
		if a.otelStore != nil && a.otelStore.HasData() {
			localIDs := localSessionIDs()
			for _, convID := range a.otelStore.ConversationIDs() {
				if localIDs[convID] {
					continue
				}
				root := a.otelStore.GetByConversation(convID)
				if root == nil {
					continue
				}
				turns := aimuxotel.SpansToTurns(root)
				if len(turns) > 0 {
					if sandboxName != "" {
						replies := aimuxotel.FetchSessionReplies(sandboxName, otelSessionID)
						aimuxotel.EnrichTurnsWithReplies(turns, replies)
					}
					return turns, views.ErrLiveTrace
				}
			}
		}

		return liveTurns(turns)
	}
}

func (a App) exportTrace() (tea.Model, tea.Cmd) {
	ctx := a.buildExportContext()
	if ctx.SessionID == "" || len(ctx.Turns) == 0 {
		a.statusHint = "Open a trace first (l on an agent or Enter for split view), then :export"
		return a, nil
	}

	result, err := a.ctrl.ExportJSONL(ctx)
	if err != nil {
		a.statusHint = fmt.Sprintf("Export failed: %v", err)
		a.stickyHint = true
		return a, nil
	}

	a.statusHint = fmt.Sprintf("Exported %d turns to %s (press any key to dismiss)", result.Count, result.Path)
	a.stickyHint = true
	return a, nil
}

// exportOTEL sends the current trace + annotations as OTLP/HTTP spans to
// the configured export endpoint (e.g., MLflow, Jaeger).

func (a App) exportOTEL() (tea.Model, tea.Cmd) {
	ctx := a.buildExportContext()
	if ctx.SessionID == "" || len(ctx.Turns) == 0 {
		a.statusHint = "Open a trace first (l on an agent or Enter for split view), then :export-otel"
		return a, nil
	}

	result, err := a.ctrl.ExportOTEL(ctx)
	if err != nil {
		a.statusHint = fmt.Sprintf("OTEL export failed: %v", err)
		a.stickyHint = true
		return a, nil
	}

	a.statusHint = fmt.Sprintf("Exported %d turns to %s (press any key to dismiss)", result.Count, result.Path)
	a.stickyHint = true
	return a, nil
}

// jumpToSession opens the selected agent's session in a separate terminal pane
// (iTerm split or tmux pane). Used for providers like Codex whose TUI can't embed.

func (a App) openLogsForAgent(ag *agent.Agent, sessionFile string) (tea.Model, tea.Cmd) {
	p := a.providerFor(ag.ProviderName)
	var parser views.TraceParser
	if p != nil {
		parser = a.parserForProvider(p)
	}
	a.logsView = views.NewLogsView(ag.PID, sessionFile, parser)
	a.logsView.SetSessionCost(ag.EstCostUSD)
	contentHeight := a.height - a.headerView.Height()
	if contentHeight < 1 {
		contentHeight = 10
	}
	a.logsView.SetSize(a.width, contentHeight)

	// Set up evaluation store
	sessionID := ag.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("pid-%d", ag.PID)
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
	a.logsView.SetAnnotations(annotMap)
	a.logsView.SetNotes(noteMap)

	label := fmt.Sprintf("Trace [%s: %s]", ag.ProviderName, ag.ShortProject())
	a.statusHint = "J:jump  a:annotate  N:note  :export  :export-otel"
	return a.navigateTo(viewLogs, label)
}

func (a App) openLogsForSelected() (tea.Model, tea.Cmd) {
	selected := a.agentsView.Selected()
	if selected == nil {
		return a, nil
	}
	p := a.providerFor(selected.ProviderName)
	sessionFile := selected.SessionFile
	if sessionFile == "" {
		if p != nil {
			sessionFile = p.FindSessionFile(*selected)
		}
	}
	var parser views.TraceParser
	if p != nil {
		parser = a.parserForProvider(p)
	}
	a.logsView = views.NewLogsView(selected.PID, sessionFile, parser)
	a.logsView.SetSessionCost(selected.EstCostUSD)
	contentHeight := a.height - a.headerView.Height()
	if contentHeight < 1 {
		contentHeight = 10
	}
	a.logsView.SetSize(a.width, contentHeight)

	// Set up evaluation store and load existing annotations
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
	a.logsView.SetAnnotations(annotMap)
	a.logsView.SetNotes(noteMap)

	return a.navigateTo(viewLogs, fmt.Sprintf("Logs [PID %d]", selected.PID))
}

func (a App) activeTraceTurns() []trace.Turn {
	if a.logsView != nil {
		return a.logsView.Turns()
	}
	if a.splitTrace != nil {
		return a.splitTrace.Turns()
	}
	return nil
}

// buildExportContext assembles an ExportContext from the current TUI state.
// This is the bridge between TUI-specific state and UI-agnostic controller logic.

func (a App) buildExportContext() controller.ExportContext {
	turns := a.activeTraceTurns()
	providerName := ""
	if selected := a.agentsView.Selected(); selected != nil {
		providerName = selected.ProviderName
	}
	if providerName == "" && a.sessionView != nil && a.sessionView.Agent() != nil {
		providerName = a.sessionView.Agent().ProviderName
	}

	return controller.ExportContext{
		SessionID:    a.activeTraceSessionID(),
		SessionFile:  a.activeTraceFilePath(),
		ProviderName: providerName,
		Turns:        controller.TurnsToInputs(turns),
		EvalStore:    a.evalStore,
	}
}

// activeTraceFilePath returns the session file path for the active trace context.

func (a App) activeTraceFilePath() string {
	if a.logsView != nil {
		return a.logsView.FilePath()
	}
	if a.splitTrace != nil {
		return a.splitTrace.FilePath()
	}
	if a.sessionView != nil && a.sessionView.Agent() != nil {
		return a.sessionView.Agent().SessionFile
	}
	return ""
}

// pollSessionFile returns a tea.Cmd that fires a sessionFilePollMsg after 200ms.

func (a App) pollSessionFile(deadline time.Time) tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(_ time.Time) tea.Msg {
		return sessionFilePollMsg{deadline: deadline}
	})
}

// activeTraceSessionID returns the session ID for the active trace context.

func (a App) activeTraceSessionID() string {
	if a.evalSessionID != "" {
		return a.evalSessionID
	}
	// Derive from session view agent in split mode
	if a.sessionView != nil && a.sessionView.Agent() != nil {
		ag := a.sessionView.Agent()
		if ag.SessionID != "" {
			return ag.SessionID
		}
		return fmt.Sprintf("pid-%d", ag.PID)
	}
	return ""
}

// startTraceTailer creates a Tailer for the given session file. When new lines
// are appended, it sends a non-blocking signal on the channel so the Bubble Tea
// event loop can re-parse the trace. Returns nil if the file cannot be watched.

func startTraceTailer(path string, ch chan struct{}) *trace.Tailer {
	tailer, err := trace.NewTailer(path, func(_ string) {
		// Non-blocking send: if a signal is already pending, skip.
		select {
		case ch <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return nil
	}
	return tailer
}

// waitForTraceRefresh returns a tea.Cmd that blocks until the traceRefresh
// channel receives a signal, then delivers a traceRefreshMsg.

func (a App) waitForTraceRefresh() tea.Cmd {
	return func() tea.Msg {
		<-a.traceRefresh
		return traceRefreshMsg{}
	}
}

// stopActiveTailer stops the active file tailer and drains the channel.

func (a *App) stopActiveTailer() {
	if a.activeTailer != nil {
		a.activeTailer.Stop()
		a.activeTailer = nil
	}
	// Drain any pending signal so it doesn't fire after split exit.
	select {
	case <-a.traceRefresh:
	default:
	}
}

// otelEnvForCmd merges OTEL env vars (from the provider's OTELEnv shell prefix)
// into a cmd.Env slice suitable for exec.Cmd. Starts from the current process
// environment so the child inherits everything else.

func (a *App) agentForLogsView() *agent.Agent {
	if a.logsView == nil {
		return nil
	}
	fp := a.logsView.FilePath()
	for i := range a.instances {
		if a.instances[i].SessionFile == fp {
			return &a.instances[i]
		}
	}
	return nil
}

// liveTurns returns OTEL-backed turns with views.ErrLiveTrace, and no
// turns with no error.
func liveTurns(turns []trace.Turn) ([]trace.Turn, error) {
	if len(turns) == 0 {
		return turns, nil
	}
	return turns, views.ErrLiveTrace
}
