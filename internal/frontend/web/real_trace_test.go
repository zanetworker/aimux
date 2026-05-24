package web

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/trace"
)

// fixtureDir returns the absolute path to the project's testdata/ directory.
func fixtureDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile = .../internal/frontend/web/real_trace_test.go
	// testdata is at the project root: ../../../testdata/
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "testdata")
}

// TestRealTrace_FastEndpoint boots a real server with a real Claude parser
// and verifies that GET /api/trace?file=<fixture> returns parsed turns with
// real user text, tool actions, and token counts from the fixture file.
func TestRealTrace_FastEndpoint(t *testing.T) {
	fixturePath := filepath.Join(fixtureDir(), "sample_session.jsonl")

	// Use a real Claude provider for trace parsing.
	claudeProvider := &provider.Claude{}

	s := NewServer(0)
	s.SetConfig(config.Default())
	s.SetDiscoverFunc(func() ([]agent.Agent, error) { return nil, nil })
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		if name == "claude" {
			return claudeProvider
		}
		return nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(200 * time.Millisecond)

	addr := s.URL()

	resp, err := http.Get(addr + "/api/trace?file=" + fixturePath + "&provider=claude") // #nosec G107
	if err != nil {
		t.Fatalf("GET /api/trace: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !json.Valid(body) {
		t.Fatalf("response is not valid JSON: %.200s", body)
	}

	var result struct {
		Turns []struct {
			Number     int    `json:"number"`
			UserText   string `json:"userText"`
			OutputText string `json:"outputText"`
			TokensIn   int64  `json:"tokensIn"`
			TokensOut  int64  `json:"tokensOut"`
			Model      string `json:"model"`
			Actions    []struct {
				Name      string `json:"name"`
				FilePath  string `json:"filePath"`
				OldString string `json:"oldString"`
				NewString string `json:"newString"`
				Content   string `json:"content"`
			} `json:"actions"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// The fixture has user messages: "hello", "fix the bug", "edit the main.go file..."
	// which produce at least 3 turns (user->assistant pairs).
	if len(result.Turns) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result.Turns))
	}

	// Verify at least one turn has user text.
	hasUserText := false
	for _, turn := range result.Turns {
		if turn.UserText != "" {
			hasUserText = true
			break
		}
	}
	if !hasUserText {
		t.Error("no turn has userText; expected at least one from the fixture")
	}

	// Verify at least one turn has tool actions (Edit or Write from the fixture).
	hasToolAction := false
	for _, turn := range result.Turns {
		if len(turn.Actions) > 0 {
			hasToolAction = true
			break
		}
	}
	if !hasToolAction {
		t.Error("no turn has tool actions; expected Edit/Write from the fixture")
	}

	// Verify the Edit action details are populated.
	foundEdit := false
	foundWrite := false
	for _, turn := range result.Turns {
		for _, action := range turn.Actions {
			if action.Name == "Edit" {
				foundEdit = true
				if action.FilePath == "" {
					t.Error("Edit action has empty filePath")
				}
				if action.OldString == "" {
					t.Error("Edit action has empty oldString")
				}
				if action.NewString == "" {
					t.Error("Edit action has empty newString")
				}
			}
			if action.Name == "Write" {
				foundWrite = true
				if action.FilePath == "" {
					t.Error("Write action has empty filePath")
				}
				if action.Content == "" {
					t.Error("Write action has empty content")
				}
			}
		}
	}
	if !foundEdit {
		t.Error("expected an Edit tool action in the parsed turns")
	}
	if !foundWrite {
		t.Error("expected a Write tool action in the parsed turns")
	}

	// Verify token counts are non-zero (fixture has usage data).
	hasTokens := false
	for _, turn := range result.Turns {
		if turn.TokensIn > 0 || turn.TokensOut > 0 {
			hasTokens = true
			break
		}
	}
	if !hasTokens {
		t.Error("no turn has non-zero token counts; fixture includes usage data")
	}

	// Verify model is populated.
	hasModel := false
	for _, turn := range result.Turns {
		if turn.Model != "" {
			hasModel = true
			break
		}
	}
	if !hasModel {
		t.Error("no turn has a model name; fixture includes claude-opus-4-6")
	}
}

// TestRealTrace_FastEndpoint_MissingFile verifies the server returns an error
// for a nonexistent file path (not a crash or 503).
func TestRealTrace_FastEndpoint_MissingFile(t *testing.T) {
	claudeProvider := &provider.Claude{}

	s := NewServer(0)
	s.SetConfig(config.Default())
	s.SetDiscoverFunc(func() ([]agent.Agent, error) { return nil, nil })
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		if name == "claude" {
			return claudeProvider
		}
		return nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(200 * time.Millisecond)

	addr := s.URL()

	resp, err := http.Get(addr + "/api/trace?file=/nonexistent/path.jsonl&provider=claude") // #nosec G107
	if err != nil {
		t.Fatalf("GET /api/trace: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 for missing file, got %d", resp.StatusCode)
	}
}

// TestRealTrace_FastEndpoint_UnknownProvider verifies 500 for a provider
// that the lookup function returns nil for.
func TestRealTrace_FastEndpoint_UnknownProvider(t *testing.T) {
	s := NewServer(0)
	s.SetConfig(config.Default())
	s.SetDiscoverFunc(func() ([]agent.Agent, error) { return nil, nil })
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		return nil // unknown provider
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(200 * time.Millisecond)

	addr := s.URL()

	resp, err := http.Get(addr + "/api/trace?file=/some/file.jsonl&provider=unknown") // #nosec G107
	if err != nil {
		t.Fatalf("GET /api/trace: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 for unknown provider, got %d", resp.StatusCode)
	}
}
