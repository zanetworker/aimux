package views

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/trace"
)

// countingParser returns one turn per non-empty line of the file and counts
// its calls, so tests can tell whether parsing ran and when.
func countingParser(calls *int32) TraceParser {
	return func(path string) ([]trace.Turn, error) {
		atomic.AddInt32(calls, 1)
		data, err := os.ReadFile(path) // #nosec G304 -- test file
		if err != nil {
			return nil, err
		}
		var turns []trace.Turn
		for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			turns = append(turns, trace.Turn{Number: i + 1, UserLines: []string{line}})
		}
		return turns, nil
	}
}

func writeTranscript(t *testing.T, dir, name string, lines int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString("line\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendLine(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- test file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("more\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// runLoad runs a load command and returns its message, failing when the
// command is missing or returns something else.
func runLoad(t *testing.T, cmd tea.Cmd) PreviewLoadedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no load command")
	}
	msg, ok := cmd().(PreviewLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T, want PreviewLoadedMsg", msg)
	}
	return msg
}

func TestPreviewSetAgent_ParsesOffTheCaller(t *testing.T) {
	var calls int32
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 3)
	p := NewPreviewPane()
	p.SetSize(120, 40)
	p.SetParser(countingParser(&calls))

	cmd := p.SetAgent(&agent.Agent{PID: 1, SessionFile: path})
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("SetAgent parsed %d times itself; the parse belongs in the returned command", n)
	}
	if !strings.Contains(p.View(), "Loading") {
		t.Errorf("preview should say it is loading before the result arrives:\n%s", p.View())
	}
	msg := runLoad(t, cmd)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("load command parsed %d times, want 1", n)
	}
	if !p.ApplyLoaded(msg) {
		t.Fatal("result for the selected agent was dropped")
	}
	if got := len(p.logsView.Turns()); got != 3 {
		t.Errorf("turns after load = %d, want 3", got)
	}
}

func TestPreviewApplyLoaded_DropsResultForAgentNoLongerSelected(t *testing.T) {
	var calls int32
	dir := t.TempDir()
	first := writeTranscript(t, dir, "a.jsonl", 2)
	second := writeTranscript(t, dir, "b.jsonl", 5)
	p := NewPreviewPane()
	p.SetParser(countingParser(&calls))

	cmd1 := p.SetAgent(&agent.Agent{PID: 1, SessionFile: first})
	cmd2 := p.SetAgent(&agent.Agent{PID: 2, SessionFile: second})
	stale := runLoad(t, cmd1)
	if p.ApplyLoaded(stale) {
		t.Fatal("a result for agent 1 was applied while agent 2 is selected")
	}
	if p.logsView != nil && len(p.logsView.Turns()) != 0 {
		t.Fatalf("stale result leaked %d turns into the preview", len(p.logsView.Turns()))
	}
	if !p.ApplyLoaded(runLoad(t, cmd2)) {
		t.Fatal("result for the selected agent was dropped")
	}
	if got := len(p.logsView.Turns()); got != 5 {
		t.Errorf("turns = %d, want 5", got)
	}

	// Coming back to agent 1 starts a new load; the old result still does not apply.
	cmd3 := p.SetAgent(&agent.Agent{PID: 1, SessionFile: first})
	if p.ApplyLoaded(stale) {
		t.Error("an older load's result was applied after reselecting its agent")
	}
	if !p.ApplyLoaded(runLoad(t, cmd3)) {
		t.Error("the new load's result was dropped")
	}
}

func TestPreviewReload_OnlyWhenFileChanged(t *testing.T) {
	var calls int32
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 2)
	p := NewPreviewPane()
	p.SetParser(countingParser(&calls))
	a := &agent.Agent{PID: 1, SessionFile: path}

	cmd := p.SetAgent(a)
	if p.Reload() != nil {
		t.Error("Reload started a second load while one is in flight")
	}
	p.ApplyLoaded(runLoad(t, cmd))

	if p.Reload() != nil {
		t.Error("Reload started a load although the file did not change")
	}
	appendLine(t, path)
	cmd = p.Reload()
	if cmd == nil {
		t.Fatal("Reload did not load an appended file")
	}
	p.ApplyLoaded(runLoad(t, cmd))
	if got := len(p.logsView.Turns()); got != 3 {
		t.Errorf("turns after reload = %d, want 3", got)
	}
	if p.Reload() != nil {
		t.Error("Reload loaded again with no further change")
	}
}

