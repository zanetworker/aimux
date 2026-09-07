package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/config"
	"gopkg.in/yaml.v3"
)

func TestEnvironmentsView_CursorNavigation(t *testing.T) {
	v := NewEnvironmentsView()
	v.SetEnvironments(map[string]config.EnvironmentConfig{
		"alpha": {Type: "local"},
		"beta":  {Type: "openshell", Gateway: "https://gw"},
		"gamma": {Type: "k8s", Namespace: "agents"},
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
}

func TestEnvironmentsView_AddLocal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	v := NewEnvironmentsView()
	v.SetConfigPath(cfgPath)
	v.SetEnvironments(map[string]config.EnvironmentConfig{})
	v.SetSize(80, 24)

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !v.InForm() {
		t.Fatal("expected form mode after 'a'")
	}

	typeText(&v.formInput, "dev")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if v.formStep != envFormType {
		t.Fatalf("expected envFormType, got %d", v.formStep)
	}

	typeText(&v.formInput, "local")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("form should be closed after local type")
	}
	if !strings.Contains(v.status, "Added environment 'dev'") {
		t.Fatalf("expected success status, got %q", v.status)
	}

	if _, ok := v.envs["dev"]; !ok {
		t.Fatal("environment 'dev' should exist in view data")
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config file should exist: %v", err)
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	envs, ok := raw["environments"]
	if !ok {
		t.Fatal("config should contain environments key")
	}
	envsMap, ok := envs.(map[string]interface{})
	if !ok {
		t.Fatal("environments should be a map")
	}
	if _, ok := envsMap["dev"]; !ok {
		t.Fatal("config should contain 'dev' environment")
	}
}

func TestEnvironmentsView_AddOpenShell(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	v := NewEnvironmentsView()
	v.SetConfigPath(cfgPath)
	v.SetEnvironments(map[string]config.EnvironmentConfig{})

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})

	typeText(&v.formInput, "sandbox")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	typeText(&v.formInput, "openshell")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if v.formStep != envFormGateway {
		t.Fatalf("expected gateway prompt for openshell, got %d", v.formStep)
	}

	typeText(&v.formInput, "https://gw.example.com")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("form should be complete")
	}
	if !strings.Contains(v.status, "Added environment 'sandbox'") {
		t.Fatalf("expected success, got %q", v.status)
	}
	if v.envs["sandbox"].Gateway != "https://gw.example.com" {
		t.Fatalf("expected gateway URL, got %q", v.envs["sandbox"].Gateway)
	}
}

func TestEnvironmentsView_AddK8s(t *testing.T) {
	v := NewEnvironmentsView()
	v.SetEnvironments(map[string]config.EnvironmentConfig{})

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})

	typeText(&v.formInput, "prod")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	typeText(&v.formInput, "k8s")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if v.formStep != envFormNamespace {
		t.Fatalf("expected namespace prompt for k8s, got %d", v.formStep)
	}

	typeText(&v.formInput, "agents")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("form should be complete")
	}
	if v.envs["prod"].Namespace != "agents" {
		t.Fatalf("expected namespace 'agents', got %q", v.envs["prod"].Namespace)
	}
}

func TestEnvironmentsView_RemoveEnvironment(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	initial := map[string]config.EnvironmentConfig{
		"local":   {Type: "local"},
		"sandbox": {Type: "openshell", Gateway: "https://gw"},
	}

	if err := config.AddEnvironmentToConfig(cfgPath, "local", initial["local"]); err != nil {
		t.Fatal(err)
	}
	if err := config.AddEnvironmentToConfig(cfgPath, "sandbox", initial["sandbox"]); err != nil {
		t.Fatal(err)
	}

	v := NewEnvironmentsView()
	v.SetConfigPath(cfgPath)
	v.SetEnvironments(initial)

	// Select 'sandbox' (sorted: local=0, sandbox=1)
	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if v.SelectedName() != "sandbox" {
		t.Fatalf("expected 'sandbox' selected, got %q", v.SelectedName())
	}

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if v.formStep != envFormConfirmRemove {
		t.Fatalf("expected confirm prompt, got %d", v.formStep)
	}

	typeText(&v.formInput, "y")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("form should be closed")
	}
	if !strings.Contains(v.status, "Removed environment 'sandbox'") {
		t.Fatalf("expected remove status, got %q", v.status)
	}
	if _, ok := v.envs["sandbox"]; ok {
		t.Fatal("sandbox should be removed from view data")
	}
	if len(v.names) != 1 {
		t.Fatalf("expected 1 name remaining, got %d", len(v.names))
	}
}

func TestEnvironmentsView_RemoveCancel(t *testing.T) {
	v := NewEnvironmentsView()
	v.SetEnvironments(map[string]config.EnvironmentConfig{
		"keep": {Type: "local"},
	})

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	typeText(&v.formInput, "n")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.status != "Remove cancelled" {
		t.Fatalf("expected cancel status, got %q", v.status)
	}
	if len(v.envs) != 1 {
		t.Fatal("environment should still exist")
	}
}

func TestEnvironmentsView_EscCancelsForm(t *testing.T) {
	v := NewEnvironmentsView()
	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !v.InForm() {
		t.Fatal("expected form mode")
	}

	v.HandleKey(tea.KeyMsg{Type: tea.KeyEscape})
	if v.InForm() {
		t.Fatal("form should be cancelled by Esc")
	}
}

func TestEnvironmentsView_HealthCheckHint(t *testing.T) {
	v := NewEnvironmentsView()
	v.SetEnvironments(map[string]config.EnvironmentConfig{
		"sandbox": {Type: "openshell", Gateway: "https://gw"},
	})

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if !strings.Contains(v.status, "Health check") {
		t.Fatalf("expected health check hint, got %q", v.status)
	}
}

func TestEnvironmentsView_AddInvalidName(t *testing.T) {
	v := NewEnvironmentsView()
	v.SetEnvironments(map[string]config.EnvironmentConfig{})

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	typeText(&v.formInput, "bad name!")
	v.HandleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if v.InForm() {
		t.Fatal("form should close on invalid name")
	}
	if v.status == "" {
		t.Fatal("expected error status")
	}
}

func TestEnvironmentsView_ViewRendersForm(t *testing.T) {
	v := NewEnvironmentsView()
	v.SetSize(80, 24)
	v.SetEnvironments(map[string]config.EnvironmentConfig{})

	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	output := v.View()
	if !strings.Contains(output, "Environment name:") {
		t.Fatal("view should render form prompt")
	}
}

func TestEnvironmentsView_RemoveNoSelection(t *testing.T) {
	v := NewEnvironmentsView()
	v.SetEnvironments(map[string]config.EnvironmentConfig{})
	v.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if v.InForm() {
		t.Fatal("should not enter form when nothing selected")
	}
}
