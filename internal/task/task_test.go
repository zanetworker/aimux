package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- Task model tests ---

func TestTask_IsTerminal(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusPending, false},
		{StatusClaimed, false},
		{StatusInProgress, false},
		{StatusCompleted, true},
		{StatusFailed, true},
		{StatusDead, true},
	}
	for _, tt := range tests {
		task := Task{Status: tt.status}
		if got := task.IsTerminal(); got != tt.want {
			t.Errorf("IsTerminal() for status %q = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestTask_IsActive(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusPending, false},
		{StatusClaimed, true},
		{StatusInProgress, true},
		{StatusCompleted, false},
		{StatusFailed, false},
		{StatusDead, false},
	}
	for _, tt := range tests {
		task := Task{Status: tt.status}
		if got := task.IsActive(); got != tt.want {
			t.Errorf("IsActive() for status %q = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// --- parseRedisFields tests ---

func TestParseRedisFields_AllFields(t *testing.T) {
	fields := map[string]string{
		"status":         "in_progress",
		"prompt":         "Refactor the users endpoint",
		"required_role":  "coder",
		"assignee":       "agent-1",
		"depends_on":     `["task-abc","task-def"]`,
		"result_summary": "Refactored /users, extracted userService",
		"result_ref":     "branch:task-abc123",
		"source_branch":  "task-prev",
		"error":          "",
		"retry_count":    "2",
		"created_at":     "1709654321",
		"completed_at":   "1709654999.5",
	}

	task := parseRedisFields("abc123", fields)

	if task.ID != "abc123" {
		t.Errorf("ID = %q, want %q", task.ID, "abc123")
	}
	if task.Status != StatusInProgress {
		t.Errorf("Status = %q, want %q", task.Status, StatusInProgress)
	}
	if task.Prompt != "Refactor the users endpoint" {
		t.Errorf("Prompt = %q", task.Prompt)
	}
	if task.RequiredRole != "coder" {
		t.Errorf("RequiredRole = %q", task.RequiredRole)
	}
	if task.Assignee != "agent-1" {
		t.Errorf("Assignee = %q", task.Assignee)
	}
	if len(task.DependsOn) != 2 || task.DependsOn[0] != "task-abc" || task.DependsOn[1] != "task-def" {
		t.Errorf("DependsOn = %v", task.DependsOn)
	}
	if task.ResultSummary != "Refactored /users, extracted userService" {
		t.Errorf("ResultSummary = %q", task.ResultSummary)
	}
	if task.ResultRef != "branch:task-abc123" {
		t.Errorf("ResultRef = %q", task.ResultRef)
	}
	if task.SourceBranch != "task-prev" {
		t.Errorf("SourceBranch = %q", task.SourceBranch)
	}
	if task.RetryCount != 2 {
		t.Errorf("RetryCount = %d, want 2", task.RetryCount)
	}
	if task.Location != LocationK8s {
		t.Errorf("Location = %q, want %q", task.Location, LocationK8s)
	}
	if task.CreatedAt.Unix() != 1709654321 {
		t.Errorf("CreatedAt = %v, want unix 1709654321", task.CreatedAt)
	}
	// Float timestamp: 1709654999.5
	if task.CompletedAt.Unix() != 1709654999 {
		t.Errorf("CompletedAt = %v, want unix 1709654999", task.CompletedAt)
	}
}

func TestParseRedisFields_MinimalFields(t *testing.T) {
	fields := map[string]string{
		"status": "pending",
		"prompt": "Do something",
	}

	task := parseRedisFields("minimal", fields)

	if task.ID != "minimal" {
		t.Errorf("ID = %q", task.ID)
	}
	if task.Status != StatusPending {
		t.Errorf("Status = %q", task.Status)
	}
	if task.DependsOn != nil {
		t.Errorf("DependsOn should be nil, got %v", task.DependsOn)
	}
	if task.RetryCount != 0 {
		t.Errorf("RetryCount = %d", task.RetryCount)
	}
	if !task.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be zero, got %v", task.CreatedAt)
	}
}

func TestParseRedisFields_InvalidDependsOn(t *testing.T) {
	fields := map[string]string{
		"status":     "pending",
		"depends_on": "not-valid-json",
	}
	task := parseRedisFields("bad-deps", fields)
	if task.DependsOn != nil {
		t.Errorf("DependsOn should be nil for invalid JSON, got %v", task.DependsOn)
	}
}

func TestParseRedisFields_EmptyDependsOn(t *testing.T) {
	fields := map[string]string{
		"status":     "pending",
		"depends_on": "[]",
	}
	task := parseRedisFields("empty-deps", fields)
	if len(task.DependsOn) != 0 {
		t.Errorf("DependsOn should be empty, got %v", task.DependsOn)
	}
}

func TestParseRedisFields_InvalidRetryCount(t *testing.T) {
	fields := map[string]string{
		"status":      "pending",
		"retry_count": "not-a-number",
	}
	task := parseRedisFields("bad-rc", fields)
	if task.RetryCount != 0 {
		t.Errorf("RetryCount should be 0 for invalid input, got %d", task.RetryCount)
	}
}

// --- parseUnixTimestamp tests ---

func TestParseUnixTimestamp(t *testing.T) {
	tests := []struct {
		input   string
		wantSec int64
	}{
		{"1709654321", 1709654321},
		{"1709654321.5", 1709654321},
		{"0", 0},
	}
	for _, tt := range tests {
		got := parseUnixTimestamp(tt.input)
		if got.Unix() != tt.wantSec {
			t.Errorf("parseUnixTimestamp(%q).Unix() = %d, want %d", tt.input, got.Unix(), tt.wantSec)
		}
	}
}

func TestParseUnixTimestamp_Invalid(t *testing.T) {
	got := parseUnixTimestamp("not-a-timestamp")
	if !got.IsZero() {
		t.Errorf("parseUnixTimestamp(invalid) should return zero time, got %v", got)
	}
}

// --- Local file loader tests ---

func TestLoadFromLocalFiles_BasicTasks(t *testing.T) {
	dir := t.TempDir()

	writeLocalTask(t, dir, "1.json", localTaskJSON{
		ID:          "1",
		Subject:     "Implement feature X",
		Description: "Build the X feature with tests",
		Status:      "completed",
		Blocks:      []string{"2"},
		BlockedBy:   []string{},
	})
	writeLocalTask(t, dir, "2.json", localTaskJSON{
		ID:          "2",
		Subject:     "Review feature X",
		Description: "Review the implementation of X",
		Status:      "in_progress",
		Blocks:      []string{},
		BlockedBy:   []string{"1"},
	})

	tasks, err := LoadFromLocalFiles(dir)
	if err != nil {
		t.Fatalf("LoadFromLocalFiles: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}

	// Files are read in directory order; find task "1"
	var task1, task2 Task
	for _, task := range tasks {
		switch task.ID {
		case "1":
			task1 = task
		case "2":
			task2 = task
		}
	}

	if task1.Status != StatusCompleted {
		t.Errorf("task1.Status = %q, want %q", task1.Status, StatusCompleted)
	}
	if task1.Prompt != "Build the X feature with tests" {
		t.Errorf("task1.Prompt = %q", task1.Prompt)
	}
	if task1.ResultSummary != "Implement feature X" {
		t.Errorf("task1.ResultSummary = %q", task1.ResultSummary)
	}
	if task1.Location != LocationLocal {
		t.Errorf("task1.Location = %q, want %q", task1.Location, LocationLocal)
	}

	if task2.Status != StatusInProgress {
		t.Errorf("task2.Status = %q, want %q", task2.Status, StatusInProgress)
	}
	if len(task2.DependsOn) != 1 || task2.DependsOn[0] != "1" {
		t.Errorf("task2.DependsOn = %v, want [1]", task2.DependsOn)
	}
}

func TestLoadFromLocalFiles_NonexistentDir(t *testing.T) {
	tasks, err := LoadFromLocalFiles("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got: %v", err)
	}
	if tasks != nil {
		t.Errorf("expected nil tasks, got %v", tasks)
	}
}

func TestLoadFromLocalFiles_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	tasks, err := LoadFromLocalFiles(dir)
	if err != nil {
		t.Fatalf("LoadFromLocalFiles: %v", err)
	}
	if tasks != nil {
		t.Errorf("expected nil tasks for empty dir, got %v", tasks)
	}
}

func TestLoadFromLocalFiles_SkipsNonJSON(t *testing.T) {
	dir := t.TempDir()

	writeLocalTask(t, dir, "1.json", localTaskJSON{
		ID:     "1",
		Status: "pending",
	})
	// Write a non-JSON file that should be skipped
	if err := os.WriteFile(filepath.Join(dir, ".lock"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	tasks, err := LoadFromLocalFiles(dir)
	if err != nil {
		t.Fatalf("LoadFromLocalFiles: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("got %d tasks, want 1 (should skip .lock)", len(tasks))
	}
}

func TestLoadFromLocalFiles_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid}"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromLocalFiles(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestLoadFromLocalFiles_SkipsSubdirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	writeLocalTask(t, dir, "1.json", localTaskJSON{
		ID:     "1",
		Status: "pending",
	})

	tasks, err := LoadFromLocalFiles(dir)
	if err != nil {
		t.Fatalf("LoadFromLocalFiles: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("got %d tasks, want 1 (should skip subdirectories)", len(tasks))
	}
}

// --- mapLocalStatus tests ---

func TestMapLocalStatus(t *testing.T) {
	tests := []struct {
		input string
		want  Status
	}{
		{"pending", StatusPending},
		{"in_progress", StatusInProgress},
		{"completed", StatusCompleted},
		{"failed", StatusFailed},
		{"claimed", StatusClaimed},
		{"dead", StatusDead},
		{"unknown_status", StatusPending},
		{"", StatusPending},
	}
	for _, tt := range tests {
		if got := mapLocalStatus(tt.input); got != tt.want {
			t.Errorf("mapLocalStatus(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- localTaskToTask tests ---

func TestLocalTaskToTask(t *testing.T) {
	lt := localTaskJSON{
		ID:          "42",
		Subject:     "Build the widget",
		Description: "Create a new widget with full test coverage",
		ActiveForm:  "Building widget",
		Status:      "in_progress",
		Blocks:      []string{"43", "44"},
		BlockedBy:   []string{"41"},
	}

	task := localTaskToTask(lt)

	if task.ID != "42" {
		t.Errorf("ID = %q", task.ID)
	}
	if task.Status != StatusInProgress {
		t.Errorf("Status = %q", task.Status)
	}
	if task.Prompt != "Create a new widget with full test coverage" {
		t.Errorf("Prompt = %q", task.Prompt)
	}
	if task.ResultSummary != "Build the widget" {
		t.Errorf("ResultSummary = %q", task.ResultSummary)
	}
	if len(task.DependsOn) != 1 || task.DependsOn[0] != "41" {
		t.Errorf("DependsOn = %v", task.DependsOn)
	}
	if task.Location != LocationLocal {
		t.Errorf("Location = %q", task.Location)
	}
	// These fields are not set by local tasks
	if task.RequiredRole != "" {
		t.Errorf("RequiredRole should be empty, got %q", task.RequiredRole)
	}
	if task.Assignee != "" {
		t.Errorf("Assignee should be empty, got %q", task.Assignee)
	}
	if !task.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be zero for local tasks")
	}
}

// --- Status constant tests ---

func TestStatusConstants(t *testing.T) {
	// Verify status string values match Redis field values exactly.
	if StatusPending != "pending" {
		t.Error("StatusPending mismatch")
	}
	if StatusClaimed != "claimed" {
		t.Error("StatusClaimed mismatch")
	}
	if StatusInProgress != "in_progress" {
		t.Error("StatusInProgress mismatch")
	}
	if StatusCompleted != "completed" {
		t.Error("StatusCompleted mismatch")
	}
	if StatusFailed != "failed" {
		t.Error("StatusFailed mismatch")
	}
	if StatusDead != "dead" {
		t.Error("StatusDead mismatch")
	}
}

func TestLocationConstants(t *testing.T) {
	if LocationLocal != "local" {
		t.Error("LocationLocal mismatch")
	}
	if LocationK8s != "k8s" {
		t.Error("LocationK8s mismatch")
	}
}

// --- parseUnixTimestamp float precision ---

func TestParseUnixTimestamp_FloatPrecision(t *testing.T) {
	// Python time.time() produces floats like "1709654321.123456"
	ts := parseUnixTimestamp("1709654321.123456")
	if ts.Unix() != 1709654321 {
		t.Errorf("seconds = %d, want 1709654321", ts.Unix())
	}
	// Subsecond precision should be preserved
	nsec := ts.Nanosecond()
	if nsec < 123000000 || nsec > 124000000 {
		t.Errorf("nanoseconds = %d, want ~123456000", nsec)
	}
}

// --- Helper ---

func writeLocalTask(t *testing.T, dir, filename string, lt localTaskJSON) {
	t.Helper()
	data, err := json.Marshal(lt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// --- Zero-value task tests ---

func TestTask_ZeroValue(t *testing.T) {
	var task Task
	if task.IsTerminal() {
		t.Error("zero-value task should not be terminal")
	}
	if task.IsActive() {
		t.Error("zero-value task should not be active")
	}
	if task.CreatedAt != (time.Time{}) {
		t.Error("zero-value CreatedAt should be zero time")
	}
}
