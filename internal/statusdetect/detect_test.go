package statusdetect

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func testdataPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

func TestDetectFromJSONL(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    agent.Status
	}{
		{"active tool loop", "active.jsonl", agent.StatusActive},
		{"idle after end_turn", "idle.jsonl", agent.StatusIdle},
		{"waiting for permission", "waiting.jsonl", agent.StatusWaitingPermission},
		{"error context overflow", "error.jsonl", agent.StatusError},
		{"active on enqueue", "enqueue.jsonl", agent.StatusActive},
		{"missing file", "nonexistent.jsonl", agent.StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectFromJSONL(testdataPath(tt.fixture), 8192)
			if got != tt.want {
				t.Errorf("DetectFromJSONL(%q) = %v, want %v", tt.fixture, got, tt.want)
			}
		})
	}
}

func TestDetectFromJSONL_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "empty.jsonl")
	os.WriteFile(f, []byte(""), 0644)
	got := DetectFromJSONL(f, 8192)
	if got != agent.StatusUnknown {
		t.Errorf("DetectFromJSONL(empty) = %v, want StatusUnknown", got)
	}
}

func TestDetectFromJSONL_LargeFileTailRead(t *testing.T) {
	// Verify that seeking into a large file still works correctly.
	// Write a file larger than tailBytes, with an idle ending.
	tmp := t.TempDir()
	f := filepath.Join(tmp, "large.jsonl")

	// Write padding lines that are valid JSON but not meaningful.
	var data []byte
	padding := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_pad","content":"padding"}]},"timestamp":"2026-03-29T11:00:00Z"}` + "\n"
	for i := 0; i < 200; i++ {
		data = append(data, []byte(padding)...)
	}
	// End with an end_turn assistant message.
	ending := `{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"All done."}]},"timestamp":"2026-03-29T12:00:05Z"}` + "\n"
	data = append(data, []byte(ending)...)

	os.WriteFile(f, data, 0644)

	got := DetectFromJSONL(f, 4096)
	if got != agent.StatusIdle {
		t.Errorf("DetectFromJSONL(large file) = %v, want StatusIdle", got)
	}
}

func TestDetectFromJSONL_MalformedJSON(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "malformed.jsonl")
	os.WriteFile(f, []byte("not json at all\n{bad json\n"), 0644)
	got := DetectFromJSONL(f, 8192)
	if got != agent.StatusUnknown {
		t.Errorf("DetectFromJSONL(malformed) = %v, want StatusUnknown", got)
	}
}
