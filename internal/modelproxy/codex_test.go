package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var fakeCodex string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aimux-fakecodex-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeCodex = filepath.Join(dir, "fakecodex")
	cmd := exec.Command("go", "build", "-o", fakeCodex, "./testdata/fakecodex") // #nosec G204 -- builds the test fixture
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	status := 1
	if err := cmd.Run(); err == nil {
		status = m.Run()
	} else {
		fmt.Fprintln(os.Stderr, "build fake codex:", err)
	}
	_ = os.RemoveAll(dir)
	os.Exit(status)
}

func newFakeBackend(t *testing.T) (*CodexBackend, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "protocol.log")
	t.Setenv("AIMUX_FAKE_CODEX_LOG", log)
	backend := NewCodexBackend(CodexOptions{Command: fakeCodex, Stderr: os.Stderr})
	t.Cleanup(backend.Close)
	return backend, log
}

// The Fast ("priority") service tier roughly halves the time a long answer takes, so the proxy asks for it
// unless the owner chooses the standard tier to spend the plan's allowance more slowly.
func TestCodexBackendAsksForTheFastServiceTier(t *testing.T) {
	for _, tc := range []struct {
		name, tier string
		want       bool
	}{
		{name: "by default", want: true},
		{name: "asked for", tier: ServiceTierPriority, want: true},
		{name: "standard", tier: ServiceTierStandard, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "protocol.log")
			t.Setenv("AIMUX_FAKE_CODEX_LOG", log)
			backend := NewCodexBackend(CodexOptions{Command: fakeCodex, ServiceTier: tc.tier, Stderr: os.Stderr})
			t.Cleanup(backend.Close)
			if _, err := backend.Run(context.Background(), fakeTurn("hello"), func(Event) error { return nil }); err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(log) // #nosec G304 -- test temp file
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(args), `-c service_tier="priority"`); got != tc.want {
				t.Errorf("service_tier flag present = %v, want %v: %s", got, tc.want, args)
			}
		})
	}
}

func fakeTurn(text string) TurnRequest {
	return TurnRequest{Model: defaultModel, Effort: "low", Input: []textPart{{Type: "text", Text: text}}}
}

