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
	"sync/atomic"
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
	command     string
	serviceTier string
	stderr      io.Writer
	// lifecycleTimeout bounds each lifecycle RPC (thread/start, turn/start) on its own.
	lifecycleTimeout time.Duration

	mu          sync.Mutex
	server      *appServer
	catalogPath string
	versionOnce sync.Once
	closed      bool
	sessions    sessions
	vitals      healthState
	// current mirrors server for readers that must not wait on mu, such as health.
	current atomic.Pointer[appServer]
}

// Service tiers decide how fast OpenAI serves a turn.
const (
	// ServiceTierPriority is Codex's "Fast" tier. Measured against the standard tier on gpt-6.2-sol: about 55
	// tokens a second against 34, so a long answer arrives in roughly half the time. It spends the plan's
	// allowance faster, which is why it can be turned off.
	ServiceTierPriority = "priority"
	// ServiceTierStandard leaves Codex's own default in place: the proxy then asks for no tier at all.
	ServiceTierStandard = "standard"
)

// CodexOptions configures a Codex backend. The zero value is the default setup: codex-lean when it is installed,
// the Fast service tier, and no stderr.
type CodexOptions struct {
	// Command is the Codex executable. Empty prefers codex-lean when available and falls back to codex.
	Command string
	// ServiceTier is ServiceTierPriority (the default, also when empty) or ServiceTierStandard.
	ServiceTier string
	Stderr      io.Writer
	Version     string
}

// NewCodexBackend does not start app-server: Start does, and so does the first turn when Start was not called or
// failed.
func NewCodexBackend(opts CodexOptions) *CodexBackend {
	command := opts.Command
	if command == "" {
		command = "codex"
		if _, err := exec.LookPath("codex-lean"); err == nil {
			command = "codex-lean"
		}
	}
	return &CodexBackend{
		command: command, stderr: opts.Stderr, serviceTier: opts.ServiceTier, lifecycleTimeout: 15 * time.Second,
		vitals:   healthState{version: opts.Version, startedAt: time.Now().UTC()},
		sessions: sessions{pauseTimeout: 10 * time.Minute, idleTimeout: 30 * time.Minute},
	}
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
			b.current.Store(nil)
		default:
			return b.server, nil
		}
	}
	began := time.Now()
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
	mcpArgs, err := disabledMCPArgs(ctx, b.command)
	if err != nil {
		return nil, err
	}
	args := []string{"app-server", "--enable", "omit_skill_descriptions", "-c", "model_catalog_json=" + strconv.Quote(catalog)}
	args = append(args, mcpArgs...)
	for _, feature := range disabledCodexFeatures {
		args = append(args, "--disable", feature)
	}
	if b.serviceTier != ServiceTierStandard {
		args = append(args, "-c", `service_tier="`+ServiceTierPriority+`"`)
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
	b.current.Store(p)
	// Logged here, where Codex is really started, so the time is right whoever asked first: Start, the usage
	// poller or a request.
	if b.stderr != nil {
		_, _ = fmt.Fprintf(b.stderr, "model proxy: Codex started in %dms\n", time.Since(began).Milliseconds())
	}
	return p, nil
}

// Start brings app-server up ahead of the first request. A failure is said on stderr and is not fatal to the
// proxy: the next request starts Codex again.
func (b *CodexBackend) Start(ctx context.Context) error {
	_, err := b.process(ctx)
	if err != nil && ctx.Err() == nil && b.stderr != nil {
		_, _ = fmt.Fprintf(b.stderr, "model proxy: Codex did not start, the first request will try again: %v\n", err)
	}
	return err
}

func (b *CodexBackend) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.sessions.close()
	if b.server != nil {
		b.server.close()
		b.server = nil
		b.current.Store(nil)
	}
	if b.catalogPath != "" {
		_ = os.Remove(b.catalogPath)
		b.catalogPath = ""
	}
}

func (b *CodexBackend) logMessage(outcome string, usage Usage, firstByte time.Duration) {
	if b.stderr == nil {
		return
	}
	if usage.estimated {
		_, _ = fmt.Fprintf(b.stderr, "model proxy: session=%s usage=estimated estimated_input_tokens=%d estimated_cached_input_tokens=%d ttfb_ms=%d\n",
			outcome, usage.InputTokens, usage.CacheReadInputTokens, firstByte.Milliseconds())
		return
	}
	_, _ = fmt.Fprintf(b.stderr, "model proxy: session=%s input_tokens=%d cached_input_tokens=%d ttfb_ms=%d\n",
		outcome, usage.InputTokens, usage.CacheReadInputTokens, firstByte.Milliseconds())
}

