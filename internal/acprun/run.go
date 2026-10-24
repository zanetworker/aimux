// Package acprun continues an existing agent session through an ACP adapter
// (claude-agent-acp, codex-acp): it starts the adapter, loads the session,
// sends prompts, streams updates as numbered events and relays permission
// requests. The session's own files stay the source of truth, so the agent
// CLI on the laptop continues the same conversation afterwards.
package acprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	acp "github.com/coder/acp-go-sdk"
)

// Event kinds.
const (
	KindUpdate     = "update"     // Data: one ACP session update (agent text, tool call, plan, ...)
	KindPermission = "permission" // Data: the ACP permission request sent to the approver
)

// maxRunEvents bounds the history a run keeps for Events; a long-lived run
// would otherwise grow without limit. Callers that need everything use the
// Prompt callback.
const maxRunEvents = 1000

// Event is one thing that happened during a run, numbered so a client that
// reconnects can ask for everything after the last number it saw.
type Event struct {
	Seq  int64           `json:"seq"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// Spec is how to launch an ACP adapter.
type Spec struct {
	Command string
	Args    []string
	Cwd     string   // the session's working directory
	Env     []string // extra KEY=VALUE pairs on top of aimux's environment
}

// Approver decides a permission request. It returns the chosen option ID,
// or ok=false to deny. It should return when ctx is done.
type Approver func(ctx context.Context, req acp.RequestPermissionRequest) (optionID string, ok bool)

// Run is one adapter process driving one loaded session.
type Run struct {
	sessionID acp.SessionId
	approve   Approver
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	conn      *acp.ClientSideConnection

	promptMu sync.Mutex // one prompt at a time

	mu      sync.Mutex
	events  []Event
	seq     int64
	loading bool
	onEvent func(Event)
	closed  bool
	// prompting is true while a prompt is in flight; cancelPending records a
	// Cancel that arrived before the prompt was sent, which then ends it.
	prompting     bool
	cancelPending bool
}

// Start launches the adapter, initializes it and loads sessionID. Updates the
// agent replays while loading are history and are not reported as events.
func Start(ctx context.Context, spec Spec, sessionID string, approve Approver) (*Run, error) {
	r := &Run{sessionID: acp.SessionId(sessionID), approve: approve}
	cmd := exec.Command(spec.Command, spec.Args...) // #nosec G204 -- adapter command comes from aimux config
	cmd.Dir = spec.Cwd
	cmd.Env = adapterEnv(os.Environ(), spec.Env)
	// Own session (and so own process group): Close also stops what a
	// wrapper like npx started, and the agent has no controlling terminal,
	// so it cannot read keystrokes meant for aimux's approval prompt.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("start ACP adapter: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("start ACP adapter: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ACP adapter %s: %w", spec.Command, err)
	}
	r.cmd, r.stdin = cmd, stdin
	r.conn = acp.NewClientSideConnection(client{r}, stdin, stdout)
	// The SDK logs connection events to slog.Default, which would land in the
	// user's terminal output.
	r.conn.SetLogger(slog.New(slog.DiscardHandler))

	init, err := r.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		// No fs or terminal capabilities: the agent must not reach into
		// files or shells through aimux.
		ClientCapabilities: acp.ClientCapabilities{},
		ClientInfo:         &acp.Implementation{Name: "aimux", Version: "1"},
	})
	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("initialize ACP adapter %s: %w", spec.Command, err)
	}
	if !init.AgentCapabilities.LoadSession {
		_ = r.Close()
		return nil, fmt.Errorf("ACP adapter %s cannot load existing sessions", spec.Command)
	}

	r.setLoading(true)
	_, err = r.conn.LoadSession(ctx, acp.LoadSessionRequest{
		SessionId: r.sessionID, Cwd: spec.Cwd, McpServers: []acp.McpServer{},
	})
	r.setLoading(false)
	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("load session %s in %s: %w", sessionID, spec.Cwd, err)
	}
	return r, nil
}

func (r *Run) setLoading(v bool) {
	r.mu.Lock()
	r.loading = v
	r.mu.Unlock()
}

// Prompt sends text and blocks until the agent ends its turn, calling onEvent
// for each event of this turn. It returns the ACP stop reason.
func (r *Run) Prompt(ctx context.Context, text string, onEvent func(Event)) (string, error) {
	r.promptMu.Lock()
	defer r.promptMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", errors.New("run is closed")
	}
	if r.cancelPending {
		r.cancelPending = false
		r.mu.Unlock()
		return string(acp.StopReasonCancelled), nil
	}
	r.onEvent = onEvent
	r.prompting = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.onEvent = nil
		r.prompting = false
		r.cancelPending = false
		r.mu.Unlock()
	}()

	resp, err := r.conn.Prompt(ctx, acp.PromptRequest{
		SessionId: r.sessionID,
		Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
	})
	if err != nil {
		return "", fmt.Errorf("prompt session %s: %w", r.sessionID, err)
	}
	return string(resp.StopReason), nil
}

// Cancel asks the agent to stop the current turn; Prompt then returns
// "cancelled". A Cancel before the prompt is sent ends that prompt at once.
func (r *Run) Cancel() {
	r.mu.Lock()
	if !r.prompting {
		r.cancelPending = true
	}
	r.mu.Unlock()
	_ = r.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: r.sessionID})
}

// Events returns the events numbered after seq, oldest first.
func (r *Run) Events(after int64) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out
}

// PID is the adapter's process ID.
func (r *Run) PID() int { return r.cmd.Process.Pid }

// Close stops the adapter and everything it started. It is safe to call twice.
func (r *Run) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	_ = r.stdin.Close()
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL) // the group may already be gone
	_ = r.cmd.Wait()                                      // reap; the exit status is expected to be a kill
	return nil
}

func (r *Run) emit(kind string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(map[string]string{"error": err.Error()})
	}
	r.mu.Lock()
	if r.loading {
		r.mu.Unlock()
		return
	}
	r.seq++
	e := Event{Seq: r.seq, Kind: kind, Data: data}
	r.events = append(r.events, e)
	if len(r.events) > maxRunEvents {
		r.events = r.events[len(r.events)-maxRunEvents:]
	}
	cb := r.onEvent
	r.mu.Unlock()
	if cb != nil {
		cb(e)
	}
}

// client answers the agent's requests to aimux.
type client struct{ r *Run }

func (c client) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	c.r.emit(KindUpdate, n.Update)
	return nil
}

func (c client) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.r.emit(KindPermission, req)
	if c.r.approve != nil {
		if id, ok := c.r.approve(ctx, req); ok {
			for _, o := range req.Options {
				if string(o.OptionId) == id {
					return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(o.OptionId)}, nil
				}
			}
			// An option the agent never offered is treated as a denial.
		}
	}
	return acp.RequestPermissionResponse{Outcome: denyOutcome(req.Options)}, nil
}

// denyOutcome picks the agent's own reject option, so the agent records a
// clear refusal; without one, the request is cancelled.
func denyOutcome(opts []acp.PermissionOption) acp.RequestPermissionOutcome {
	for _, kind := range []acp.PermissionOptionKind{acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways} {
		for _, o := range opts {
			if o.Kind == kind {
				return acp.NewRequestPermissionOutcomeSelected(o.OptionId)
			}
		}
	}
	return acp.NewRequestPermissionOutcomeCancelled()
}

func (c client) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errNotOffered
}

func (c client) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errNotOffered
}

// errNotOffered answers requests for capabilities aimux does not advertise.
var errNotOffered = errors.New("aimux does not offer this capability to agents")

func (c client) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errNotOffered
}

func (c client) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errNotOffered
}

func (c client) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errNotOffered
}

func (c client) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errNotOffered
}

func (c client) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errNotOffered
}

func (c client) UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error {
	return nil // nothing was elicited
}

func (c client) UnstableCreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	return acp.UnstableCreateElicitationResponse{}, errNotOffered
}

func (c client) UnstableConnectMcp(context.Context, acp.UnstableConnectMcpRequest) (acp.UnstableConnectMcpResponse, error) {
	return acp.UnstableConnectMcpResponse{}, errNotOffered
}

func (c client) UnstableDisconnectMcp(context.Context, acp.UnstableDisconnectMcpRequest) (acp.UnstableDisconnectMcpResponse, error) {
	return acp.UnstableDisconnectMcpResponse{}, errNotOffered
}

// adapterEnv is the adapter's environment: aimux's own plus extra, without
// CLAUDECODE, which makes Claude Code refuse to start inside another session.
func adapterEnv(base, extra []string) []string {
	env := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		if !strings.HasPrefix(kv, "CLAUDECODE=") {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}