func TestCodexBackendTurn(t *testing.T) {
	backend, log := newFakeBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := fakeTurn("tool")
	req.System = "base instructions"
	req.Tools = []dynamicTool{{"function", "Read", "Read it", json.RawMessage(`{"type":"object"}`)}}
	req.History = []any{json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"previous"}]}`)}
	var gotText string
	var gotTool *ToolCall
	usage, err := backend.Run(ctx, req, func(event Event) error {
		gotText += event.Text
		if event.Tool != nil {
			if gotTool != nil {
				t.Fatal("emitted a second tool")
			}
			gotTool = event.Tool
		}
		return nil
	})
	if err != nil || gotText != "before tool" || gotTool == nil ||
		gotTool.ID != "call-t1" || gotTool.Name != "Read" || string(gotTool.Input) != `{"path":"a.txt"}` ||
		usage != (Usage{InputTokens: 17, OutputTokens: 3}) {
		t.Fatalf("text %q, tool %+v, usage %+v, err %v", gotText, gotTool, usage, err)
	}
	waitForLog(t, log, "interrupt t1")
	waitForLog(t, log, "unsubscribe t1")
}

func TestCodexBackendCancellation(t *testing.T) {
	backend, log := newFakeBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := backend.Run(ctx, fakeTurn("cancel-before-ack"), func(Event) error { return nil })
		done <- err
	}()
	waitForLog(t, log, "turn t1")
	cancel()
	if err := os.WriteFile(log+".release", nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("got %v, want context canceled", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("canceled turn did not return")
	}
	waitForLog(t, log, "interrupt t1")
	waitForLog(t, log, "unsubscribe t1")
}

func TestCodexBackendCrashAndRestart(t *testing.T) {
	backend, log := newFakeBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var got string
	emit := func(event Event) error { got += event.Text; return nil }
	_, err := backend.Run(ctx, fakeTurn("crash"), emit)
	if err == nil || !strings.Contains(err.Error(), "app-server exited") || got != "" {
		t.Fatalf("crash: text %q, err %v", got, err)
	}
	usage, err := backend.Run(ctx, fakeTurn("recovered"), emit)
	if err != nil || got != "recovered" || usage != (Usage{InputTokens: 17, OutputTokens: 3}) {
		t.Fatalf("restart: text %q, usage %+v, err %v", got, usage, err)
	}
	logBytes, err := os.ReadFile(log) // #nosec G304 -- test temp file
	// crash, the one retry (which crashes again), then the recovered turn.
	if err != nil || strings.Count(string(logBytes), "initialize ") != 3 {
		t.Fatalf("expected exactly three process starts: %s, %v", logBytes, err)
	}
}

func TestCodexBackendRetriesOnceWhenCodexDiesBeforeOutput(t *testing.T) {
	backend, log := newFakeBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var got string
	usage, err := backend.Run(ctx, fakeTurn("crash-once"), func(event Event) error { got += event.Text; return nil })
	if err != nil || got != "recovered" || usage != (Usage{InputTokens: 17, OutputTokens: 3}) {
		t.Fatalf("text %q, usage %+v, err %v; want the retried turn to succeed", got, usage, err)
	}
	logBytes, err := os.ReadFile(log) // #nosec G304 -- test temp file
	if err != nil || strings.Count(string(logBytes), "initialize ") != 2 {
		t.Fatalf("expected exactly two process starts: %s, %v", logBytes, err)
	}
}

func TestCodexBackendConcurrentTurns(t *testing.T) {
	backend, log := newFakeBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type result struct {
		prompt, text string
		usage        Usage
		err          error
	}
	done := make(chan result, 2)
	for i, prompt := range []string{"concurrent one", "concurrent two"} {
		go func() {
			r := result{prompt: prompt}
			r.usage, r.err = backend.Run(ctx, fakeTurn(prompt), func(event Event) error {
				r.text += event.Text
				return nil
			})
			done <- r
		}()
		if i == 0 {
			waitForLog(t, log, "turn t1")
		}
	}
	for range 2 {
		r := <-done
		wantInput := 17
		if r.prompt == "concurrent two" {
			wantInput = 29
		}
		if r.err != nil || r.text != r.prompt || r.usage != (Usage{InputTokens: wantInput, OutputTokens: 3}) {
			t.Fatalf("crossed turns: %+v", r)
		}
	}
	logBytes, err := os.ReadFile(log) // #nosec G304 -- test temp file
	if err != nil || strings.Count(string(logBytes), "initialize ") != 1 {
		t.Fatalf("expected one shared process: %s, %v", logBytes, err)
	}
}

func waitForLog(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, _ := os.ReadFile(path) // #nosec G304 -- test temp file
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %q in protocol log: %s", want, data)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCodexBackendWaitsForUsageAfterToolCall(t *testing.T) {
	backend, log := newFakeBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var gotTool *ToolCall
	usage, err := backend.Run(ctx, fakeTurn("tool-late-usage"), func(event Event) error {
		if event.Tool != nil {
			gotTool = event.Tool
		}
		return nil
	})
	if err != nil || gotTool == nil || gotTool.Name != "Read" {
		t.Fatalf("tool %+v, err %v", gotTool, err)
	}
	if usage != (Usage{InputTokens: 23, OutputTokens: 3}) {
		t.Fatalf("usage = %+v, want the usage reported after the tool call {23 3}", usage)
	}
	waitForLog(t, log, "interrupt t1")
}

func TestCodexBackendWarnsOnUntestedVersion(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"0.170.0", ""},
		{"0.170.4", ""},
		{"0.171.0", "Codex 0.171.0 has not been tested with the model proxy (tested: 0.170)"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			t.Setenv("AIMUX_FAKE_CODEX_LOG", filepath.Join(t.TempDir(), "protocol.log"))
			t.Setenv("AIMUX_FAKE_CODEX_VERSION", tc.version)
			var stderr strings.Builder
			backend := NewCodexBackend(CodexOptions{Command: fakeCodex, Stderr: &stderr})
			defer backend.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := backend.Run(ctx, fakeTurn("hello"), func(Event) error { return nil }); err != nil {
				t.Fatalf("turn failed: %v", err)
			}
			if tc.want == "" && strings.Contains(stderr.String(), "has not been tested") {
				t.Fatalf("unexpected warning: %s", stderr.String())
			}
			if tc.want != "" && !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr %q, want %q", stderr.String(), tc.want)
			}
		})
	}
}

// The lifecycle timeout bounds each lifecycle RPC on its own. Injecting a long history can take longer than that,
// and must not use up the clock turn/start gets: in production every new conversation with a long history failed
// after exactly 15 seconds, and each retry failed the same way.
func TestCodexBackendStartsTurnAfterSlowHistoryInjection(t *testing.T) {
	backend, log := newFakeBackend(t)
	backend.lifecycleTimeout = 200 * time.Millisecond
	t.Setenv("AIMUX_FAKE_CODEX_INJECT_DELAY_MS", "500")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req := fakeTurn("after slow history")
	req.History = []any{json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"previous"}]}`)}
	var text string
	_, err := backend.Run(ctx, req, func(event Event) error {
		text += event.Text
		return nil
	})
	if err != nil || text != "after slow history" {
		t.Fatalf("text %q, err %v", text, err)
	}
	if data := protocolLog(t, log); !strings.Contains(data, "inject t1 1") || !strings.Contains(data, "turn t1") {
		t.Fatalf("expected the history injected and the turn started:\n%s", data)
	}
}

// A turn/start Codex never answers still fails once its own lifecycle timeout runs out, instead of hanging.
func TestCodexBackendTurnStartTimesOut(t *testing.T) {
	backend, log := newFakeBackend(t)
	backend.lifecycleTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := time.Now()
	_, err := backend.Run(ctx, fakeTurn("no-turn-ack"), func(Event) error { return nil })
	took := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) || took < 200*time.Millisecond || took > 5*time.Second {
		t.Fatalf("err %v after %v, want a deadline after about 200ms", err, took)
	}
	// thread/start shares the timeout and the error, so prove the deadline came from turn/start: the fake
	// logs a turn only once it has received one.
	protocol, err := os.ReadFile(log) //nolint:gosec // G304: the test's own log in its temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(protocol), "turn ") {
		t.Fatalf("the deadline came before turn/start was sent; fake log:\n%s", protocol)
	}
}

