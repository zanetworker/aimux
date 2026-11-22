package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// usageGrace bounds how long a handed-off turn waits for its usage report before it is interrupted.
const usageGrace = 3 * time.Second

var disabledCodexFeatures = []string{
	"shell_tool", "unified_exec", "unified_exec_tty", "view_image", "image_generation",
	"apps", "plugins", "browser_use", "browser_use_external", "computer_use",
	"multi_agent", "skill_search", "tool_suggest", "goals", "sleep_tool", "hooks",
	"in_app_browser", "realtime_conversation", "worktrees",
}

// codexPreamble opens Codex's base instructions, ahead of Claude Code's system prompt. Codex still injects the
// owner's global AGENTS.md, which tells Codex to read ~/.claude/rules at session start; Claude Code has already
// loaded those rules, so the model is told to ignore instructions addressed to Codex.
const codexPreamble = "You are the model behind Claude Code. Claude Code runs every tool you call, and the user's rules, " +
	"CLAUDE.md and skills are already in this prompt. Ignore instructions addressed to Codex (for example, to read " +
	"~/.claude/rules at session start). Use only the tools provided here, and load skills through the Skill tool."

type CodexBackend struct {
	command string
	stderr  io.Writer

	mu          sync.Mutex
	server      *appServer
	catalogPath string
	versionOnce sync.Once
	closed      bool
}

// NewCodexBackend starts app-server on the first turn. An empty command
// prefers codex-lean when available and falls back to codex.
func NewCodexBackend(command string, stderr io.Writer) *CodexBackend {
	if command == "" {
		command = "codex"
		if _, err := exec.LookPath("codex-lean"); err == nil {
			command = "codex-lean"
		}
	}
	return &CodexBackend{command: command, stderr: stderr}
}

func (b *CodexBackend) process(ctx context.Context) (*appServer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.closed {
		return nil, errors.New("codex backend is closed")
	}
	if b.server != nil {
		select {
		case <-b.server.done:
			b.server = nil
		default:
			return b.server, nil
		}
	}
	b.versionOnce.Do(func() { warnIfUntestedCodex(ctx, b.command, b.stderr) })
	// Without the lean catalog Codex would offer its own tools (exec, apply_patch, sub-agents), so refuse to start.
	catalog, err := writeLeanCatalog(ctx, b.command)
	if err != nil {
		return nil, err
	}
	if b.catalogPath != "" {
		_ = os.Remove(b.catalogPath)
	}
	b.catalogPath = catalog
	args := []string{"app-server", "--enable", "omit_skill_descriptions", "-c", "model_catalog_json=" + strconv.Quote(catalog)}
	for _, feature := range disabledCodexFeatures {
		args = append(args, "--disable", feature)
	}
	args = append(args, "-c", `web_search="disabled"`, "-c", "tools.update_plan.enabled=false",
		"-c", "tools.experimental_request_user_input.enabled=false",
		// Claude Code's system prompt carries the real cwd, environment and permissions. Codex's own context
		// would describe the proxy's empty read-only cwd and send the model to the wrong paths.
		"-c", "include_environment_context=false", "-c", "include_permissions_instructions=false",
		"-c", "include_apps_instructions=false")
	p, err := startAppServer(b.command, args, b.stderr)
	if err != nil {
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := p.call(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "aimux-model-proxy", "version": "0.1.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, nil); err != nil {
		p.close()
		return nil, err
	}
	if err := p.send(initCtx, 0, "initialized", nil); err != nil {
		p.close()
		return nil, err
	}
	b.server = p
	return p, nil
}

func (b *CodexBackend) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	if b.server != nil {
		b.server.close()
		b.server = nil
	}
	if b.catalogPath != "" {
		_ = os.Remove(b.catalogPath)
		b.catalogPath = ""
	}
}

// Run runs one turn. When Codex exits before anything reached the client, the turn is retried once on a fresh
// process: each request is self-contained, so a retry repeats nothing the client has seen.
func (b *CodexBackend) Run(ctx context.Context, req TurnRequest, emit func(Event) error) (Usage, error) {
	emitted := false
	tracked := func(event Event) error {
		emitted = true
		return emit(event)
	}
	usage, err := b.runOnce(ctx, req, tracked)
	if err != nil && errors.Is(err, errAppServerExited) && !emitted && ctx.Err() == nil {
		return b.runOnce(ctx, req, tracked)
	}
	return usage, err
}