func TestPreviewReload_MissingFileStillReloads(t *testing.T) {
	// No file yet (a new session, OTEL fills in): nothing to compare, so
	// every refresh asks the parser.
	var calls int32
	p := NewPreviewPane()
	p.SetParser(func(string) ([]trace.Turn, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	})
	cmd := p.SetAgent(&agent.Agent{PID: 1, SessionFile: filepath.Join(t.TempDir(), "none.jsonl")})
	p.ApplyLoaded(runLoad(t, cmd))
	if p.Reload() == nil {
		t.Error("Reload skipped a session whose file does not exist yet")
	}
}

func TestPreviewSetAgent_SameAgentLoadsOnceParserArrives(t *testing.T) {
	// The first selected agent used to stay empty: a SetAgent before the
	// parser was set recorded the PID, and later calls returned early.
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 2)
	p := NewPreviewPane()
	a := &agent.Agent{PID: 7, SessionFile: path}
	if cmd := p.SetAgent(a); cmd != nil {
		t.Fatal("SetAgent without a parser returned a load")
	}
	var calls int32
	p.SetParser(countingParser(&calls))
	cmd := p.SetAgent(a)
	if cmd == nil {
		t.Fatal("same agent with a parser now: no load started")
	}
	p.ApplyLoaded(runLoad(t, cmd))
	if got := len(p.logsView.Turns()); got != 2 {
		t.Errorf("turns = %d, want 2", got)
	}
	if p.SetAgent(a) != nil {
		t.Error("an unchanged agent started another load")
	}
}

func TestPreviewSetAgent_SameAgentKeepsFreshHeader(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(120, 30)
	p.SetAgent(&agent.Agent{PID: 1, Name: "x", Status: agent.StatusIdle, Activity: "idle"})
	p.SetAgent(&agent.Agent{PID: 1, Name: "x", Status: agent.StatusActive, Activity: "working"})
	if out := p.renderHeader(); !strings.Contains(out, "working") {
		t.Errorf("header kept the old agent data after a refresh:\n%s", out)
	}
}

func TestPreviewSetAgent_NilClears(t *testing.T) {
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 1)
	var calls int32
	p := NewPreviewPane()
	p.SetParser(countingParser(&calls))
	cmd := p.SetAgent(&agent.Agent{PID: 1, SessionFile: path})
	if p.SetAgent(nil) != nil {
		t.Error("clearing the agent returned a load")
	}
	if p.ApplyLoaded(runLoad(t, cmd)) {
		t.Error("a result applied after the selection was cleared")
	}
}

func TestPreviewTailParser_StatsSayLastTurns(t *testing.T) {
	var calls int32
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 4)
	p := NewPreviewPane()
	p.SetSize(120, 40)
	p.SetTailParser(countingParser(&calls))
	p.ApplyLoaded(runLoad(t, p.SetAgent(&agent.Agent{PID: 1, SessionFile: path})))
	if out := p.View(); !strings.Contains(out, "last 4 turns") {
		t.Errorf("a tail-read preview should label its turn count as the last turns:\n%s", out)
	}
	p.SetParser(countingParser(&calls))
	p.ApplyLoaded(runLoad(t, p.SetAgent(&agent.Agent{PID: 2, SessionFile: path})))
	if out := p.View(); strings.Contains(out, "last 4 turns") || !strings.Contains(out, "4 turns") {
		t.Errorf("a whole-file preview should show the plain turn count:\n%s", out)
	}
}

func TestLogsViewReload_SkipsUnchangedFile(t *testing.T) {
	var calls int32
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 2)
	v := NewLogsView(1, path, countingParser(&calls))
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("constructor parsed %d times, want 1", n)
	}
	v.Reload()
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("Reload parsed an unchanged file again (calls=%d)", n)
	}
	// Same size, newer mtime: still a change.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	v.Reload()
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("Reload ignored a new mtime (calls=%d)", n)
	}
	appendLine(t, path)
	v.Reload()
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("Reload ignored an appended line (calls=%d)", n)
	}
	if got := len(v.Turns()); got != 3 {
		t.Errorf("turns = %d, want 3", got)
	}
	// A new path always parses.
	other := writeTranscript(t, filepath.Dir(path), "b.jsonl", 2)
	v.SetFilePath(other)
	v.Reload()
	if n := atomic.LoadInt32(&calls); n != 4 {
		t.Errorf("Reload after SetFilePath did not parse (calls=%d)", n)
	}
}

func TestLogsViewReload_NoTurnsKeepsAsking(t *testing.T) {
	// An empty result may come from a parser that falls back to OTEL, whose
	// data grows without the file changing: keep asking.
	var calls int32
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 1)
	v := NewLogsView(1, path, func(string) ([]trace.Turn, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	})
	v.Reload()
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}
}