// lockedBuffer collects the proxy's stderr: the backend and the app-server's stderr copier both write to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The proxy brings Codex up when it starts, so the first request does not pay for the start and a Codex that
// cannot start is known before anyone asks. Later turns and a second Start share that one process.
func TestCodexBackendStart_BringsCodexUpBeforeAnyTurn(t *testing.T) {
	log := filepath.Join(t.TempDir(), "protocol.log")
	t.Setenv("AIMUX_FAKE_CODEX_LOG", log)
	var stderr lockedBuffer
	backend := NewCodexBackend(CodexOptions{Command: fakeCodex, Stderr: &stderr})
	t.Cleanup(backend.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if backend.health().Codex.AppServerRunning {
		t.Fatal("app-server reported running before Start")
	}
	if err := backend.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !backend.health().Codex.AppServerRunning {
		t.Fatal("app-server not running after Start")
	}
	if got := stderr.String(); !strings.Contains(got, "model proxy: Codex started in ") {
		t.Fatalf("stderr %q does not say how long the start took", got)
	}

	var text string
	if _, err := backend.Run(ctx, fakeTurn("warm"), func(e Event) error { text += e.Text; return nil }); err != nil || text != "warm" {
		t.Fatalf("turn after Start: text %q, err %v", text, err)
	}
	if err := backend.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	logBytes, err := os.ReadFile(log) // #nosec G304 -- test temp file
	if err != nil || strings.Count(string(logBytes), "initialize ") != 1 {
		t.Fatalf("expected one process for Start, a turn and a second Start: %s, %v", logBytes, err)
	}
	if n := strings.Count(stderr.String(), "Codex started in "); n != 1 {
		t.Fatalf("start reported %d times, want once", n)
	}
}

// The start time is logged where Codex is started, so it is there when a request, not Start, was first.
func TestCodexBackend_LogsTheStartWhoeverAskedFirst(t *testing.T) {
	t.Setenv("AIMUX_FAKE_CODEX_LOG", filepath.Join(t.TempDir(), "protocol.log"))
	var stderr lockedBuffer
	backend := NewCodexBackend(CodexOptions{Command: fakeCodex, Stderr: &stderr})
	t.Cleanup(backend.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := backend.Run(ctx, fakeTurn("first"), func(Event) error { return nil }); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if err := backend.Start(ctx); err != nil {
		t.Fatalf("Start after a turn: %v", err)
	}
	if n := strings.Count(stderr.String(), "model proxy: Codex started in "); n != 1 {
		t.Fatalf("start reported %d times, want once, by the turn that started Codex: %q", n, stderr.String())
	}
}

// A failed start says why and leaves the proxy serving: the next request starts Codex again.
func TestCodexBackendStart_FailureIsReportedAndTheNextTurnRetries(t *testing.T) {
	t.Setenv("AIMUX_FAKE_CODEX_LOG", filepath.Join(t.TempDir(), "protocol.log"))
	t.Setenv("AIMUX_FAKE_CODEX_CATALOG_FAIL", "1")
	var stderr lockedBuffer
	backend := NewCodexBackend(CodexOptions{Command: fakeCodex, Stderr: &stderr})
	t.Cleanup(backend.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := backend.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "catalog refresh failed") {
		t.Fatalf("Start error %v, want the catalog failure", err)
	}
	if backend.health().Codex.AppServerRunning {
		t.Fatal("app-server reported running after a failed start")
	}
	want := "model proxy: Codex did not start, the first request will try again: "
	if got := stderr.String(); !strings.Contains(got, want) || !strings.Contains(got, "catalog refresh failed") {
		t.Fatalf("stderr %q, want %q with the cause", got, want)
	}

	t.Setenv("AIMUX_FAKE_CODEX_CATALOG_FAIL", "")
	var text string
	if _, err := backend.Run(ctx, fakeTurn("recovered"), func(e Event) error { text += e.Text; return nil }); err != nil || text != "recovered" {
		t.Fatalf("turn after a failed start: text %q, err %v", text, err)
	}
}

// Shutting down during the start is not a failure worth a log line, and a closed backend does not start Codex.
func TestCodexBackendStart_ShutdownIsQuiet(t *testing.T) {
	log := filepath.Join(t.TempDir(), "protocol.log")
	t.Setenv("AIMUX_FAKE_CODEX_LOG", log)
	var stderr lockedBuffer
	backend := NewCodexBackend(CodexOptions{Command: fakeCodex, Stderr: &stderr})
	t.Cleanup(backend.Close)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.Start(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start on a cancelled context: %v", err)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("a shutdown was logged: %q", got)
	}

	backend.Close()
	if err := backend.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Start on a closed backend: %v", err)
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Codex was started: %v", err)
	}
}