// Run runs one turn. When Codex exits before anything reached the client, the turn is retried once on a fresh
// process: each request is self-contained, so a retry repeats nothing the client has seen.
// Run answers one turn and records how it went for GET /health. A turn the client abandoned is left out: the
// proxy is not unwell because Claude Code was interrupted.
func (b *CodexBackend) Run(ctx context.Context, req TurnRequest, emit func(Event) error) (Usage, error) {
	usage, err := b.run(ctx, req, emit)
	b.vitals.observe(ctx, &b.vitals.requests, err)
	return usage, err
}

// health reports the proxy's own state without starting Codex or calling it. It reads the app-server through
// current, never b.mu, because process() holds that lock across a Codex start of several seconds.
func (b *CodexBackend) health() Health {
	running := false
	if p := b.current.Load(); p != nil {
		select {
		case <-p.done:
		default:
			running = true
		}
	}
	return b.vitals.snapshot(b.command, running)
}

func (b *CodexBackend) run(ctx context.Context, req TurnRequest, emit func(Event) error) (Usage, error) {
	if req.session != nil {
		return b.runSession(ctx, req, emit)
	}
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
	threadID, err := p.startThread(ctx, b.lifecycleTimeout, req)
	if err != nil {
		return usage, err
	}
	events := p.subscribe(threadID)
	defer p.unsubscribe(threadID)

	turnID, err := p.startTurn(ctx, b.lifecycleTimeout, threadID, req.History, req.Input, req.Effort)
	if err != nil {
		return usage, err
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
		return p.interrupt(threadID, turnID)
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
				last := event.TokenUsage.Last
				usage = Usage{
					InputTokens:          max(0, last.InputTokens-last.CachedInputTokens),
					OutputTokens:         event.TokenUsage.Total.OutputTokens,
					CacheReadInputTokens: last.CachedInputTokens,
				}
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

// lifecycleContext bounds one lifecycle RPC from the moment it is made. It stays alive briefly after cancellation
// so a turn started just before the client disconnected can still be interrupted by ID.
func lifecycleContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (p *appServer) startThread(ctx context.Context, timeout time.Duration, req TurnRequest) (string, error) {
	startCtx, cancel := lifecycleContext(ctx, timeout)
	defer cancel()
	var result struct {
		Thread struct{ ID string }
	}
	if err := p.call(startCtx, "thread/start", map[string]any{
		"ephemeral": true, "cwd": p.cwd, "sandbox": "read-only", "approvalPolicy": "never",
		"model": req.Model, "baseInstructions": baseInstructions(req.System), "dynamicTools": req.Tools,
	}, &result); err != nil {
		return "", err
	}
	if result.Thread.ID == "" {
		return "", errors.New("thread/start returned no thread ID")
	}
	return result.Thread.ID, nil
}

func (p *appServer) startTurn(ctx context.Context, timeout time.Duration, threadID string, history []any, input []textPart, effort string) (string, error) {
	// Injecting a long history can take longer than a lifecycle timeout, so it runs on the request's own context.
	if len(history) > 0 {
		if err := p.call(ctx, "thread/inject_items", map[string]any{"threadId": threadID, "items": history}, nil); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if input == nil {
		input = []textPart{}
	}
	params := map[string]any{"threadId": threadID, "input": input}
	if effort != "" {
		params["effort"] = effort
	}
	var result struct {
		Turn struct{ ID string }
	}
	startCtx, cancel := lifecycleContext(ctx, timeout)
	defer cancel()
	if err := p.call(startCtx, "turn/start", params, &result); err != nil {
		return "", err
	}
	if result.Turn.ID == "" {
		return "", errors.New("turn/start returned no turn ID")
	}
	return result.Turn.ID, nil
}

func (p *appServer) interrupt(threadID, turnID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.call(ctx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, nil)
}

func (p *appServer) unsubscribe(threadID string) {
	p.mu.Lock()
	delete(p.threads, threadID)
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Unsubscribe is best effort for ephemeral threads, including after exit.
	_ = p.call(ctx, "thread/unsubscribe", map[string]string{"threadId": threadID}, nil)
}

type codexEvent struct {
	TurnID, Delta, CallID, Tool string
	Arguments                   json.RawMessage
	TokenUsage                  struct {
		Total struct{ OutputTokens int }
		Last  struct{ InputTokens, OutputTokens, CachedInputTokens int }
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
