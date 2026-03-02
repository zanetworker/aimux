package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

// Suppress unused variable warnings for time import.
var _ = time.Now

// Verify Gemini implements the Provider interface at compile time.
var _ Provider = (*Gemini)(nil)

func TestGeminiName(t *testing.T) {
	g := &Gemini{}
	if got := g.Name(); got != "gemini" {
		t.Errorf("Gemini.Name() = %q, want %q", got, "gemini")
	}
}

func TestGeminiDiscover(t *testing.T) {
	g := &Gemini{}
	_, err := g.Discover()
	if err != nil {
		t.Errorf("Gemini.Discover() error = %v, want nil", err)
	}
	// Result depends on running processes — just verify no error
}

func TestGeminiResumeCommand(t *testing.T) {
	g := &Gemini{}
	cmd := g.ResumeCommand(agent.Agent{SessionID: "test", WorkingDir: "/tmp"})
	if cmd == nil {
		t.Fatal("Gemini.ResumeCommand() returned nil, want non-nil")
	}
	assertArgPresent(t, cmd.Args, "--resume")
	assertArgPresent(t, cmd.Args, "latest")
}

func TestGeminiCanEmbed(t *testing.T) {
	g := &Gemini{}
	if g.CanEmbed() {
		t.Error("Gemini.CanEmbed() = true, want false")
	}
}

func TestGeminiDiscover_NoError(t *testing.T) {
	g := &Gemini{}
	_, err := g.Discover()
	if err != nil {
		t.Errorf("Gemini.Discover() error = %v, want nil", err)
	}
}

func TestGeminiResumeCommand_WithWorkingDir(t *testing.T) {
	g := &Gemini{}
	cmd := g.ResumeCommand(agent.Agent{WorkingDir: "/tmp/project"})
	if cmd == nil {
		t.Fatal("ResumeCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--resume")
	assertArgPresent(t, cmd.Args, "latest")
	if cmd.Dir != "/tmp/project" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/project")
	}
}

func TestGeminiResumeCommand_NoWorkingDir(t *testing.T) {
	g := &Gemini{}
	cmd := g.ResumeCommand(agent.Agent{})
	if cmd != nil {
		t.Errorf("ResumeCommand with no WorkingDir should return nil, got %v", cmd)
	}
}

func TestGeminiFindSessionFile_NoWorkingDir(t *testing.T) {
	g := &Gemini{}
	a := agent.Agent{SessionID: "test"}
	if got := g.FindSessionFile(a); got != "" {
		t.Errorf("Gemini.FindSessionFile(no WorkingDir) = %q, want empty", got)
	}
}

func TestGeminiRecentDirs_NoPanic(t *testing.T) {
	g := &Gemini{}
	_ = g.RecentDirs(5)
}

func TestGeminiSpawnCommand_Default(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	if cmd.Dir != "/tmp/myproject" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/myproject")
	}
	if base := filepath.Base(cmd.Args[0]); base != "gemini" {
		t.Errorf("binary = %q, want %q", base, "gemini")
	}
	if len(cmd.Args) != 1 {
		t.Errorf("Args = %v, want 1 element (binary only)", cmd.Args)
	}
}

func TestGeminiSpawnCommand_DefaultModelSkipped(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "default", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgAbsent(t, cmd.Args, "--model")
}

func TestGeminiSpawnCommand_WithModel(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "gemini-2.5-pro", "")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "gemini-2.5-pro")
}

func TestGeminiSpawnCommand_Yolo(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "", "yolo")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgPresent(t, cmd.Args, "--yolo")
}

func TestGeminiSpawnCommand_Plan(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "", "plan")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--approval-mode", "plan")
}

func TestGeminiSpawnCommand_ModelAndYolo(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/myproject", "gemini-3-pro", "yolo")
	if cmd == nil {
		t.Fatal("SpawnCommand returned nil")
	}
	assertArgsContain(t, cmd.Args, "--model", "gemini-3-pro")
	assertArgPresent(t, cmd.Args, "--yolo")
}

