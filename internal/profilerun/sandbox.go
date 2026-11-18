package profilerun

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/errcode"
	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// archiveTimeout bounds copying a transcript out after a turn.
const archiveTimeout = time.Minute

// SandboxEngine runs profile sessions with claude in print mode inside the
// profile's OpenShell sandbox. Every turn compiles the profile afresh,
// ensures the sandbox, issues the session a gateway token allowing exactly
// Compiled.Grant under Compiled.Snapshot (none when it has no servers), so
// the gateway knows which pins the turn was granted against, runs one claude process
// and revokes the token once that process is gone. The session id is fixed
// at New and is claude's own session id (--session-id on the first turn,
// --resume after), so the transcript archived after each turn carries the
// same name.
type SandboxEngine struct {
	L       *sandboxlaunch.Launcher
	Compile func(profile string) (profile.Compiled, error)
	Gateway func() (baseURL string, leases Leases, err error)
	// RenewEvery is how often a running turn renews its lease (0:
	// toolgateway.LeaseRenewEvery).
	RenewEvery time.Duration
	// ProbeEvery is how often a running turn checks the gateway worker's
	// status, to put its lease again soon after the worker restarted (0:
	// leaseProbeEvery).
	ProbeEvery time.Duration
	// ProbeRetryEvery is how soon the status is checked again after a
	// failed renewal or status check, while the worker is coming back (0:
	// leaseProbeRetry).
	ProbeRetryEvery time.Duration
	// Review, if set, is what the gateway will not serve a turn: a line the
	// turn shows before claude answers (the profile's tools held for
	// review; "" shows nothing) and the servers left out of its MCP config
	// because every tool granted on them is held back.
	Review func(c profile.Compiled) TurnReview
	// GatewayCheck, if set, checks the OpenShell gateway (openshell status)
	// after a sandbox failure: a failed check codes the failure
	// sandbox_gateway_down. It must not start the gateway.
	GatewayCheck func(ctx context.Context) error
	ArchiveDir   string // default ~/.aimux/archive (the search index scans it)
	// OnStarted, if set, hears once per session that its first turn's
	// claude process started, as its stream showed (the id is claude's from
	// then on); remote serve records it (Store.MarkStarted) so a restart
	// knows to resume. A failure is logged, never the turn's.
	OnStarted func(session string) error
	Log       *slog.Logger // archive failures; default slog.Default()

	mu       sync.Mutex
	sessions map[string]*sbSession
	ensureMu map[string]chan struct{} // by sandbox name: one Ensure at a time (a slot of 1)
	ensured  map[string]string        // sandbox -> digest last ensured
}

// leaseFailure codes a turn's failed lease: the worker not running, or
// running but not taking it. A cancelled turn stays uncoded.
func leaseFailure(err error) error {
	err = fmt.Errorf("tool gateway: %w", err)
	switch {
	case errors.Is(err, toolgateway.ErrGatewayNotRunning):
		return errcode.Wrap(errcode.ToolGatewayNotRunning, err)
	case errors.Is(err, context.Canceled):
		return err
	}
	return errcode.Wrap(errcode.GatewayBackendDown, err)
}

// gatewayCheckTimeout bounds the OpenShell gateway check after a failure.
const gatewayCheckTimeout = 5 * time.Second

// sandboxFailure codes err as sandbox_gateway_down when the OpenShell
// gateway check (GatewayCheck) fails now; any other failure, such as an MCP
// server refusing a connection inside the sandbox, stays as it is. Without
// a check nothing is coded.
func (e *SandboxEngine) sandboxFailure(ctx context.Context, err error) error {
	// A cancelled step is the caller's doing: no probe holding locks.
	if e.GatewayCheck == nil || ctx.Err() != nil {
		return err
	}
	check, cancel := context.WithTimeout(ctx, gatewayCheckTimeout)
	defer cancel()
	if e.GatewayCheck(check) != nil && ctx.Err() == nil {
		return errcode.Wrap(errcode.SandboxGatewayDown, err)
	}
	return err
}

// TurnReview is what the tool gateway will not serve one turn.
type TurnReview struct {
	Notice  string   // one line for the turn, "" for none
	LeftOut []string // granted servers left out of the turn's MCP config
}

type sbSession struct {
	profile string
	first   *AskRequest        // the page and its header, until a turn succeeds
	created bool               // a claude process has run under this id: resume it
	cancel  context.CancelFunc // the running turn's, nil between turns
	dropped bool               // adopted unstarted without its page: unusable
}

