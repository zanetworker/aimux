package modelproxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Health is what the proxy knows about its own state, as GET /health returns it. It reports work the proxy has
// already done rather than running a new check, so asking costs nothing and never starts Codex.
type Health struct {
	// Status is "ok" or "degraded"; Reason says what went wrong, and is empty while the status is ok.
	Status        string      `json:"status"`
	Reason        string      `json:"reason,omitempty"`
	Version       string      `json:"version,omitempty"`
	StartedAt     time.Time   `json:"started_at"`
	UptimeSeconds int64       `json:"uptime_seconds"`
	Codex         CodexHealth `json:"codex"`
	// Requests counts turns sent to Codex. A request Claude Code malformed never reaches Codex and is not counted.
	Requests StageHealth `json:"requests"`
	// Probe counts the rate-limit reads behind the HUD's usage file. They cost no tokens, so on the stable proxy
	// they are a standing check that the Codex login and app-server still answer.
	Probe StageHealth `json:"codex_probe"`
	// Ledger is the record this proxy writes of its model calls. Empty when it was given none.
	Ledger LedgerHealth `json:"ledger"`
}

// LedgerHealth is how the proxy's ledger writer is doing.
type LedgerHealth struct {
	Seq    uint64 `json:"seq,omitempty"`    // the last line number handed out
	Failed uint64 `json:"failed,omitempty"` // lines that could not be written
	Off    bool   `json:"off,omitempty"`    // the file could not be opened at start
}

// CodexHealth describes the Codex side of the proxy.
type CodexHealth struct {
	Command string `json:"command"`
	// AppServerRunning is false while Codex is starting, after a failed start and after a crash, which is normal:
	// the proxy starts app-server again on the next turn.
	AppServerRunning bool `json:"app_server_running"`
}

// StageHealth counts one kind of work and remembers how it last went.
type StageHealth struct {
	Total       int        `json:"total"`
	Failed      int        `json:"failed"`
	LastOKAt    *time.Time `json:"last_ok_at,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

type stageState struct {
	total, failed int
	// lastFailed is the most recent attempt's outcome. The timestamps are for the reader, and are not compared:
	// they carry no monotonic reading, so a clock that steps backwards could reorder them.
	lastFailed  bool
	lastOK      time.Time
	lastErrorAt time.Time
	lastError   string
}

// failing reports whether the most recent attempt failed. An older error with a success after it is history.
func (s stageState) failing() bool { return s.lastFailed }

func (s stageState) report() StageHealth {
	health := StageHealth{Total: s.total, Failed: s.failed, LastError: s.lastError}
	if !s.lastOK.IsZero() {
		at := s.lastOK
		health.LastOKAt = &at
	}
	if !s.lastErrorAt.IsZero() {
		at := s.lastErrorAt
		health.LastErrorAt = &at
	}
	return health
}

// healthState records how the proxy's work goes. Its own mutex keeps recording off the backend's lock, so a slow
// health reader cannot hold up a turn.
type healthState struct {
	mu        sync.Mutex
	version   string
	startedAt time.Time
	requests  stageState
	probe     stageState
}

func (h *healthState) record(stage *stageState, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stage.total++
	stage.lastFailed = err != nil
	if err == nil {
		stage.lastOK = time.Now().UTC()
		return
	}
	stage.failed++
	stage.lastErrorAt = time.Now().UTC()
	stage.lastError = err.Error()
}

func (h *healthState) snapshot(command string, appServerRunning bool) Health {
	h.mu.Lock()
	defer h.mu.Unlock()
	health := Health{
		Status:    "ok",
		Version:   h.version,
		StartedAt: h.startedAt,
		Codex:     CodexHealth{Command: command, AppServerRunning: appServerRunning},
		Requests:  h.requests.report(),
		Probe:     h.probe.report(),
	}
	if !h.startedAt.IsZero() {
		health.UptimeSeconds = int64(time.Since(h.startedAt).Seconds())
	}
	switch {
	case h.requests.failing():
		health.Status, health.Reason = "degraded", "the last request failed: "+h.requests.lastError
	case h.probe.failing():
		health.Status, health.Reason = "degraded", "the last Codex check failed: "+h.probe.lastError
	}
	return health
}

// observe records an attempt unless its context was cancelled: a client that hung up, or a proxy shutting down,
// says nothing about the proxy's health. A deadline that expired does say something, so it is recorded: Codex
// going quiet is the failure health exists to report.
func (h *healthState) observe(ctx context.Context, stage *stageState, err error) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	// A conversation too long for its model is that session's to shorten. Codex answered, and correctly: the turn
	// is counted, and as one that worked, so an earlier failure no longer stands as the last word.
	if err != nil && classifyFailure(err) == failureContextTooLong {
		err = nil
	}
	h.record(stage, err)
}

// LivenessService names the model proxy in its keyless liveness answer.
const LivenessService = "aimux-model-proxy"

// Liveness is what anyone on this machine may learn without the key: enough for a status check, no error text.
type Liveness struct {
	Service   string     `json:"service"` // always "aimux-model-proxy"
	Status    string     `json:"status"`  // "ok" or "degraded"
	Version   string     `json:"version,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	LastOKAt  *time.Time `json:"last_ok_at,omitempty"` // last successful turn
	// The ledger this proxy writes: the last line number it handed out, how many lines it could not write, and
	// whether the file could not be opened at start. Counts only, like the rest of this answer.
	LedgerSeq    uint64 `json:"ledger_seq,omitempty"`
	LedgerFailed uint64 `json:"ledger_failed,omitempty"`
	LedgerOff    bool   `json:"ledger_off,omitempty"`
	// Engines are the engines this proxy can answer with: "codex", and "anthropic" when it was started with it.
	// A build from before there was a second engine names none.
	Engines []string `json:"engines,omitempty"`
	// Proof answers a challenge sent as ?challenge=<nonce>: LivenessProof of the proxy's key and that nonce. It
	// shows the answer came from a process that holds the key, without the key going either way.
	Proof string `json:"proof,omitempty"`
}

