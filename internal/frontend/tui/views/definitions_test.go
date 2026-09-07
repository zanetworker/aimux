package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/config"
)

func TestDefinitionsView_CursorNavigation(t *testing.T) {
	v := NewDefinitionsView()
	v.SetConfigs([]config.AgentConfig{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	})

	if v.cursor != 0 {
		t.Fatalf("expected cursor 0, got %d", v.cursor)
	}

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if v.cursor != 1 {
		t.Fatalf("expected cursor 1 after j, got %d", v.cursor)
	}

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if v.cursor != 0 {
		t.Fatalf("expected cursor 0 after k, got %d", v.cursor)
	}

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if v.cursor != 0 {
		t.Fatalf("cursor should not go below 0, got %d", v.cursor)
	}
}

func TestDefinitionsView_CreateAgent(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")

	v := NewDefinitionsView()
	v.SetConfigDir(dir)
	v.SetSize(80, 24)

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if !v.InForm() {
		t.Fatal("expected form mode after 'c'")
	}
	if v.formStep != defFormName {
		t.Fatalf("expected defFormName, got %d", v.formStep)
	}

	typeText(&v.formInput, "test-agent")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if v.formStep != defFormModel {
		t.Fatalf("expected defFormModel after name entry, got %d", v.formStep)
	}

	typeText(&v.formInput, "sonnet")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if v.formStep != defFormPrompt {
		t.Fatalf("expected defFormPrompt after model entry, got %d", v.formStep)
	}

	typeText(&v.formInput, "You are a helpful assistant")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("expected form to be complete")
	}

	status := v.status
	if !strings.Contains(status, "Created agent 'test-agent'") {
		t.Fatalf("expected success status, got %q", status)
	}

	_, err := os.Stat(filepath.Join(agentsDir, "test-agent.agent.yaml"))
	if err != nil {
		t.Fatalf("agent file should exist: %v", err)
	}

	if len(v.configs) != 1 {
		t.Fatalf("expected 1 config after create, got %d", len(v.configs))
	}
	if v.configs[0].Name != "test-agent" {
		t.Fatalf("expected config name 'test-agent', got %q", v.configs[0].Name)
	}
}

func TestDefinitionsView_CreateAgent_InvalidName(t *testing.T) {
	v := NewDefinitionsView()
	v.SetConfigDir(t.TempDir())

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})

	typeText(&v.formInput, "bad name with spaces")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("form should close on invalid name")
	}
	if v.status == "" {
		t.Fatal("expected error status for invalid name")
	}
}

func TestDefinitionsView_DeleteAgent(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")

	ac := config.AgentConfig{Name: "doomed", Harness: "claude"}
	if err := config.SaveAgent(agentsDir, ac); err != nil {
		t.Fatal(err)
	}

	configs, _ := config.LoadAgentDir(agentsDir)

	v := NewDefinitionsView()
	v.SetConfigDir(dir)
	v.SetConfigs(configs)

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !v.InForm() {
		t.Fatal("expected confirm prompt after 'd'")
	}
	if v.formStep != defFormConfirmDelete {
		t.Fatalf("expected defFormConfirmDelete, got %d", v.formStep)
	}

	typeText(&v.formInput, "y")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("form should be closed after confirmation")
	}
	if !strings.Contains(v.status, "Deleted agent 'doomed'") {
		t.Fatalf("expected delete status, got %q", v.status)
	}
	if len(v.configs) != 0 {
		t.Fatalf("expected 0 configs after delete, got %d", len(v.configs))
	}

	_, err := os.Stat(filepath.Join(agentsDir, "doomed.agent.yaml"))
	if !os.IsNotExist(err) {
		t.Fatal("agent file should be deleted")
	}
}

func TestDefinitionsView_DeleteCancel(t *testing.T) {
	v := NewDefinitionsView()
	v.SetConfigDir(t.TempDir())
	v.SetConfigs([]config.AgentConfig{{Name: "keep-me", Harness: "claude"}})

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	typeText(&v.formInput, "n")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("form should be closed")
	}
	if v.status != "Delete cancelled" {
		t.Fatalf("expected cancel status, got %q", v.status)
	}
	if len(v.configs) != 1 {
		t.Fatal("config should still exist after cancel")
	}
}

func TestDefinitionsView_EscCancelsForm(t *testing.T) {
	v := NewDefinitionsView()
	v.SetConfigDir(t.TempDir())

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if !v.InForm() {
		t.Fatal("expected form mode")
	}

	v.HandleKey(tea.KeyMsg{Type: tea.KeyEscape})
	if v.InForm() {
		t.Fatal("form should be cancelled by Esc")
	}
}

func TestDefinitionsView_EnterShowsStartHint(t *testing.T) {
	v := NewDefinitionsView()
	v.SetConfigs([]config.AgentConfig{{Name: "reviewer", Harness: "claude"}})

	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(v.status, "aimux sessions start reviewer") {
		t.Fatalf("expected start hint, got %q", v.status)
	}
}

func TestDefinitionsView_ViewRendersForm(t *testing.T) {
	v := NewDefinitionsView()
	v.SetSize(80, 24)

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	output := v.View()
	if !strings.Contains(output, "Agent name:") {
		t.Fatal("view should render form prompt")
	}
}

func TestDefinitionsView_DeleteNoSelection(t *testing.T) {
	v := NewDefinitionsView()
	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if v.InForm() {
		t.Fatal("should not enter form when no agents exist")
	}
}

func typeText(input *TextInput, s string) {
	for _, r := range s {
		input.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}