// NewSandboxEngine returns a sandbox engine. compile turns a profile name
// into its compiled form; gw returns the tool gateway's base URL (as seen
// from inside the sandbox) and the token table its listener checks.
func NewSandboxEngine(l *sandboxlaunch.Launcher, compile func(profile string) (profile.Compiled, error), gw func() (baseURL string, leases Leases, err error)) *SandboxEngine {
	home, _ := os.UserHomeDir()
	return &SandboxEngine{L: l, Compile: compile, Gateway: gw,
		ArchiveDir: filepath.Join(home, ".aimux", "archive"), Log: slog.Default()}
}

// New compiles m.Profile, makes sure its sandbox exists and returns a new
// session id. The page, title and URL are held for the first prompt.
func (e *SandboxEngine) New(ctx context.Context, m Meta, page string) (string, error) {
	c, err := e.Compile(m.Profile)
	if err != nil {
		return "", errcode.Wrap(errcode.ProfileUnavailable, fmt.Errorf("profile %s: %w", m.Profile, err))
	}
	if err := e.ensure(ctx, c); err != nil {
		return "", err
	}
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sessions == nil {
		e.sessions = map[string]*sbSession{}
	}
	e.sessions[id] = &sbSession{profile: m.Profile, first: &AskRequest{URL: m.URL, Title: m.Title, Page: page, OriginOnly: m.OriginOnly}}
	return id, nil
}

// ensure runs Ensure for c's sandbox at most once per policy digest, one call per
// sandbox at a time, so two first sessions of a profile never both create it.
func (e *SandboxEngine) ensure(ctx context.Context, c profile.Compiled) error {
	l := e.sandboxLock(c.Sandbox)
	select {
	case l <- struct{}{}:
	case <-ctx.Done(): // a waiter gives up with its caller
		return ctx.Err()
	}
	defer func() { <-l }()
	e.mu.Lock()
	done := e.ensured[c.Sandbox] == c.PolicyDigest
	e.mu.Unlock()
	if done {
		return nil
	}
	if err := e.L.Ensure(ctx, c); err != nil {
		return e.sandboxFailure(ctx, fmt.Errorf("sandbox %s: %w", c.Sandbox, err))
	}
	e.mu.Lock()
	e.ensured[c.Sandbox] = c.PolicyDigest
	e.mu.Unlock()
	return nil
}

// sandboxLock is the slot one Ensure of sandbox holds at a time.
func (e *SandboxEngine) sandboxLock(sandbox string) chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ensureMu == nil {
		e.ensureMu, e.ensured = map[string]chan struct{}{}, map[string]string{}
	}
	l := e.ensureMu[sandbox]
	if l == nil {
		l = make(chan struct{}, 1)
		e.ensureMu[sandbox] = l
	}
	return l
}

// forgetEnsured drops sandbox from the ensured cache, so the next turn runs
// Ensure again (the sandbox may have been deleted out of band).
func (e *SandboxEngine) forgetEnsured(sandbox string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.ensured, sandbox)
}

// Adopt makes a session recorded before a restart known again. One whose
// first turn started (m.Started) resumes (--resume); its page is in its
// transcript. One that never started was never created by claude, so it
// stays first-turn (--session-id) with page, when the caller still holds
// it; without the page it is dropped: Check and Send answer
// errNeverStarted. A known session is unchanged.
func (e *SandboxEngine) Adopt(session string, m Meta, page *string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sessions == nil {
		e.sessions = map[string]*sbSession{}
	}
	if _, ok := e.sessions[session]; ok {
		return
	}
	st := &sbSession{profile: m.Profile}
	switch {
	case m.Started:
		st.created = true
	case page != nil:
		st.first = &AskRequest{URL: m.URL, Title: m.Title, Page: *page, OriginOnly: m.OriginOnly}
	default:
		st.dropped = true
	}
	e.sessions[session] = st
}

// errNeverStarted answers a session dropped by Adopt.
var errNeverStarted = errors.New("its first question never started before aimux restarted and its page was not kept; ask again from the page to start a new session")

// Check reports whether Send can serve session: an error for a session this
// engine does not know (started by another aimux, or lost).
func (e *SandboxEngine) Check(session string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.sessions[session]
	if st == nil {
		return fmt.Errorf("session %s is not known to the sandbox engine (it was started elsewhere or its record was lost): %w", session, acprun.ErrNotRunning)
	}
	if st.dropped {
		return fmt.Errorf("session %s: %w", session, errNeverStarted)
	}
	return nil
}