func TestGeminiSpawnArgs(t *testing.T) {
	g := &Gemini{}
	sa := g.SpawnArgs()

	expectedModels := []string{"default", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-3-pro", "gemini-3.1-flash"}
	if len(sa.Models) != len(expectedModels) {
		t.Fatalf("SpawnArgs.Models = %v, want %v", sa.Models, expectedModels)
	}
	for i, m := range expectedModels {
		if sa.Models[i] != m {
			t.Errorf("SpawnArgs.Models[%d] = %q, want %q", i, sa.Models[i], m)
		}
	}

	expectedModes := []string{"default", "yolo", "auto_edit", "plan", "sandbox"}
	if len(sa.Modes) != len(expectedModes) {
		t.Fatalf("SpawnArgs.Modes = %v, want %v", sa.Modes, expectedModes)
	}
	for i, m := range expectedModes {
		if sa.Modes[i] != m {
			t.Errorf("SpawnArgs.Modes[%d] = %q, want %q", i, sa.Modes[i], m)
		}
	}
}

func TestGeminiExtractFlag(t *testing.T) {
	if got := geminiExtractFlag("gemini --model gemini-2.5-pro --yolo", "--model"); got != "gemini-2.5-pro" {
		t.Errorf("geminiExtractFlag(--model) = %q, want %q", got, "gemini-2.5-pro")
	}
	if got := geminiExtractFlag("gemini --yolo", "--model"); got != "" {
		t.Errorf("geminiExtractFlag(missing) = %q, want empty", got)
	}
}

func TestIsGeminiProcess(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 /opt/homebrew/bin/gemini", true},
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 node /path/to/gemini --model x", true},
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 grep gemini", false},
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 aimux gemini", false},
		{"user  123 0.0 0.1 1234 5678 ?? S  10:00AM 0:01.23 /usr/bin/python3 something", false},
	}
	for _, tt := range tests {
		if got := isGeminiProcess(tt.line); got != tt.want {
			t.Errorf("isGeminiProcess(%q) = %v, want %v", tt.line[:40], got, tt.want)
		}
	}
}

func TestNewestSessionJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Empty dir
	path, mod := newestSessionJSON(tmpDir)
	if path != "" {
		t.Errorf("newestSessionJSON(empty) = %q, want empty", path)
	}
	if !mod.IsZero() {
		t.Errorf("newestSessionJSON(empty) time should be zero")
	}

	// Create session files
	s1 := `{"sessionId":"aaa","startTime":"2026-02-28T09:00:00Z","lastUpdated":"2026-02-28T09:10:00Z","messages":[]}`
	s2 := `{"sessionId":"bbb","startTime":"2026-02-28T10:00:00Z","lastUpdated":"2026-02-28T10:30:00Z","messages":[]}`

	f1 := filepath.Join(tmpDir, "session-2026-02-28T09-00-aaa.json")
	f2 := filepath.Join(tmpDir, "session-2026-02-28T10-00-bbb.json")
	os.WriteFile(f1, []byte(s1), 0o644)
	os.WriteFile(f2, []byte(s2), 0o644)
	// Ensure f2 has a newer mod time (filesystem resolution may be 1s on Linux)
	past := time.Now().Add(-10 * time.Second)
	os.Chtimes(f1, past, past)
	// f2 keeps current mod time (newer)

	path, mod = newestSessionJSON(tmpDir)
	if path == "" {
		t.Fatal("newestSessionJSON should find a session")
	}
	if !strings.Contains(path, "bbb") {
		t.Errorf("expected newest session (bbb), got %q", filepath.Base(path))
	}
	if mod.IsZero() {
		t.Error("mod time should not be zero")
	}
}

// --- Gemini ParseTrace tests ---

func TestGeminiParseTrace_BasicTurn(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "logs.json")

	data := `[
		{"sessionId":"s1","messageId":1,"type":"user","message":"hello gemini","timestamp":"2026-01-01T10:00:00Z"},
		{"sessionId":"s1","messageId":2,"type":"model","message":"Hi there! How can I help?","timestamp":"2026-01-01T10:00:05Z"}
	]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("ParseTrace returned %d turns, want 1", len(turns))
	}

	turn := turns[0]
	if turn.Number != 1 {
		t.Errorf("Number = %d, want 1", turn.Number)
	}
	if len(turn.UserLines) == 0 || turn.UserLines[0] != "hello gemini" {
		t.Errorf("UserLines = %v, want [\"hello gemini\"]", turn.UserLines)
	}
	if len(turn.OutputLines) == 0 || turn.OutputLines[0] != "Hi there! How can I help?" {
		t.Errorf("OutputLines = %v, want [\"Hi there! How can I help?\"]", turn.OutputLines)
	}
}

func TestGeminiParseTrace_MultipleTurns(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "logs.json")

	data := `[
		{"sessionId":"s1","messageId":1,"type":"user","message":"first question","timestamp":"2026-01-01T10:00:00Z"},
		{"sessionId":"s1","messageId":2,"type":"model","message":"first answer","timestamp":"2026-01-01T10:00:05Z"},
		{"sessionId":"s1","messageId":3,"type":"user","message":"second question","timestamp":"2026-01-01T10:01:00Z"},
		{"sessionId":"s1","messageId":4,"type":"model","message":"second answer","timestamp":"2026-01-01T10:01:05Z"}
	]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "first question" {
		t.Errorf("turn 1 UserLines[0] = %q, want %q", turns[0].UserLines[0], "first question")
	}
	if turns[1].UserLines[0] != "second question" {
		t.Errorf("turn 2 UserLines[0] = %q, want %q", turns[1].UserLines[0], "second question")
	}
}

