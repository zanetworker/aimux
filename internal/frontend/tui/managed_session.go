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
	events []session.SessionEvent
	err    error
}

func startManagedSession(model, prompt, dir string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		sess, err := sdk.NewClaudeSession(ctx, model, prompt, dir)
		if err != nil {
			return managedSessionDoneMsg{err: fmt.Errorf("create session: %w", err)}
		}
		result, err := sess.Run(ctx)
		return managedSessionDoneMsg{
			result: result,
			events: sess.Events(),
			err:    err,
		}
	}
}
