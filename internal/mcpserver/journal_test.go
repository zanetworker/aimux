package mcpserver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournal_WriteAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")

	j, err := NewJournal(path)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}

	if err := j.Record(TaskEvent{TaskID: "t-1", State: "created", Prompt: "fix the bug"}); err != nil {
		t.Fatalf("Record created: %v", err)
	}
	if err := j.Record(TaskEvent{TaskID: "t-1", State: "running", Sandbox: "sb-a"}); err != nil {
		t.Fatalf("Record running: %v", err)
	}
	if err := j.Record(TaskEvent{TaskID: "t-1", State: "done", Result: `{"type":"text","summary":"fixed"}`}); err != nil {
		t.Fatalf("Record done: %v", err)
	}
	_ = j.Close()

	// Replay from file
	j2, err := NewJournal(path)
	if err != nil {
		t.Fatalf("NewJournal (replay): %v", err)
	}
	defer func() { _ = j2.Close() }()

	tasks := j2.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	ts := tasks["t-1"]
	if ts.State != "done" {
		t.Errorf("state: got %q, want 'done'", ts.State)
	}
	if ts.Prompt != "fix the bug" {
		t.Errorf("prompt: got %q", ts.Prompt)
	}
	if ts.Sandbox != "sb-a" {
		t.Errorf("sandbox: got %q", ts.Sandbox)
	}
	if ts.Result == "" {
		t.Error("result should not be empty")
	}
}

func TestJournal_MultipleTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	j, err := NewJournal(path)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}

	_ = j.Record(TaskEvent{TaskID: "a", State: "created", Prompt: "task a"})
	_ = j.Record(TaskEvent{TaskID: "b", State: "created", Prompt: "task b"})
	_ = j.Record(TaskEvent{TaskID: "a", State: "done"})

	tasks := j.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks["a"].State != "done" {
		t.Errorf("task a: got %q, want 'done'", tasks["a"].State)
	}
	if tasks["b"].State != "created" {
		t.Errorf("task b: got %q, want 'created'", tasks["b"].State)
	}
	_ = j.Close()
}

func TestJournal_MissingFile_CreatesNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.jsonl")

	j, err := NewJournal(path)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	defer func() { _ = j.Close() }()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("journal file should have been created")
	}
	if len(j.Tasks()) != 0 {
		t.Error("new journal should have 0 tasks")
	}
}

func TestJournal_SkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")

	// Write a valid line and a garbage line
	f, _ := os.Create(path) // #nosec G304 -- test temp path
	_, _ = f.WriteString(`{"task_id":"t-1","state":"created","prompt":"ok","ts":"2026-01-01T00:00:00Z"}` + "\n")
	_, _ = f.WriteString("this is not json\n")
	_, _ = f.WriteString(`{"task_id":"t-1","state":"done","ts":"2026-01-01T00:01:00Z"}` + "\n")
	_ = f.Close()

	j, err := NewJournal(path)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	defer func() { _ = j.Close() }()

	tasks := j.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks["t-1"].State != "done" {
		t.Errorf("state: got %q, want 'done'", tasks["t-1"].State)
	}
}

func TestJournal_ErrorState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	j, err := NewJournal(path)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	defer func() { _ = j.Close() }()

	_ = j.Record(TaskEvent{TaskID: "t-err", State: "created", Prompt: "will fail"})
	_ = j.Record(TaskEvent{TaskID: "t-err", State: "failed", Error: "timeout after 10m"})

	tasks := j.Tasks()
	if tasks["t-err"].State != "failed" {
		t.Errorf("state: got %q, want 'failed'", tasks["t-err"].State)
	}
	if tasks["t-err"].Error != "timeout after 10m" {
		t.Errorf("error: got %q", tasks["t-err"].Error)
	}
}

func TestJournal_TimestampAutoSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	j, err := NewJournal(path)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}

	ev := TaskEvent{TaskID: "t-ts", State: "created"}
	_ = j.Record(ev)
	_ = j.Close()

	// Read the file and check the last line has a timestamp
	data, _ := os.ReadFile(path) // #nosec G304 -- test temp path
	if len(data) == 0 {
		t.Fatal("journal file is empty")
	}
	line := string(data)
	if !contains(line, `"ts":"`) {
		t.Errorf("expected timestamp in journal line, got: %s", line)
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