func TestGeminiParseTrace_InfoMessage(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "logs.json")

	data := `[
		{"sessionId":"s1","messageId":1,"type":"user","message":"what is the status?","timestamp":"2026-01-01T10:00:00Z"},
		{"sessionId":"s1","messageId":2,"type":"info","message":"session started","timestamp":"2026-01-01T10:00:01Z"}
	]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if len(turns[0].OutputLines) == 0 {
		t.Fatal("expected info message in OutputLines")
	}
	if !strings.Contains(turns[0].OutputLines[0], "[info]") {
		t.Errorf("OutputLines[0] = %q, want to contain [info]", turns[0].OutputLines[0])
	}
}

func TestGeminiParseTrace_MissingFile(t *testing.T) {
	g := &Gemini{}
	_, err := g.ParseTrace("/nonexistent/path/logs.json")
	if err == nil {
		t.Error("ParseTrace on missing file should return error")
	}
}

func TestGeminiParseTrace_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace should not error on invalid JSON, got: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("expected 0 turns for invalid JSON, got %d", len(turns))
	}
}

func TestGeminiParseTrace_FiltersToLatestSession(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "logs.json")

	// Simulate append-only logs.json with entries from multiple sessions.
	// Only the latest session (s3) should appear in the parsed output.
	data := `[
		{"sessionId":"s1","messageId":1,"type":"user","message":"old question 1","timestamp":"2026-01-01T10:00:00Z"},
		{"sessionId":"s1","messageId":2,"type":"model","message":"old answer 1","timestamp":"2026-01-01T10:00:05Z"},
		{"sessionId":"s2","messageId":1,"type":"user","message":"old question 2","timestamp":"2026-01-02T10:00:00Z"},
		{"sessionId":"s2","messageId":2,"type":"model","message":"old answer 2","timestamp":"2026-01-02T10:00:05Z"},
		{"sessionId":"s3","messageId":1,"type":"user","message":"current question","timestamp":"2026-01-03T10:00:00Z"},
		{"sessionId":"s3","messageId":2,"type":"model","message":"current answer","timestamp":"2026-01-03T10:00:05Z"}
	]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn (from latest session), got %d", len(turns))
	}
	if turns[0].UserLines[0] != "current question" {
		t.Errorf("UserLines[0] = %q, want %q", turns[0].UserLines[0], "current question")
	}
	if turns[0].OutputLines[0] != "current answer" {
		t.Errorf("OutputLines[0] = %q, want %q", turns[0].OutputLines[0], "current answer")
	}
}

func TestGeminiParseTrace_NoSessionID_ReturnsAll(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "logs.json")

	// Entries without sessionId should all be returned (backwards compat).
	data := `[
		{"messageId":1,"type":"user","message":"q1","timestamp":"2026-01-01T10:00:00Z"},
		{"messageId":2,"type":"model","message":"a1","timestamp":"2026-01-01T10:00:05Z"},
		{"messageId":3,"type":"user","message":"q2","timestamp":"2026-01-01T10:01:00Z"}
	]`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 2 {
		t.Fatalf("expected 2 turns (no filtering), got %d", len(turns))
	}
}

