package modelproxy

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Health is what the proxy knows about its own state, as GET /health returns it. It reports work the proxy has
// already done rather than running a new check, so asking costs nothing and never starts Codex.
type Health struct {
	// Status is "ok" or "degraded"; Reason says what went wrong, and is empty while the status is ok.
	Status        string      `json:"status"`
	Reason        string      `json:"reason,omitempty"`
	StartedAt     time.Time   `json:"started_at"`
	UptimeSeconds int64       `json:"uptime_seconds"`
	Codex         CodexHealth `json:"codex"`
	// Requests counts turns sent to Codex. A request Claude Code malformed never reaches Codex and is not counted.
	Requests StageHealth `json:"requests"`
	// Probe counts the rate-limit reads behind the HUD's usage file. They cost no tokens, so on the stable proxy
	// they are a standing check that the Codex login and app-server still answer.
	Probe StageHealth `json:"codex_probe"`
}

// CodexHealth describes the Codex side of the proxy.
type CodexHealth struct {
	Command string `json:"command"`
	// AppServerRunning is false before the first turn and after a crash, which is normal: the proxy starts
	// app-server again on the next turn.
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
	h.record(stage, err)
}
