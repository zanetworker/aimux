package sdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/session"
)

// ClaudeResult holds the final output from a Claude CLI session.
type ClaudeResult struct {
	Output     string
	IsError    bool
	NumTurns   int
	CostUSD    float64
	TokensIn   int64
	TokensOut  int64
	StopReason string
	SessionID  string
}

// ClaudeSession manages a Claude CLI subprocess using --output-format stream-json.
type ClaudeSession struct {
	cmd    *exec.Cmd
	stdout *bufio.Scanner
	stderr bytes.Buffer
	done   chan error

	sessionID string
	events    []session.SessionEvent
	mu        sync.Mutex
}

// NewClaudeSession creates a ClaudeSession that will run claude -p with stream-json output.
// The process is started but Run must be called to consume the output.
func NewClaudeSession(ctx context.Context, model, prompt, dir string) (*ClaudeSession, error) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude session: claude binary not found: %w", err)
	}

	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, claudeBin, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude session: stdout pipe: %w", err)
	}

	cs := &ClaudeSession{
		cmd:    cmd,
		stdout: bufio.NewScanner(stdoutPipe),
		done:   make(chan error, 1),
	}
	cs.stdout.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	cmd.Stderr = &cs.stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude session: start: %w", err)
	}

	go func() {
		cs.done <- cmd.Wait()
	}()

	return cs, nil
}

// Run reads stream-json events from stdout until the result event or EOF.
func (cs *ClaudeSession) Run(ctx context.Context) (*ClaudeResult, error) {
	var result ClaudeResult

	for cs.stdout.Scan() {
		line := cs.stdout.Bytes()
		if len(line) == 0 {
			continue
		}

		var envelope StreamEvent
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}

		switch envelope.Type {
		case "system":
			cs.parseSystemEvent(line)

		case "assistant":
			cs.parseAssistantEvent(line, &result)

		case "result":
			cs.parseResultEvent(line, &result)
			return &result, nil
		}
	}

	if err := cs.stdout.Err(); err != nil {
		return &result, fmt.Errorf("claude session: read stdout: %w", err)
	}

	// stdout closed without a result event; wait for the process to exit.
	if err := <-cs.done; err != nil {
		stderrOut := cs.stderr.String()
		if stderrOut != "" {
			return &result, fmt.Errorf("claude session: process exited with error: %w (stderr: %s)", err, stderrOut)
		}
		return &result, fmt.Errorf("claude session: process exited with error: %w", err)
	}

	if result.Output == "" && len(cs.events) == 0 {
		return &result, fmt.Errorf("claude session: no output received")
	}
	return &result, nil
}

func (cs *ClaudeSession) parseSystemEvent(line []byte) {
	var sys struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(line, &sys); err == nil && sys.SessionID != "" {
		cs.mu.Lock()
		cs.sessionID = sys.SessionID
		cs.mu.Unlock()
	}
}

func (cs *ClaudeSession) parseAssistantEvent(line []byte, result *ClaudeResult) {
	var evt AssistantStreamEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return
	}

	if cs.sessionID == "" && evt.SessionID != "" {
		cs.mu.Lock()
		cs.sessionID = evt.SessionID
		cs.mu.Unlock()
	}

	result.TokensIn += int64(evt.Message.Usage.InputTokens)
	result.TokensOut += int64(evt.Message.Usage.OutputTokens)

	for _, block := range evt.Message.Content {
		switch block.Type {
		case "text":
			if result.Output != "" {
				result.Output += "\n"
			}
			result.Output += block.Text

			cs.addEvent(session.SessionEvent{
				Type: "text",
				Data: mustJSON(map[string]string{"text": block.Text}),
			})

		case "tool_use":
			cs.addEvent(session.SessionEvent{
				Type: "tool_call",
				Data: mustJSON(map[string]any{
					"id":    block.ID,
					"name":  block.Name,
					"input": block.Input,
				}),
			})

		case "tool_result":
			cs.addEvent(session.SessionEvent{
				Type: "tool_result",
				Data: mustJSON(map[string]any{
					"id":     block.ID,
					"output": block.Text,
				}),
			})
		}
	}
}

