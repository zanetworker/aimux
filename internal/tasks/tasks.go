package tasks

import "strings"

type Task struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Notes    string `json:"notes"`
	Due      string `json:"due"`
	Status   string `json:"status"`
	ListID   string `json:"listID"`
	ListName string `json:"listName"`
	Updated  string `json:"updated"`
}

type TaskList struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Provider interface {
	ListTaskLists() ([]TaskList, error)
	ListTasks(listID string) ([]Task, error)
	CompleteTask(listID, taskID string) error
	ReopenTask(listID, taskID string) error
	AddNote(listID, taskID, note string) error
}

func RenderPrompt(template, title, notes, userPrompt string) string {
	r := strings.NewReplacer(
		"{title}", title,
		"{notes}", notes,
		"{user_prompt}", userPrompt,
	)
	return r.Replace(template)
}