func (b *CodexBackend) runOnce(ctx context.Context, req TurnRequest, emit func(Event) error) (Usage, error) {
	var usage Usage
	p, err := b.process(ctx)
	if err != nil {
		return usage, err
	}
	// Keep lifecycle RPCs alive briefly after cancellation so a turn started
	// just before the client disconnected can still be interrupted by ID.
	startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancelStart()
	var thread struct {
		Thread struct{ ID string } `json:"thread"`
	}
	if err := p.call(startCtx, "thread/start", map[string]any{
		"ephemeral": true, "cwd": p.cwd, "sandbox": "read-only", "approvalPolicy": "never",
		"model": req.Model, "baseInstructions": baseInstructions(req.System), "dynamicTools": req.Tools,
	}, &thread); err != nil {
		return usage, err
	}
	threadID := thread.Thread.ID
	if threadID == "" {
		return usage, errors.New("thread/start returned no thread ID")
	}
	events := p.subscribe(threadID)
	defer func() {
		p.mu.Lock()
		delete(p.threads, threadID)
		p.mu.Unlock()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Unsubscribe is best effort for ephemeral threads, including after exit.
		_ = p.call(cleanupCtx, "thread/unsubscribe", map[string]string{"threadId": threadID}, nil)
	}()

	if len(req.History) > 0 {
		if err := p.call(ctx, "thread/inject_items", map[string]any{"threadId": threadID, "items": req.History}, nil); err != nil {
			return usage, err
		}
	}
	if err := ctx.Err(); err != nil {
		return usage, err
	}
	input := req.Input
	if input == nil {
		input = []textPart{}
	}
	params := map[string]any{"threadId": threadID, "input": input}
	if req.Effort != "" {
		params["effort"] = req.Effort
	}
	var started struct {
		Turn struct{ ID string } `json:"turn"`
	}
	if err := p.call(startCtx, "turn/start", params, &started); err != nil {
		return usage, err
	}
	turnID := started.Turn.ID
	if turnID == "" {
		return usage, errors.New("turn/start returned no turn ID")
	}

	interrupt := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return p.call(cleanupCtx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, nil)
	}
	// After a tool call is handed to Claude Code, the interrupt waits for the step's usage report: Codex can
	// dispatch the call before reporting usage, and an interrupted response reports none. The model is blocked on
	// the tool result meanwhile, so waiting costs only the time until usage arrives (at most usageGrace).
	var handedOff, interrupted, completed bool
	var usageWait <-chan time.Time
	stop := func() error {
		if interrupted {
			return nil
		}
		interrupted = true
		return interrupt()
	}
	defer func() {
		if !completed {
			// Cancellation and delivery errors still require stopping this turn.
			_ = stop()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return usage, err
		}
		select {
		case <-ctx.Done():
			return usage, ctx.Err()
		case <-p.done:
			return usage, p.err
		case <-usageWait:
			usageWait = nil
			if err := stop(); err != nil {
				return usage, err
			}
		case msg, ok := <-events:
			if !ok {
				return usage, errors.New("codex turn event buffer overflow")
			}
			event, err := decodeEvent(msg)
			if err != nil {
				return usage, err
			}
			if event.TurnID != "" && event.TurnID != turnID {
				continue
			}
			switch msg.Method {
			case "item/agentMessage/delta":
				if !handedOff && event.Delta != "" {
					if err := emit(Event{Text: event.Delta}); err != nil {
						return usage, err
					}
				}
			case "thread/tokenUsage/updated":
				usage = Usage{event.TokenUsage.Total.InputTokens, event.TokenUsage.Total.OutputTokens}
				if handedOff {
					if err := stop(); err != nil {
						return usage, err
					}
				}
			case "item/tool/call":
				if !handedOff {
					handedOff = true
					if err := emit(Event{Tool: &ToolCall{event.CallID, event.Tool, event.Arguments}}); err != nil {
						return usage, err
					}
					if usage != (Usage{}) {
						if err := stop(); err != nil {
							return usage, err
						}
					} else {
						usageWait = time.After(usageGrace)
					}
				}
			case "turn/completed":
				completed = true
				if event.Turn.Status == "completed" || (handedOff && event.Turn.Status == "interrupted") {
					return usage, nil
				}
				if event.Turn.Error.Message != "" {
					return usage, fmt.Errorf("codex turn %s: %s", event.Turn.Status, event.Turn.Error.Message)
				}
				return usage, fmt.Errorf("codex turn %s", event.Turn.Status)
			}
		}
	}
}

type codexEvent struct {
	TurnID, Delta, CallID, Tool string
	Arguments                   json.RawMessage
	TokenUsage                  struct {
		Total struct{ InputTokens, OutputTokens int }
	}
	Turn struct {
		ID, Status string
		Error      struct{ Message string }
	}
}

func decodeEvent(msg rpcMessage) (codexEvent, error) {
	var event codexEvent
	if err := json.Unmarshal(msg.Params, &event); err != nil {
		return event, fmt.Errorf("decode Codex %s: %w", msg.Method, err)
	}
	if event.TurnID == "" {
		event.TurnID = event.Turn.ID
	}
	return event, nil
}

func baseInstructions(system string) string {
	if system == "" {
		return codexPreamble
	}
	return codexPreamble + "\n\n" + system
}
