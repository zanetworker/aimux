package tasks

import (
	"testing"
)

func TestMCPParseListTaskListsResult(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    []TaskList
		wantErr bool
	}{
		{
			name: "valid response",
			input: []byte(`{
				"task_lists": [
					{"id": "list1", "title": "Work Tasks"},
					{"id": "list2", "title": "Personal Tasks"}
				]
			}`),
			want: []TaskList{
				{ID: "list1", Name: "Work Tasks"},
				{ID: "list2", Name: "Personal Tasks"},
			},
			wantErr: false,
		},
		{
			name:    "empty task lists",
			input:   []byte(`{"task_lists": []}`),
			want:    []TaskList{},
			wantErr: false,
		},
		{
			name:    "invalid json",
			input:   []byte(`invalid json`),
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMCPTaskLists(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMCPTaskLists() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("parseMCPTaskLists() len = %d, want %d", len(got), len(tt.want))
					return
				}
				for i := range got {
					if got[i].ID != tt.want[i].ID || got[i].Name != tt.want[i].Name {
						t.Errorf("parseMCPTaskLists()[%d] = %+v, want %+v", i, got[i], tt.want[i])
					}
				}
			}
		})
	}
}

func TestMCPParseListTasksResult(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    []Task
		wantErr bool
	}{
		{
			name: "valid response",
			input: []byte(`{
				"tasks": [
					{
						"id": "task1",
						"title": "Review PR",
						"notes": "Check the backend changes",
						"status": "needsAction",
						"due": "2024-01-15T00:00:00Z"
					},
					{
						"id": "task2",
						"title": "Write tests",
						"notes": "",
						"status": "completed",
						"due": ""
					}
				]
			}`),
			want: []Task{
				{
					ID:     "task1",
					Title:  "Review PR",
					Notes:  "Check the backend changes",
					Status: "needsAction",
					Due:    "2024-01-15T00:00:00Z",
				},
				{
					ID:     "task2",
					Title:  "Write tests",
					Notes:  "",
					Status: "completed",
					Due:    "",
				},
			},
			wantErr: false,
		},
		{
			name:    "empty tasks",
			input:   []byte(`{"tasks": []}`),
			want:    []Task{},
			wantErr: false,
		},
		{
			name:    "invalid json",
			input:   []byte(`invalid json`),
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMCPTasks(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMCPTasks() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("parseMCPTasks() len = %d, want %d", len(got), len(tt.want))
					return
				}
				for i := range got {
					if got[i].ID != tt.want[i].ID ||
						got[i].Title != tt.want[i].Title ||
						got[i].Notes != tt.want[i].Notes ||
						got[i].Status != tt.want[i].Status ||
						got[i].Due != tt.want[i].Due {
						t.Errorf("parseMCPTasks()[%d] = %+v, want %+v", i, got[i], tt.want[i])
					}
				}
			}
		})
	}
}

func TestNewMCPProviderRequiresEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{
			name:     "valid endpoint",
			endpoint: "http://localhost:8080/mcp",
			wantErr:  false,
		},
		{
			name:     "empty endpoint",
			endpoint: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewMCPProvider(tt.endpoint)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewMCPProvider() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && provider == nil {
				t.Error("NewMCPProvider() returned nil provider with no error")
			}
			if !tt.wantErr && provider.endpoint != tt.endpoint {
				t.Errorf("NewMCPProvider() endpoint = %s, want %s", provider.endpoint, tt.endpoint)
			}
		})
	}
}
