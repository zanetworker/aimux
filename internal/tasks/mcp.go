package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// MCPProvider implements the Provider interface using an MCP server endpoint
type MCPProvider struct {
	endpoint string
	client   *http.Client
}

// NewMCPProvider creates a new MCP provider
func NewMCPProvider(endpoint string) (*MCPProvider, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("MCP endpoint cannot be empty")
	}
	return &MCPProvider{
		endpoint: endpoint,
		client:   &http.Client{},
	}, nil
}

// mcpRequest represents a JSON-RPC request to the MCP server
type mcpRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      int                    `json:"id"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params"`
}

// mcpResponse represents a JSON-RPC response from the MCP server
type mcpResponse struct {
	Result *mcpResult      `json:"result,omitempty"`
	Error  *mcpError       `json:"error,omitempty"`
}

type mcpResult struct {
	Content []mcpContent `json:"content"`
}

type mcpContent struct {
	Text string `json:"text"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// callTool sends a JSON-RPC request to the MCP server
func (m *MCPProvider) callTool(toolName string, args map[string]interface{}) (json.RawMessage, error) {
	reqBody := mcpRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := m.client.Post(m.endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var mcpResp mcpResponse
	if err := json.Unmarshal(respBody, &mcpResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if mcpResp.Error != nil {
		return nil, fmt.Errorf("MCP error: %s", mcpResp.Error.Message)
	}

	if mcpResp.Result == nil || len(mcpResp.Result.Content) == 0 {
		return nil, fmt.Errorf("empty response from MCP server")
	}

	// Extract the text field from the first content item
	text := mcpResp.Result.Content[0].Text
	return json.RawMessage(text), nil
}

// parseMCPTaskLists parses the task lists response from MCP
func parseMCPTaskLists(data []byte) ([]TaskList, error) {
	var response struct {
		TaskLists []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"task_lists"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("failed to parse task lists: %w", err)
	}

	lists := make([]TaskList, len(response.TaskLists))
	for i, tl := range response.TaskLists {
		lists[i] = TaskList{
			ID:   tl.ID,
			Name: tl.Title,
		}
	}
	return lists, nil
}

// parseMCPTasks parses the tasks response from MCP
func parseMCPTasks(data []byte) ([]Task, error) {
	var response struct {
		Tasks []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Notes  string `json:"notes"`
			Status string `json:"status"`
			Due    string `json:"due"`
		} `json:"tasks"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("failed to parse tasks: %w", err)
	}

	tasks := make([]Task, len(response.Tasks))
	for i, t := range response.Tasks {
		tasks[i] = Task{
			ID:     t.ID,
			Title:  t.Title,
			Notes:  t.Notes,
			Status: t.Status,
			Due:    t.Due,
		}
	}
	return tasks, nil
}

// ListTaskLists returns all task lists
func (m *MCPProvider) ListTaskLists() ([]TaskList, error) {
	args := map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
	}

	data, err := m.callTool("list_task_lists", args)
	if err != nil {
		return nil, err
	}

	return parseMCPTaskLists(data)
}

// ListTasks returns all tasks in a task list
func (m *MCPProvider) ListTasks(listID string) ([]Task, error) {
	args := map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
		"task_list_id":      listID,
	}

	data, err := m.callTool("list_tasks", args)
	if err != nil {
		return nil, err
	}

	return parseMCPTasks(data)
}

// CompleteTask marks a task as completed
func (m *MCPProvider) CompleteTask(listID, taskID string) error {
	args := map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
		"task_list_id":      listID,
		"task_id":           taskID,
		"action":            "update",
		"status":            "completed",
	}

	_, err := m.callTool("manage_task", args)
	return err
}

// ReopenTask reopens a completed task
func (m *MCPProvider) ReopenTask(listID, taskID string) error {
	args := map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
		"task_list_id":      listID,
		"task_id":           taskID,
		"action":            "update",
		"status":            "needsAction",
	}

	_, err := m.callTool("manage_task", args)
	return err
}

// AddNote adds a note to a task
func (m *MCPProvider) AddNote(listID, taskID, note string) error {
	args := map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
		"task_list_id":      listID,
		"task_id":           taskID,
		"action":            "update",
		"notes":             note,
	}

	_, err := m.callTool("manage_task", args)
	return err
}

// Compile-time check that MCPProvider implements Provider
var _ Provider = (*MCPProvider)(nil)
