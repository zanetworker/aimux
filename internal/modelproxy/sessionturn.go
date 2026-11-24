package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (b *CodexBackend) runSessionTurn(ctx context.Context, entry *sessionEntry, req TurnRequest, emit func(Event) error) (Usage, bool, error) {
	p, err := b.process(ctx)
	if err != nil {
		return Usage{}, false, err
	}
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	history := req.History
	thread := entry.thread
	var continuation *toolContinuation
	reused := false
	if thread != nil {
		req.session.outcome, continuation = thread.match(p, req)
		switch req.session.outcome {
		case "hit_idle":
			history = history[req.session.offsets[len(thread.prefixes)-1]:]
			reused = true
		case "hit_continuation":
			reused = true
		default:
			thread.close()
			entry.thread = nil
		}
	}
	if entry.thread == nil {
		id, err := p.startThread(startCtx, req)
		if err != nil {
			return Usage{}, false, err
		}
		thread = &sessionThread{server: p, id: id, model: req.Model, events: p.subscribe(id)}
		entry.thread = thread
	}
	if continuation != nil {
		err = thread.resume(ctx, continuation)
	} else {
		thread.turnID, err = p.startTurn(ctx, startCtx, thread.id, history, req.Input, req.Effort)
	}
	if err != nil {
		thread.close()
		entry.thread = nil
		return Usage{}, reused, err
	}
	usage, err := thread.readTurn(ctx, req, emit)
	if err != nil {
		thread.close()
		entry.thread = nil
	}
	// An upstream tool wait can end while Claude Code is running the tool. If no
	// output reached the client, a fresh thread can replay the supplied results.
	return usage, continuation != nil, err
}

const parallelToolWindow = 300 * time.Millisecond

type pendingTool struct {
	call ToolCall
	id   json.RawMessage
}

type toolContent struct {
	Type     string  `json:"type"`
	Text     *string `json:"text,omitempty"`
	ImageURL string  `json:"imageUrl,omitempty"`
}

type toolAnswer struct {
	Content []toolContent `json:"contentItems"`
	Success bool          `json:"success"`
}

type toolContinuation struct {
	answers map[string]toolAnswer
	steer   []textPart
}

func matchToolResults(pending []pendingTool, blocks []requestBlock) *toolContinuation {
	continuation := &toolContinuation{answers: make(map[string]toolAnswer, len(pending))}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			continuation.steer = append(continuation.steer, textPart{Type: "text", Text: block.Text})
		case "image":
			continuation.steer = append(continuation.steer, textPart{Type: "image", ImageURL: block.imageURL})
		case "tool_result":
			if _, exists := continuation.answers[block.ToolUseID]; exists {
				return nil
			}
			content, err := decodeContent(block.Content)
			if err != nil {
				return nil
			}
			answer := toolAnswer{Content: make([]toolContent, 0, len(content)), Success: !block.IsError}
			markError := block.IsError
			for _, part := range content {
				switch part.Type {
				case "text":
					if markError {
						part.Text = toolErrorPrefix + part.Text
						markError = false
					}
					answer.Content = append(answer.Content, toolContent{Type: "inputText", Text: &part.Text})
				case "image":
					answer.Content = append(answer.Content, toolContent{Type: "inputImage", ImageURL: part.imageURL})
				}
			}
			if markError {
				text := toolErrorPrefix
				answer.Content = append([]toolContent{{Type: "inputText", Text: &text}}, answer.Content...)
			}
			continuation.answers[block.ToolUseID] = answer
		default:
			return nil
		}
	}
	if len(continuation.answers) != len(pending) {
		return nil
	}
	for _, call := range pending {
		if _, ok := continuation.answers[call.call.ID]; !ok {
			return nil
		}
	}
	return continuation
}

func (t *sessionThread) resume(ctx context.Context, continuation *toolContinuation) error {
	// Steer while Codex is still waiting, before the last answer lets it finish the turn.
	if len(continuation.steer) != 0 {
		err := t.server.call(ctx, "turn/steer", map[string]any{
			"threadId": t.id, "expectedTurnId": t.turnID, "input": continuation.steer,
		}, nil)
		if err != nil {
			return err
		}
	}
	for _, call := range t.pending {
		if err := t.server.respond(ctx, call.id, continuation.answers[call.call.ID]); err != nil {
			return err
		}
	}
	t.pending = nil
	return nil
}