// Running reports whether a turn of session is running.
func (e *SandboxEngine) Running(session string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.sessions[session]
	return st != nil && st.cancel != nil
}

// Send runs one turn of session and returns its stop reason.
func (e *SandboxEngine) Send(ctx context.Context, session, text string, onEvent func(acprun.Event)) (string, error) {
	model := ModelFrom(ctx)
	switch model {
	case "", DefaultPageModel, OpusPageModel:
	default: // the API checks it too; the engine never puts anything else on argv
		return "", fmt.Errorf("unsupported model %q: choose %s or %s", model, DefaultPageModel, OpusPageModel)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, first, created, err := e.begin(session, cancel)
	if err != nil {
		return "", err
	}
	defer e.end(session)

	c, err := e.Compile(st.profile)
	if err != nil {
		return "", errcode.Wrap(errcode.ProfileUnavailable, fmt.Errorf("profile %s: %w", st.profile, err))
	}
	if err := e.ensure(ctx, c); err != nil {
		return "", err
	}
	harness := c.Harness
	if harness == "" {
		harness = "claude-code"
	}
	adapter, err := harnessFor(harness)
	if err != nil {
		return "", fmt.Errorf("harness %s: %w", harness, err)
	}
	var review TurnReview
	if e.Review != nil {
		review = e.Review(c)
	}
	caps := c.Capabilities()
	if len(review.LeftOut) > 0 {
		scope := c.Grant.Scope()
		for _, srv := range review.LeftOut {
			delete(scope, srv)
		}
		caps.Grant = profile.NewToolGrant(scope)
	}
	if model != "" {
		caps.Model = model // the turn's choice (--model), checked above
	}
	img := ImageFrom(ctx)
	// With an image claude reads one stream-json user message instead of text.
	launch := harnessconfig.Launch{Mode: harnessconfig.ModeHeadless, StreamJSONInput: img != nil}
	if created {
		launch.ResumeID = session
	} else {
		launch.SessionID = session
	}
	spec, err := adapter.Config(caps, launch)
	if err != nil {
		return "", fmt.Errorf("harness %s: %w", harness, err)
	}
	prompt := sandboxTurnPrompt(first, PriorFrom(ctx), img != nil, text)
	if img != nil {
		if prompt, err = streamUserMessage(prompt, img); err != nil {
			return "", err
		}
	}

	// A profile with no servers (research) needs no tool gateway: no token
	// is issued and the MCP config lists no servers.
	var base, token string
	if !c.Grant.Empty() {
		var leases Leases
		base, leases, err = e.Gateway()
		if err != nil {
			return "", fmt.Errorf("tool gateway: %w", err)
		}
		if leases == nil {
			return "", errors.New("tool gateway: no lease table")
		}
		if token, err = toolgateway.NewToken(); err != nil {
			return "", fmt.Errorf("tool gateway token: %w", err)
		}
		end, err := e.lease(ctx, leases, session, token, c)
		if err != nil {
			return "", leaseFailure(err)
		}
		// Deferred before rc.Close, so it runs after it: the lease ends once
		// the process that held its token is closed. Ending it does not
		// close MCP streams the listener still has open; closing the exec
		// reader ends the process group that opened them.
		defer end()
	}
	redact := func(err error) error { return redactToken(err, token) }
	toClient := acpEmitter(onEvent)
	collect := toolResultsFrom(ctx)
	emit := func(ev LaneEvent) {
		if ev.Type == "tool_result" { // stays in aimux: only the turn's collector sees it
			if collect != nil {
				collect(ToolResult{Name: ev.Tool, Input: ev.Input, Output: ev.Text, Failed: ev.Failed})
			}
			return
		}
		toClient(ev)
	}
	if review.Notice != "" {
		emit(LaneEvent{Type: "notice", Text: review.Notice})
	}

	// The prompt goes on stdin after the config line, never on a command line.
	rc, err := e.L.Exec(ctx, c, spec.MCPConfig(base, token), prompt, spec.Env, spec.Argv)
	if err != nil {
		e.forgetEnsured(c.Sandbox)
		return "", e.sandboxFailure(ctx, redact(fmt.Errorf("start claude in %s: %w", c.Sandbox, err)))
	}
	// Deferred after the Revoke above, so it runs before it, even when
	// onEvent panics: claude is gone before its token is. Close is
	// idempotent; the explicit call below collects the exit error.
	defer func() { _ = rc.Close() }()
	// Once claude's stream shows it running this session (its init event),
	// the id is taken, even if this turn fails; a retry must resume it.
	// Until then (openshell started but could not connect, or claude failed
	// to start) it is not, and a retry creates it again with --session-id.
	started := func(id string) {
		if id != session {
			return // claude ran another session: the turn fails below
		}
		e.mu.Lock()
		report := !st.created && e.OnStarted != nil
		st.created = true
		e.mu.Unlock()
		if report {
			if err := e.OnStarted(session); err != nil && e.Log != nil {
				e.Log.Warn("profile session: started flag not recorded; after a restart this session cannot be resumed", "session", session, "err", err)
			}
		}
	}
	res, readErr := parseStream(rc, emit, started)
	closeErr := rc.Close()

	if res.sawResult {
		e.archive(c.Sandbox, session)
	}
	switch {
	case ctx.Err() != nil:
		return "", ctx.Err()
	case !errors.Is(readErr, io.EOF):
		return "", redact(fmt.Errorf("read claude output: %w", readErr))
	case res.resultErr != "":
		return "", redact(fmt.Errorf("claude failed: %s", res.resultErr))
	case !res.sawResult && closeErr != nil:
		return "", e.sandboxFailure(ctx, redact(fmt.Errorf("claude produced no result: %w", closeErr)))
	case !res.sawResult:
		return "", errors.New("claude produced no result")
	case res.sid != session:
		return "", fmt.Errorf("claude ran session %q, not %q", res.sid, session)
	}
	if first != nil {
		e.mu.Lock()
		st.first = nil // the page is in the transcript now
		e.mu.Unlock()
	}
	return "end_turn", nil
}

// minTokenFragment is the shortest piece of a token that is redacted: remote
// stderr is cut to its last 4 KiB, so a token can arrive cut anywhere.
const minTokenFragment = 8

// redactedError is err with every token fragment removed from its text. It
// unwraps to err, so errors.Is still works; callers must show Error(), not
// the unwrapped error's text.
type redactedError struct {
	msg string
	err error
}

func (r *redactedError) Error() string { return r.msg }
func (r *redactedError) Unwrap() error { return r.err }

// redactToken removes token and every piece of it of minTokenFragment or
// more characters from err's text, longest first.
func redactToken(err error, token string) error {
	msg := err.Error()
	for n := len(token); n >= minTokenFragment; n-- {
		for i := 0; i+n <= len(token); i++ {
			msg = strings.ReplaceAll(msg, token[i:i+n], "[redacted]")
		}
	}
	return &redactedError{msg: msg, err: err}
}

// begin marks session's turn as running, returning its state, the page
// request still owed to a prompt (nil once a turn succeeded) and whether
// claude already created the session.
func (e *SandboxEngine) begin(session string, cancel context.CancelFunc) (*sbSession, *AskRequest, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.sessions[session]
	if st == nil {
		return nil, nil, false, fmt.Errorf("session %s: %w", session, acprun.ErrNotRunning)
	}
	if st.dropped {
		return nil, nil, false, fmt.Errorf("session %s: %w", session, errNeverStarted)
	}
	if st.cancel != nil {
		return nil, nil, false, fmt.Errorf("session %s: %w", session, acprun.ErrBusy)
	}
	st.cancel = cancel
	return st, st.first, st.created, nil
}

func (e *SandboxEngine) end(session string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st := e.sessions[session]; st != nil {
		st.cancel = nil
	}
}

// Cancel stops session's running turn: its claude process is killed and its
// token revoked as Send returns.
func (e *SandboxEngine) Cancel(session string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.sessions[session]
	if st == nil || st.cancel == nil {
		return acprun.ErrNotRunning
	}
	st.cancel()
	return nil
}

// archive copies the transcript out; a failure is logged, never the turn's.
func (e *SandboxEngine) archive(sandbox, session string) {
	ctx, cancel := context.WithTimeout(context.Background(), archiveTimeout)
	defer cancel()
	if err := sandboxlaunch.Archive(ctx, e.L.Backend, sandbox, session, e.ArchiveDir); err != nil && e.Log != nil {
		e.Log.Warn("profile session: archive failed", "session", session, "err", err)
	}
}

// sandboxPrompt is the turn's prompt. The first turn carries the page, its
// title and URL (only its origin when OriginOnly) inside a block whose
// delimiter is random per prompt, as untrusted data.
func sandboxPrompt(first *AskRequest, text string) string {
	return sandboxPromptPrior(first, nil, text)
}

// sandboxPromptPrior is sandboxPrompt with the user's earlier questions
// from another mode (any turn, not only the first), before the question.
func sandboxPromptPrior(first *AskRequest, prior []string, text string) string {
	return sandboxTurnPrompt(first, prior, false, text)
}

// sandboxTurnPrompt is sandboxPromptPrior with, when image, the attached
// image's untrusted-data framing after the page block.
func sandboxTurnPrompt(first *AskRequest, prior []string, image bool, text string) string {
	var b strings.Builder
	b.WriteString(sandboxPromptPreamble)
	if first != nil && (first.URL != "" || first.Title != "" || first.Page != "") {
		pageURL := first.URL
		if first.OriginOnly {
			pageURL = originOf(pageURL)
		}
		tag := pageDelimiter(first.Title + pageURL + first.Page + strings.Join(prior, "\n") + text)
		b.WriteString(strings.TrimPrefix(sandboxHead(tag), sandboxPromptPreamble))
		fmt.Fprintf(&b, "<%s>\nTitle: %s\nURL: %s\n", tag, headerLine(first.Title), headerLine(pageURL))
		if first.Page != "" {
			b.WriteString("\n" + first.Page + "\n")
		}
		fmt.Fprintf(&b, "</%s>\n\n", tag)
	}
	if image {
		b.WriteString(imageFraming)
	}
	b.WriteString(priorBlock(prior, text))
	b.WriteString(diagramHint + questionMarker + text)
	return b.String()
}

// streamUserMessage is the stream-json line claude -p --input-format
// stream-json reads for a turn with an image: one user message holding the
// prompt as a text block, then the image as a base64 image block.
func streamUserMessage(prompt string, img *acprun.Image) (string, error) {
	type source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	type block struct {
		Type   string  `json:"type"`
		Text   string  `json:"text,omitempty"`
		Source *source `json:"source,omitempty"`
	}
	msg := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []block{
		{Type: "text", Text: prompt},
		{Type: "image", Source: &source{Type: "base64", MediaType: img.MimeType, Data: img.Data}},
	}}}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(msg); err != nil { // one line, newline-terminated
		return "", fmt.Errorf("encode the prompt: %w", err)
	}
	return b.String(), nil
}

