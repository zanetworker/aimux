package acprun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type recorded struct {
	Method string         `json:"method"`
	Meta   map[string]any `json:"meta"`
	Value  string         `json:"value"`
}

func readRecord(t *testing.T, path string) []recorded {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- test temp file
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []recorded
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r recorded
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("record line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}

func recordingSpec(t *testing.T) (Spec, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "record.jsonl")
	return fakeSpec(t, fakeRecordEnv+"="+path), path
}

func lockdownMeta(model string) map[string]any {
	return map[string]any{"claudeCode": map[string]any{"options": map[string]any{
		"tools": []any{}, "settingSources": []any{}, "mcpServers": map[string]any{},
		"strictMcpConfig": true, "model": model,
	}}}
}

func TestStartSendsMetaOnNewAndLoad(t *testing.T) {
	spec, path := recordingSpec(t)
	spec.Meta = lockdownMeta("sonnet")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nr, err := StartNew(ctx, spec, nil)
	if err != nil {
		t.Fatalf("StartNew: %v", err)
	}
	_ = nr.Close()
	lr := start(t, spec, "sess-1", nil)
	_ = lr.Close()

	got := readRecord(t, path)
	if len(got) != 2 || got[0].Method != "session/new" || got[1].Method != "session/load" {
		t.Fatalf("recorded %+v, want session/new then session/load", got)
	}
	// Compare as JSON, the way the agent sees it.
	want, _ := json.Marshal(spec.Meta)
	for _, r := range got {
		have, _ := json.Marshal(r.Meta)
		if string(have) != string(want) {
			t.Fatalf("%s _meta = %s, want %s", r.Method, have, want)
		}
	}
}

func TestStartWithoutMetaSendsNone(t *testing.T) {
	spec, path := recordingSpec(t)
	r := start(t, spec, "sess-1", nil)
	_ = r.Close()
	got := readRecord(t, path)
	if len(got) != 1 || got[0].Meta != nil {
		t.Fatalf("recorded %+v, want one load with no _meta", got)
	}
}

func TestRunModelIsWhatTheAgentReports(t *testing.T) {
	spec := fakeSpec(t)
	spec.Meta = lockdownMeta("sonnet") // ignored by the adapter
	r := start(t, spec, "sess-1", nil)
	if got := r.Model(); got != "opus" {
		t.Fatalf("Model() = %q after load, want the agent-reported opus", got)
	}
}

func TestRunSetModelSwitchesTheSession(t *testing.T) {
	spec, path := recordingSpec(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := StartNew(ctx, spec, nil)
	if err != nil {
		t.Fatalf("StartNew: %v", err)
	}
	defer func() { _ = r.Close() }()
	if got := r.Model(); got != "opus" {
		t.Fatalf("Model() = %q, want the agent's opus", got)
	}
	if err := r.SetModel(ctx, "sonnet"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if got := r.Model(); got != "sonnet" {
		t.Fatalf("Model() = %q after SetModel, want sonnet", got)
	}
	events, _ := prompt(t, r, "model?")
	if got := messageText(t, events); got != "model: sonnet" {
		t.Fatalf("agent reports %q, want the switched model", got)
	}
	var sets []string
	for _, rec := range readRecord(t, path) {
		if rec.Method == "session/set_config_option" {
			sets = append(sets, rec.Value)
		}
	}
	if !reflect.DeepEqual(sets, []string{"sonnet"}) {
		t.Fatalf("set_config_option values %v, want one sonnet", sets)
	}
}

func TestRunSetModelUnknownModelIsAClearError(t *testing.T) {
	spec, path := recordingSpec(t)
	r := start(t, spec, "sess-1", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := r.SetModel(ctx, "claude-sonnet-5-5") // looks right, but the adapter does not offer it
	if err == nil || !strings.Contains(err.Error(), "sonnet") || !strings.Contains(err.Error(), "sonnet, haiku") {
		t.Fatalf("got %v, want an error naming the model and the offered ones", err)
	}
	if !errors.Is(err, ErrModelNotOffered) {
		t.Fatalf("got %v, want ErrModelNotOffered", err)
	}
	if r.Model() != "opus" {
		t.Fatalf("Model() = %q, want it unchanged", r.Model())
	}
	for _, rec := range readRecord(t, path) {
		if rec.Method == "session/set_config_option" {
			t.Fatal("an unknown model was still sent to the agent")
		}
	}
}

// modelRunner is a fakeRunner that can switch models.
type modelRunner struct {
	fakeRunner
	model  atomic.Value
	sets   atomic.Int32
	setErr error
	order  *[]string
}

func (m *modelRunner) Model() string { s, _ := m.model.Load().(string); return s }
func (m *modelRunner) SetModel(_ context.Context, model string) error {
	m.sets.Add(1)
	if m.setErr != nil {
		return m.setErr
	}
	*m.order = append(*m.order, "set:"+model)
	m.model.Store(model)
	return nil
}

func TestSendModelSwitchesOnceBeforePrompt(t *testing.T) {
	var order []string
	mr := &modelRunner{order: &order}
	mr.model.Store("sonnet")
	mr.promptF = func(ctx context.Context, text string) (string, error) {
		order = append(order, "prompt:"+text)
		return "end_turn", nil
	}
	m := NewManager(ManagerOptions{Start: func(ctx context.Context, id, cwd string) (Runner, error) { return mr, nil }})
	defer m.Close()
	ctx := context.Background()
	for _, text := range []string{"a", "b"} {
		if _, err := m.SendModel(ctx, "s", "/tmp", "opus", text, func(Event) {}); err != nil {
			t.Fatalf("SendModel(%q): %v", text, err)
		}
	}
	want := []string{"set:opus", "prompt:a", "prompt:b"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}
	if _, err := m.SendModel(ctx, "s", "/tmp", "", "c", func(Event) {}); err != nil {
		t.Fatalf("SendModel with no model: %v", err)
	}
	if mr.sets.Load() != 1 {
		t.Fatalf("SetModel called %d times, want 1", mr.sets.Load())
	}
}

func TestSendModelFailureKeepsTheRunAndSendsNothing(t *testing.T) {
	var order []string
	mr := &modelRunner{order: &order, setErr: errors.New("no such model")}
	mr.model.Store("sonnet")
	prompted := false
	mr.promptF = func(ctx context.Context, text string) (string, error) { prompted = true; return "end_turn", nil }
	m := NewManager(ManagerOptions{Start: func(ctx context.Context, id, cwd string) (Runner, error) { return mr, nil }})
	defer m.Close()
	_, err := m.SendModel(context.Background(), "s", "/tmp", "opus", "a", func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "no such model") || !errors.Is(err, ErrModelSwitch) {
		t.Fatalf("got %v, want the SetModel error marked ErrModelSwitch", err)
	}
	if prompted || mr.closed.Load() {
		t.Fatalf("prompted=%v closed=%v, want neither", prompted, mr.closed.Load())
	}
	// The session is free again.
	mr.setErr = nil
	if _, err := m.SendModel(context.Background(), "s", "/tmp", "opus", "b", func(Event) {}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestSendModelOnARunnerThatCannotSwitch(t *testing.T) {
	r := &fakeRunner{}
	m := NewManager(ManagerOptions{Start: func(ctx context.Context, id, cwd string) (Runner, error) { return r, nil }})
	defer m.Close()
	_, err := m.SendModel(context.Background(), "s", "/tmp", "opus", "a", func(Event) {})
	if err == nil || !errors.Is(err, ErrModelSwitch) {
		t.Fatalf("got %v, want ErrModelSwitch", err)
	}
}

func TestManagerSetModelSwitchesAnIdleSession(t *testing.T) {
	var order []string
	mr := &modelRunner{order: &order}
	mr.model.Store("opus")
	m := NewManager(ManagerOptions{StartNew: func(ctx context.Context, cwd string) (Runner, string, error) { return mr, "n1", nil }})
	defer m.Close()
	ctx := context.Background()
	if err := m.SetModel(ctx, "missing", "sonnet"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("SetModel on no run: %v, want ErrNotRunning", err)
	}
	id, err := m.New(ctx, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := m.SetModel(ctx, id, "sonnet"); err != nil {
			t.Fatalf("SetModel %d: %v", i, err)
		}
	}
	if mr.sets.Load() != 1 || mr.Model() != "sonnet" {
		t.Fatalf("sets=%d model=%q, want one switch to sonnet", mr.sets.Load(), mr.Model())
	}
	// The session is not left busy.
	if _, err := m.Send(ctx, id, "/tmp", "hi", func(Event) {}); err != nil {
		t.Fatalf("Send after SetModel: %v", err)
	}
	mr.setErr = errors.New("rejected")
	if err := m.SetModel(ctx, id, "opus"); !errors.Is(err, ErrModelSwitch) {
		t.Fatalf("failed switch: %v, want ErrModelSwitch", err)
	}
}

func TestRunTracksConfigOptionUpdates(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	prompt(t, r, "switch-elsewhere")
	if got := r.Model(); got != "haiku" {
		t.Fatalf("Model() = %q, want the model from the agent's config_option_update", got)
	}
}

// A session reloaded after a restart comes back on the adapter's opus,
// whatever _meta says; the next sonnet ask switches it again.
func TestReloadedSessionIsSwitchedAgain(t *testing.T) {
	spec, path := recordingSpec(t)
	spec.Meta = lockdownMeta("sonnet")
	m := NewManager(ManagerOptions{Start: func(ctx context.Context, id, cwd string) (Runner, error) {
		s := spec
		s.Cwd = cwd
		return Start(ctx, s, id, nil)
	}})
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var text string
	onEvent := func(e Event) { text += messageText(t, []Event{e}) }
	for i := 0; i < 2; i++ {
		text = ""
		if _, err := m.SendModel(ctx, "sess-1", spec.Cwd, "sonnet", "model?", onEvent); err != nil {
			t.Fatalf("SendModel %d: %v", i, err)
		}
		if text != "model: sonnet" {
			t.Fatalf("turn %d: agent says %q, want sonnet", i, text)
		}
		if err := m.Drop("sess-1"); err != nil { // the adapter goes away, as on a restart
			t.Fatal(err)
		}
	}
	loads, sets := 0, 0
	for _, rec := range readRecord(t, path) {
		switch rec.Method {
		case "session/load":
			loads++
			if rec.Meta == nil {
				t.Fatal("reload sent no _meta")
			}
		case "session/set_config_option":
			sets++
		}
	}
	if loads != 2 || sets != 2 {
		t.Fatalf("loads=%d sets=%d, want 2 and 2 (switched again after the reload)", loads, sets)
	}
}