func (t *sessionThread) readTurn(ctx context.Context, req TurnRequest, emit func(Event) error) (Usage, error) {
	var usage Usage
	var text strings.Builder
	var collectDone <-chan time.Time
	var windowClosed, completed, observedUsage bool
	var cutoff time.Time
	var buffered int
	var pending []pendingTool
	queued := t.deferred
	t.deferred = nil

	for !completed && (!windowClosed || buffered > 0) {
		if err := ctx.Err(); err != nil {
			return usage, err
		}
		var msg rpcMessage
		if len(queued) != 0 {
			msg, queued = queued[0], queued[1:]
		} else {
			select {
			case <-ctx.Done():
				return usage, ctx.Err()
			case <-t.server.done:
				return usage, t.server.err
			case <-collectDone:
				collectDone, windowClosed = nil, true
				// Keep calls that arrived on time even if the HTTP writer delayed reading them.
				buffered = len(t.events)
				continue
			case received, ok := <-t.events:
				if !ok {
					return usage, errors.New("codex turn event buffer overflow")
				}
				msg = received
				if buffered > 0 {
					buffered--
				}
			}
		}
		event, err := decodeEvent(msg)
		if err != nil {
			return usage, err
		}
		if event.TurnID != t.turnID {
			continue
		}
		switch msg.Method {
		case "item/agentMessage/delta":
			if event.Delta != "" {
				text.WriteString(event.Delta)
				if err := emit(Event{Text: event.Delta}); err != nil {
					return usage, err
				}
			}
		case "thread/tokenUsage/updated":
			last := event.TokenUsage.Last
			observedUsage = true
			usage.InputTokens = max(0, last.InputTokens-last.CachedInputTokens)
			usage.CacheReadInputTokens = last.CachedInputTokens
			usage.OutputTokens += last.OutputTokens
			t.lastUncachedInputTokens = usage.InputTokens
			t.lastCachedInputTokens = usage.CacheReadInputTokens
		case "item/tool/call":
			if len(pending) == 0 {
				cutoff = msg.receivedAt.Add(parallelToolWindow)
				collect := time.NewTimer(time.Until(cutoff))
				defer collect.Stop()
				collectDone = collect.C
			}
			if msg.receivedAt.After(cutoff) {
				t.deferred = append(t.deferred, msg)
				continue
			}
			pending = append(pending, pendingTool{ToolCall{event.CallID, event.Tool, event.Arguments}, msg.ID})
		case "turn/completed":
			t.turnID = ""
			if event.Turn.Status != "completed" {
				return usage, fmt.Errorf("codex turn %s: %s", event.Turn.Status, event.Turn.Error.Message)
			}
			completed = true
		}
	}
	assistant := make([]responseBlock, 0, len(pending)+1)
	if text.Len() != 0 {
		value := text.String()
		assistant = append(assistant, responseBlock{Type: "text", Text: &value})
	}
	if !completed {
		// Codex can withhold usage for the held request until its tools are answered.
		if !observedUsage {
			usage.InputTokens = t.lastUncachedInputTokens
			usage.CacheReadInputTokens = t.lastCachedInputTokens
			usage.estimated = true
		}
		t.pending = pending
		for _, waiting := range pending {
			call := waiting.call
			name := call.Name
			if original, ok := req.ToolNames[name]; ok {
				name = original
			}
			assistant = append(assistant, responseBlock{Type: "tool_use", ID: call.ID, Name: name, Input: call.Input})
			if err := emit(Event{Tool: &call}); err != nil {
				return usage, err
			}
		}
	}
	content, err := json.Marshal(assistant)
	if err != nil {
		return usage, fmt.Errorf("encode Codex assistant history: %w", err)
	}
	prefixes := req.session.prefixes
	t.prefixes = append(prefixes, hashMessage(prefixes[len(prefixes)-1], conversationMessage{"assistant", content}))
	return usage, nil
}