// maxChallenge bounds the nonce the keyless health answer will sign.
const maxChallenge = 128

// LivenessProof is what a proxy holding key, listening on port, answers to the challenge nonce. The port is the
// one the proxy accepted the connection on, so an answer relayed from the proxy on another port does not pass
// for an answer from this one.
func LivenessProof(key, nonce string, port int) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = fmt.Fprintf(mac, "aimux-model-proxy-liveness:%d:%s", port, nonce)
	return hex.EncodeToString(mac.Sum(nil))
}

// Why a listener cannot be given a shell's sessions.
var (
	ErrNotTheProxy       = errors.New("the listener did not show it is the model proxy that holds the key")
	ErrNoAnthropicEngine = errors.New("the model proxy runs without its Anthropic engine")
)

// CheckSessionProxy reports whether live, the answer to a challenge with nonce sent to port, came from the model
// proxy that holds key, listens on that port, and can answer with both engines. Only then may a session be pointed at it: the session will send it
// the key, the owner's Claude login and every conversation.
func CheckSessionProxy(live Liveness, key, nonce string, port int) error {
	if err := CheckProxyIdentity(live, key, nonce, port); err != nil {
		return err
	}
	if !slices.Contains(live.Engines, engineAnthropic) {
		return ErrNoAnthropicEngine
	}
	return nil
}

// CheckProxyIdentity reports whether live, the answer to a challenge with nonce sent to port, came from the model
// proxy that holds key and listens on that port, whatever engines it has. A session that uses Codex alone needs
// no more than this, and no less: it is about to be sent the key and every conversation.
func CheckProxyIdentity(live Liveness, key, nonce string, port int) error {
	proven := key != "" && nonce != "" && hmac.Equal([]byte(live.Proof), []byte(LivenessProof(key, nonce, port)))
	if live.Service != LivenessService || !proven {
		return ErrNotTheProxy
	}
	return nil
}

// liveness keeps only the fields Liveness allows from a full health report.
func liveness(health Health) Liveness {
	return Liveness{
		Service: LivenessService, Status: health.Status, Version: health.Version,
		StartedAt: health.StartedAt, LastOKAt: health.Requests.LastOKAt,
	}
}
