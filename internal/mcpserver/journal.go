package mcpserver

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// TaskEvent is one line in the task journal.
type TaskEvent struct {
	TaskID    string `json:"task_id"`
	State     string `json:"state"`
	Prompt    string `json:"prompt,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Sandbox   string `json:"sandbox,omitempty"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	Timestamp string `json:"ts"`
}

// TaskState is the latest known state of a task (collapsed from events).
type TaskState struct {
	TaskID   string
	State    string
	Prompt   string
	Provider string
	Sandbox  string
	Result   string
	Error    string
}

// Journal appends task events to a JSONL file and replays on startup.
type Journal struct {
	mu    sync.Mutex
	file  *os.File
	tasks map[string]*TaskState
}

// NewJournal opens or creates a JSONL task journal. Replays existing
// events to rebuild in-memory state.
func NewJournal(path string) (*Journal, error) {
	j := &Journal{tasks: make(map[string]*TaskState)}

	if _, err := os.Stat(path); err == nil {
		if err := j.replay(path); err != nil {
			return nil, err
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- path from trusted caller
	if err != nil {
		return nil, err
	}
	j.file = f
	return j, nil
}

func (j *Journal) replay(path string) error {
	f, err := os.Open(path) // #nosec G304 -- path from trusted caller
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev TaskEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		j.apply(ev)
	}
	return scanner.Err()
}

func (j *Journal) apply(ev TaskEvent) {
	ts, ok := j.tasks[ev.TaskID]
	if !ok {
		ts = &TaskState{TaskID: ev.TaskID}
		j.tasks[ev.TaskID] = ts
	}
	ts.State = ev.State
	if ev.Prompt != "" {
		ts.Prompt = ev.Prompt
	}
	if ev.Provider != "" {
		ts.Provider = ev.Provider
	}
	if ev.Sandbox != "" {
		ts.Sandbox = ev.Sandbox
	}
	if ev.Result != "" {
		ts.Result = ev.Result
	}
	if ev.Error != "" {
		ts.Error = ev.Error
	}
}

// Record appends a task event to the journal file.
func (j *Journal) Record(ev TaskEvent) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := j.file.Write(append(data, '\n')); err != nil {
		return err
	}

	j.apply(ev)
	return nil
}

// Tasks returns a snapshot of all known task states.
func (j *Journal) Tasks() map[string]*TaskState {
	j.mu.Lock()
	defer j.mu.Unlock()
	cp := make(map[string]*TaskState, len(j.tasks))
	for k, v := range j.tasks {
		clone := *v
		cp[k] = &clone
	}
	return cp
}

// Close closes the journal file.
func (j *Journal) Close() error {
	if j.file != nil {
		return j.file.Close()
	}
	return nil
}
