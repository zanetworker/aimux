package views

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/trace"
)

// liveParser stands for a parser that fell back to OTEL: its turns grow
// with each call while the file stays the same.
func liveParser(calls *int32) TraceParser {
	return func(string) ([]trace.Turn, error) {
		n := int(atomic.AddInt32(calls, 1))
		turns := make([]trace.Turn, n)
		for i := range turns {
			turns[i] = trace.Turn{Number: i + 1, UserLines: []string{fmt.Sprintf("span %d", i)}}
		}
		return turns, ErrLiveTrace
	}
}

func TestLogsViewReload_LiveTurnsReloadDespiteUnchangedFile(t *testing.T) {
	var calls int32
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 1)
	v := NewLogsView(1, path, liveParser(&calls))
	if got := len(v.Turns()); got != 1 {
		t.Fatalf("live turns not shown: %d, want 1", got)
	}
	v.Reload()
	v.Reload()
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("parser calls = %d, want 3: live turns must not be cached against the file", n)
	}
	if got := len(v.Turns()); got != 3 {
		t.Errorf("turns = %d, want the latest 3", got)
	}
}

func TestPreview_LiveTurnsReloadDespiteUnchangedFile(t *testing.T) {
	var calls int32
	path := writeTranscript(t, t.TempDir(), "a.jsonl", 1)
	p := NewPreviewPane()
	p.SetParser(liveParser(&calls))
	if !p.ApplyLoaded(runLoad(t, p.SetAgent(&agent.Agent{PID: 1, SessionFile: path}))) {
		t.Fatal("live result dropped")
	}
	if got := len(p.logsView.Turns()); got != 1 {
		t.Fatalf("live turns not shown: %d", got)
	}
	cmd := p.Reload()
	if cmd == nil {
		t.Fatal("Reload skipped live turns because the file did not change")
	}
	p.ApplyLoaded(runLoad(t, cmd))
	if got := len(p.logsView.Turns()); got != 2 {
		t.Errorf("turns = %d, want the latest 2", got)
	}
}
