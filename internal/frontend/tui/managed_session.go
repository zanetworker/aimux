package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/sdk"
	"github.com/zanetworker/aimux/internal/session"
)

type managedSessionEventMsg struct {
	event session.SessionEvent
}

type managedSessionDoneMsg struct {
	result *sdk.ClaudeResult
	err    error
}

func startManagedSession(model, prompt, dir string) tea.Cmd {
	eventCh := make(chan session.SessionEvent, 64)

	// Start the session in background, streaming events to channel
	go func() {
		ctx := context.Background()
		sess, err := sdk.NewClaudeSession(ctx, model, prompt, dir)
		if err != nil {
			close(eventCh)
			return
		}
		_, _ = sess.RunWithEvents(ctx, eventCh)
	}()

	// Return a command that reads the first event
	return listenForSessionEvent(eventCh)
}

func listenForSessionEvent(ch <-chan session.SessionEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return managedSessionDoneMsg{}
		}
		return managedSessionEventMsg{event: event}
	}
}

func continueListening(ch <-chan session.SessionEvent) tea.Cmd {
	return listenForSessionEvent(ch)
}

// sessionEventChan is stored on the App to keep the channel alive across messages.
var sessionEventChan <-chan session.SessionEvent

func startManagedSessionStreaming(model, prompt, dir string) tea.Cmd {
	eventCh := make(chan session.SessionEvent, 64)
	sessionEventChan = eventCh

	go func() {
		ctx := context.Background()
		sess, err := sdk.NewClaudeSession(ctx, model, prompt, dir)
		if err != nil {
			close(eventCh)
			return
		}
		result, err := sess.RunWithEvents(ctx, eventCh)
		// Send a final synthetic event with the cost summary
		if result != nil {
			summary := fmt.Sprintf("[%d turns, $%.4f, %d input + %d output tokens]",
				result.NumTurns, result.CostUSD, result.TokensIn, result.TokensOut)
			eventCh <- session.SessionEvent{
				Type: "status_change",
				Data: summary,
			}
		}
		if err != nil {
			eventCh <- session.SessionEvent{
				Type: "error",
				Data: fmt.Sprintf(`{"error":"%s"}`, err.Error()),
			}
		}
		close(eventCh)
	}()

	return listenForSessionEvent(eventCh)
}
