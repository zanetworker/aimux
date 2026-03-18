package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// localTaskJSON matches the JSON schema that Claude Code writes to
// ~/.claude/tasks/{team}/task-*.json or {id}.json files.
type localTaskJSON struct {
	ID          string   `json:"id"`
	Subject     string   `json:"subject"`
	Description string   `json:"description"`
	ActiveForm  string   `json:"activeForm"`
	Status      string   `json:"status"`
	Blocks      []string `json:"blocks"`
	BlockedBy   []string `json:"blockedBy"`
}

// LoadFromLocalFiles reads all task JSON files from a team's task directory.
// The directory is typically ~/.claude/tasks/{teamID}/.
// It reads both task-*.json and *.json files (Claude Code uses both patterns).
func LoadFromLocalFiles(tasksDir string) ([]Task, error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tasks directory %q: %w", tasksDir, err)
	}

	var tasks []Task
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}

		path := filepath.Join(tasksDir, name)
		t, err := loadLocalTaskFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse task file %q: %w", path, err)
		}
		tasks = append(tasks, t)
	}

	return tasks, nil
}

// loadLocalTaskFile reads and parses a single local task JSON file.
func loadLocalTaskFile(path string) (Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}

	var lt localTaskJSON
	if err := json.Unmarshal(data, &lt); err != nil {
		return Task{}, err
	}

	return localTaskToTask(lt), nil
}

// localTaskToTask maps the Claude Code local task format to the unified Task model.
func localTaskToTask(lt localTaskJSON) Task {
	return Task{
		ID:        lt.ID,
		Status:    mapLocalStatus(lt.Status),
		Prompt:    lt.Description,
		DependsOn: lt.BlockedBy,
		Location:  LocationLocal,
		// Subject maps to ResultSummary for display in the tasks view.
		ResultSummary: lt.Subject,
	}
}

// mapLocalStatus converts Claude Code's local task statuses to the unified Status type.
func mapLocalStatus(s string) Status {
	switch s {
	case "pending":
		return StatusPending
	case "in_progress":
		return StatusInProgress
	case "completed":
		return StatusCompleted
	case "failed":
		return StatusFailed
	case "claimed":
		return StatusClaimed
	case "dead":
		return StatusDead
	default:
		return StatusPending
	}
}