func TestFilterLatestSession(t *testing.T) {
	tests := []struct {
		name    string
		entries []geminiLogEntry
		wantLen int
		wantSID string
	}{
		{
			name:    "empty",
			entries: nil,
			wantLen: 0,
		},
		{
			name: "single session",
			entries: []geminiLogEntry{
				{SessionID: "s1", Timestamp: "2026-01-01T10:00:00Z"},
				{SessionID: "s1", Timestamp: "2026-01-01T10:01:00Z"},
			},
			wantLen: 2,
			wantSID: "s1",
		},
		{
			name: "picks latest by timestamp",
			entries: []geminiLogEntry{
				{SessionID: "s1", Timestamp: "2026-01-01T10:00:00Z"},
				{SessionID: "s2", Timestamp: "2026-01-02T10:00:00Z"},
				{SessionID: "s1", Timestamp: "2026-01-01T11:00:00Z"},
			},
			wantLen: 1,
			wantSID: "s2",
		},
		{
			name: "no session IDs returns all",
			entries: []geminiLogEntry{
				{Timestamp: "2026-01-01T10:00:00Z"},
				{Timestamp: "2026-01-02T10:00:00Z"},
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterLatestSession(tt.entries)
			if len(got) != tt.wantLen {
				t.Errorf("filterLatestSession returned %d entries, want %d", len(got), tt.wantLen)
			}
			if tt.wantSID != "" {
				for _, e := range got {
					if e.SessionID != tt.wantSID {
						t.Errorf("entry has sessionId %q, want %q", e.SessionID, tt.wantSID)
					}
				}
			}
		})
	}
}

func TestGeminiParseTrace_SessionFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session-2026-01-01T10-00-abc123.json")

	data := `{
		"sessionId": "abc123",
		"startTime": "2026-01-01T10:00:00Z",
		"lastUpdated": "2026-01-01T10:01:00Z",
		"messages": [
			{
				"timestamp": "2026-01-01T10:00:00Z",
				"type": "user",
				"content": [{"text": "hello from session file"}]
			},
			{
				"timestamp": "2026-01-01T10:00:30Z",
				"type": "gemini",
				"content": "Hi! I'm ready to help."
			}
		]
	}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "hello from session file" {
		t.Errorf("UserLines[0] = %q, want %q", turns[0].UserLines[0], "hello from session file")
	}
	if len(turns[0].OutputLines) == 0 {
		t.Fatal("expected output lines from gemini response")
	}
	if turns[0].OutputLines[0] != "Hi! I'm ready to help." {
		t.Errorf("OutputLines[0] = %q, want %q", turns[0].OutputLines[0], "Hi! I'm ready to help.")
	}
}

func TestGeminiParseTrace_SessionFileMultipleTurns(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.json")

	data := `{
		"sessionId": "s1",
		"messages": [
			{"timestamp": "2026-01-01T10:00:00Z", "type": "user", "content": [{"text": "first"}]},
			{"timestamp": "2026-01-01T10:00:05Z", "type": "gemini", "content": "reply one"},
			{"timestamp": "2026-01-01T10:01:00Z", "type": "user", "content": [{"text": "second"}]},
			{"timestamp": "2026-01-01T10:01:05Z", "type": "gemini", "content": "reply two"}
		]
	}`

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &Gemini{}
	turns, err := g.ParseTrace(path)
	if err != nil {
		t.Fatalf("ParseTrace error: %v", err)
	}

	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "first" {
		t.Errorf("turn 1 UserLines[0] = %q, want %q", turns[0].UserLines[0], "first")
	}
	if turns[1].UserLines[0] != "second" {
		t.Errorf("turn 2 UserLines[0] = %q, want %q", turns[1].UserLines[0], "second")
	}
}

func TestParseGeminiSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.json")

	data := `{"sessionId": "abc-123-def", "messages": []}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	id := parseGeminiSessionID(path)
	if id != "abc-123-def" {
		t.Errorf("parseGeminiSessionID = %q, want %q", id, "abc-123-def")
	}
}

// --- Gemini new mode tests ---

func TestGeminiSpawnCommand_AutoEdit(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/p", "", "auto_edit")
	assertArgsContain(t, cmd.Args, "--approval-mode", "auto_edit")
}

func TestGeminiSpawnCommand_Sandbox(t *testing.T) {
	g := &Gemini{}
	cmd := g.SpawnCommand("/tmp/p", "", "sandbox")
	assertArgPresent(t, cmd.Args, "--sandbox")
}

func TestGeminiOTELEnv(t *testing.T) {
	g := &Gemini{}
	env := g.OTELEnv("http://localhost:4318")

	required := []string{
		"GEMINI_CLI_TELEMETRY_ENABLED=true",
		"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318",
		"OTEL_LOGS_EXPORTER=otlp",
	}
	for _, r := range required {
		if !strings.Contains(env, r) {
			t.Errorf("OTELEnv missing %q, got:\n%s", r, env)
		}
	}
}
