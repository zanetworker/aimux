package sdk

import (
	"context"
	"fmt"
	"strings"

	"github.com/zanetworker/aimux/internal/environment"
)

// SDKSession orchestrates the full event loop between aimux and an SDK subprocess.
// It starts the subprocess, sends prompts, dispatches tool calls to the environment,
// and collects text output until the subprocess signals completion.
type SDKSession struct {
	sub        *SubprocessManager
	dispatcher *Dispatcher
	output     []string
}

// NewSDKSession creates and starts an SDK session with the given binary and environment.
func NewSDKSession(ctx context.Context, binary string, env environment.Environment, sandbox string) (*SDKSession, error) {
	sub, err := StartSubprocess(ctx, binary)
	if err != nil {
		return nil, fmt.Errorf("sdk session: start subprocess: %w", err)
	}
	return &SDKSession{
		sub:        sub,
		dispatcher: NewDispatcher(env, sandbox),
	}, nil
}

// Run sends a prompt to the subprocess and processes events until completion.
// It returns the collected text output or an error.
func (s *SDKSession) Run(ctx context.Context, prompt string) (string, error) {
	if err := s.sub.Send(Event{Type: EventPrompt, Content: prompt}); err != nil {
		return "", fmt.Errorf("sdk session: send prompt: %w", err)
	}

	for {
		event, err := s.sub.Receive()
		if err != nil {
			return s.collectedOutput(), fmt.Errorf("sdk session: receive: %w", err)
		}

		switch event.Type {
		case EventToolCall:
			tc := event.ToToolCall()
			result, decision := s.dispatcher.Dispatch(ctx, tc)
			if decision == Handled {
				if err := s.sub.Send(EventFromToolResult(result)); err != nil {
					return s.collectedOutput(), fmt.Errorf("sdk session: send tool result: %w", err)
				}
			}

		case EventText:
			s.output = append(s.output, event.Content)

		case EventCompletion:
			return s.collectedOutput(), nil

		case EventError:
			return s.collectedOutput(), fmt.Errorf("sdk session: subprocess error: %s", event.Message)
		}
	}
}

// Stop gracefully shuts down the subprocess.
func (s *SDKSession) Stop() error {
	return s.sub.Stop()
}

func (s *SDKSession) collectedOutput() string {
	return strings.Join(s.output, "")
}