// acpEmitter turns stream events into ACP-shaped session updates, as the
// remote API's clients read them from adapter-driven sessions.
func acpEmitter(onEvent func(acprun.Event)) func(LaneEvent) {
	n := 0
	return func(ev LaneEvent) {
		var u map[string]any
		switch ev.Type {
		case "delta":
			u = map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": ev.Text}}
		case "tool_call":
			n++
			status := "pending"
			if strings.HasPrefix(ev.Text, "failed: ") {
				status = "failed"
			}
			u = map[string]any{"sessionUpdate": "tool_call", "toolCallId": fmt.Sprintf("call-%d", n), "title": ev.Tool, "status": status}
			if ev.Text != "" { // a failure's text, or the call's snippet (a WebFetch's url)
				u["content"] = []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": ev.Text}}}
			}
		case "notice":
			// aimux's own line for the client (held-back tools), not the
			// agent's; ACP clients that do not know it skip it.
			u = map[string]any{"sessionUpdate": "aimux_notice", "content": map[string]any{"type": "text", "text": ev.Text}}
		default:
			return
		}
		data, _ := json.Marshal(u)
		onEvent(acprun.Event{Kind: acprun.KindUpdate, Data: data})
	}
}

// newUUID is a random (version 4) UUID, the form claude takes for --session-id.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// originOf is raw's scheme://host[:port]; "" when raw does not parse, so
// nothing of an unparsable URL reaches the prompt.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Leases is the gateway's lease table as a turn uses it: a
// toolgateway.ControlClient for the gateway worker, or
// toolgateway.LocalLeases in the same process.
type Leases interface {
	PutLease(ctx context.Context, l toolgateway.Lease) error
	Renew(ctx context.Context, session string, until time.Time) error
	DeleteLease(ctx context.Context, session string) error
}

