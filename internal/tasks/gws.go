package tasks

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

type GWSProvider struct{}

var _ Provider = (*GWSProvider)(nil)

func NewGWSProvider() (*GWSProvider, error) {
	_, err := exec.LookPath("gws")
	if err != nil {
		return nil, fmt.Errorf("gws binary not found in PATH: %w", err)
	}
	return &GWSProvider{}, nil
}

func GWSAvailable() bool {
	_, err := exec.LookPath("gws")
	return err == nil
}

func (p *GWSProvider) ListTaskLists() ([]TaskList, error) {
	out, err := exec.Command("gws", "tasks", "tasklists", "list", "--params", "{}").Output()
	if err != nil {
		return nil, fmt.Errorf("gws tasklists list failed: %w", err)
	}
	return parseTaskListsJSON(out)
}

func (p *GWSProvider) ListTasks(listID string) ([]Task, error) {
	params, _ := json.Marshal(map[string]interface{}{
		"tasklist":      listID,
		"showCompleted": true,
		"showHidden":    true,
	})
	out, err := exec.Command("gws", "tasks", "tasks", "list", "--params", string(params)).Output() // #nosec G204
	if err != nil {
		return nil, fmt.Errorf("gws tasks list failed: %w", err)
	}
	return parseTasksJSON(out)
}

func (p *GWSProvider) CompleteTask(listID, taskID string) error {
	params, _ := json.Marshal(map[string]string{"tasklist": listID, "task": taskID})
	body, _ := json.Marshal(map[string]string{"status": "completed"})
	// #nosec G204
	if err := exec.Command("gws", "tasks", "tasks", "patch", "--params", string(params), "--body", string(body)).Run(); err != nil {
		return fmt.Errorf("gws tasks patch (complete) failed: %w", err)
	}
	return nil
}

func (p *GWSProvider) ReopenTask(listID, taskID string) error {
	params, _ := json.Marshal(map[string]string{"tasklist": listID, "task": taskID})
	body := `{"status":"needsAction","completed":null}`
	// #nosec G204
	if err := exec.Command("gws", "tasks", "tasks", "patch", "--params", string(params), "--body", body).Run(); err != nil {
		return fmt.Errorf("gws tasks patch (reopen) failed: %w", err)
	}
	return nil
}

func (p *GWSProvider) AddNote(listID, taskID, note string) error {
	params, _ := json.Marshal(map[string]string{"tasklist": listID, "task": taskID})
	out, err := exec.Command("gws", "tasks", "tasks", "get", "--params", string(params)).Output() // #nosec G204
	if err != nil {
		return fmt.Errorf("gws tasks get failed: %w", err)
	}

	var task struct {
		Notes string `json:"notes"`
	}
	if err := json.Unmarshal(out, &task); err != nil {
		return fmt.Errorf("failed to parse task: %w", err)
	}

	newNotes := task.Notes
	if newNotes != "" {
		newNotes += "\n"
	}
	newNotes += note

	body, _ := json.Marshal(map[string]string{"notes": newNotes})
	// #nosec G204
	if err := exec.Command("gws", "tasks", "tasks", "patch", "--params", string(params), "--body", string(body)).Run(); err != nil {
		return fmt.Errorf("gws tasks patch (add note) failed: %w", err)
	}
	return nil
}

func parseTaskListsJSON(data []byte) ([]TaskList, error) {
	var resp struct {
		Items []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse tasklists JSON: %w", err)
	}
	lists := make([]TaskList, len(resp.Items))
	for i, item := range resp.Items {
		lists[i] = TaskList{ID: item.ID, Name: item.Title}
	}
	return lists, nil
}

func parseTasksJSON(data []byte) ([]Task, error) {
	var resp struct {
		Items []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Notes   string `json:"notes"`
			Due     string `json:"due"`
			Status  string `json:"status"`
			Updated string `json:"updated"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse tasks JSON: %w", err)
	}
	tasks := make([]Task, len(resp.Items))
	for i, item := range resp.Items {
		tasks[i] = Task{
			ID:      item.ID,
			Title:   item.Title,
			Notes:   item.Notes,
			Due:     item.Due,
			Status:  item.Status,
			Updated: item.Updated,
		}
	}
	return tasks, nil
}