func (cs *ClaudeSession) parseResultEvent(line []byte, result *ClaudeResult) {
	var evt ResultStreamEvent
	if err := json.Unmarshal(line, &evt); err != nil {
		return
	}

	result.IsError = evt.IsError
	result.NumTurns = evt.NumTurns
	result.CostUSD = evt.TotalCostUSD
	result.StopReason = evt.StopReason
	result.SessionID = evt.SessionID

	if evt.Result != "" {
		result.Output = evt.Result
	}

	result.TokensIn += int64(evt.Usage.InputTokens)
	result.TokensOut += int64(evt.Usage.OutputTokens)
}

func (cs *ClaudeSession) addEvent(evt session.SessionEvent) {
	evt.Timestamp = time.Now()
	cs.mu.Lock()
	evt.SessionID = cs.sessionID
	cs.events = append(cs.events, evt)
	cs.mu.Unlock()
}

// Events returns a copy of all collected session events.
func (cs *ClaudeSession) Events() []session.SessionEvent {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([]session.SessionEvent, len(cs.events))
	copy(out, cs.events)
	return out
}

// SessionID returns the Claude session ID from the init event.
func (cs *ClaudeSession) SessionID() string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.sessionID
}

// Stop gracefully shuts down the Claude process.
func (cs *ClaudeSession) Stop() error {
	if cs.cmd.Process == nil {
		return nil
	}
	_ = cs.cmd.Process.Signal(syscall.SIGTERM)

	select {
	case err := <-cs.done:
		return ignoreSignalExit(err)
	case <-time.After(5 * time.Second):
		_ = cs.cmd.Process.Kill()
		return ignoreSignalExit(<-cs.done)
	}
}

// RunWithEvents reads stream-json events, sending each to eventCh as they arrive.
// The channel is closed when the session completes or errors.
func (cs *ClaudeSession) RunWithEvents(ctx context.Context, eventCh chan<- session.SessionEvent) (*ClaudeResult, error) {
	defer close(eventCh)

	var result ClaudeResult

	for cs.stdout.Scan() {
		line := cs.stdout.Bytes()
		if len(line) == 0 {
			continue
		}

		var envelope StreamEvent
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}

		prevLen := len(cs.events)

		switch envelope.Type {
		case "system":
			cs.parseSystemEvent(line)
		case "assistant":
			cs.parseAssistantEvent(line, &result)
		case "result":
			cs.parseResultEvent(line, &result)
			cs.mu.Lock()
			for i := prevLen; i < len(cs.events); i++ {
				eventCh <- cs.events[i]
			}
			cs.mu.Unlock()
			return &result, nil
		}

		cs.mu.Lock()
		for i := prevLen; i < len(cs.events); i++ {
			eventCh <- cs.events[i]
		}
		cs.mu.Unlock()
	}

	if err := cs.stdout.Err(); err != nil {
		return &result, fmt.Errorf("claude session: read stdout: %w", err)
	}

	if err := <-cs.done; err != nil {
		stderrOut := cs.stderr.String()
		if stderrOut != "" {
			return &result, fmt.Errorf("claude session: process exited with error: %w (stderr: %s)", err, stderrOut)
		}
		return &result, fmt.Errorf("claude session: process exited with error: %w", err)
	}

	if result.Output == "" && len(cs.events) == 0 {
		return &result, fmt.Errorf("claude session: no output received")
	}
	return &result, nil
}

// Stderr returns captured stderr output.
func (cs *ClaudeSession) Stderr() string {
	return cs.stderr.String()
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ReadEvents reads events from the given io.Reader containing stream-json lines.
// This is useful for testing with pre-recorded output.
func ReadEvents(r io.Reader) (*ClaudeResult, []session.SessionEvent, error) {
	cs := &ClaudeSession{
		stdout: bufio.NewScanner(r),
		done:   make(chan error, 1),
	}
	cs.stdout.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	go func() { cs.done <- nil }()

	var result ClaudeResult
	for cs.stdout.Scan() {
		line := cs.stdout.Bytes()
		if len(line) == 0 {
			continue
		}

		var envelope StreamEvent
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}

		switch envelope.Type {
		case "system":
			cs.parseSystemEvent(line)
		case "assistant":
			cs.parseAssistantEvent(line, &result)
		case "result":
			cs.parseResultEvent(line, &result)
		}
	}

	return &result, cs.Events(), cs.stdout.Err()
}