// leaseCallTimeout bounds a renew or delete the turn makes in the
// background; leasePutTimeout a re-put, which the worker may hold while it
// loads the lease's snapshot (15 s) and lists its servers (5 s).
const (
	leaseCallTimeout = 5 * time.Second
	leasePutTimeout  = 25 * time.Second
	// leaseProbeEvery is how often a turn asks the worker's status, so a
	// lease lost to a worker restart is put again within seconds.
	leaseProbeEvery = 10 * time.Second
	// leaseProbeRetry is the quicker check while the worker is not
	// answering, so its lease is back about a second after it does.
	leaseProbeRetry = time.Second
)

// leaseStatuser is the worker status a turn probes (toolgateway.ControlClient
// has it; in-process leases do not need it).
type leaseStatuser interface {
	Status(ctx context.Context) (toolgateway.ControlStatus, error)
}

// lease puts the turn's lease (token, the grant, its snapshot and pins)
// and renews it until end is called, putting it again when the gateway
// lost it (a restarted worker). end stops renewing and deletes it.
func (e *SandboxEngine) lease(ctx context.Context, leases Leases, session, token string, c profile.Compiled) (end func(), err error) {
	scope := c.Grant.Scope()
	l := toolgateway.Lease{Session: session, Token: token, Allow: scope, Version: c.Snapshot.Version(),
		Pins: toolgateway.PinsFor(c.Snapshot, scope), Expires: time.Now().Add(toolgateway.LeaseTTL)}
	// Which worker gets the lease is read before the put: a restart in
	// between then shows as a new start time at the first probe. Unknown
	// (the status failed) means the first good probe puts the lease again.
	st, _ := leases.(leaseStatuser)
	var started time.Time
	if st != nil {
		if s, err := st.Status(ctx); err == nil {
			started = s.StartedAt
		}
	}
	if err := leases.PutLease(ctx, l); err != nil {
		return nil, err
	}
	every := e.RenewEvery
	if every == 0 {
		every = toolgateway.LeaseRenewEvery
	}
	// Each renew or re-put runs to its own deadline, never cut short by the
	// turn's end: a put the client abandoned could still land on the worker
	// after the delete. end waits for one in flight, then deletes.
	probeEvery := e.ProbeEvery
	if probeEvery == 0 {
		probeEvery = leaseProbeEvery
	}
	put := func(until time.Time) error {
		l.Expires = until
		pctx, cancel := context.WithTimeout(context.Background(), leasePutTimeout)
		defer cancel()
		return leases.PutLease(pctx, l)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { //nolint:gosec // G118: on purpose detached from the turn's context, see above
		defer close(done)
		renew := time.NewTicker(every)
		defer renew.Stop()
		retry := e.ProbeRetryEvery
		if retry == 0 {
			retry = leaseProbeRetry
		}
		probe := time.NewTimer(probeEvery)
		defer probe.Stop()
		for {
			var err error
			select {
			case <-stop:
				return
			case <-renew.C:
				until := time.Now().Add(toolgateway.LeaseTTL)
				cctx, cancel := context.WithTimeout(context.Background(), leaseCallTimeout)
				err = leases.Renew(cctx, session, until)
				cancel()
				if errors.Is(err, toolgateway.ErrUnknownLease) {
					err = put(until)
				}
				if err != nil { // the worker may be restarting: look again soon
					probe.Reset(retry)
				}
			case <-probe.C:
				if st == nil {
					continue
				}
				cctx, cancel := context.WithTimeout(context.Background(), leaseCallTimeout)
				s, serr := st.Status(cctx)
				cancel()
				if serr != nil {
					probe.Reset(retry) // not answering yet: look again soon
					continue
				}
				probe.Reset(probeEvery)
				// A restarted worker (new start time) or one holding no lease
				// lost this turn's: put it again now, not at the renewal.
				if started.IsZero() || !s.StartedAt.Equal(started) || s.Leases == 0 {
					if err = put(time.Now().Add(toolgateway.LeaseTTL)); err != nil {
						probe.Reset(retry) // try the put again soon
					}
				}
				// The new start time counts only once this turn's lease is
				// back, so a failed put is retried at the next probe.
				if err == nil {
					started = s.StartedAt
				}
			}
			if err != nil && e.Log != nil {
				e.Log.Warn("tool gateway: lease renewal failed; the turn's tools stop when the lease expires", "session", session, "err", err)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		dctx, cancel := context.WithTimeout(context.Background(), leaseCallTimeout)
		defer cancel()
		if err := leases.DeleteLease(dctx, session); err != nil && e.Log != nil {
			e.Log.Warn("tool gateway: lease not deleted; it expires on its own", "session", session, "err", err)
		}
	}, nil
}
