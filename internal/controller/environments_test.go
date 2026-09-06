package controller

import (
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
)

func TestAddEnvironment_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	env := config.EnvironmentConfig{
		Type:    "openshell",
		Gateway: "https://gw.example.com",
		Image:   "quay.io/test:latest",
	}
	if err := AddEnvironment(cfgPath, "staging", env); err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := cfg.Environments["staging"]
	if !ok {
		t.Fatal("environment 'staging' not found after add")
	}
	if got.Type != "openshell" {
		t.Errorf("Type = %q, want openshell", got.Type)
	}
	if got.Gateway != "https://gw.example.com" {
		t.Errorf("Gateway = %q, want https://gw.example.com", got.Gateway)
	}
}

func TestAddEnvironment_InvalidType(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	env := config.EnvironmentConfig{Type: "docker"}
	err := AddEnvironment(cfgPath, "bad", env)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestAddEnvironment_OpenshellRequiresGateway(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	env := config.EnvironmentConfig{Type: "openshell"}
	err := AddEnvironment(cfgPath, "no-gw", env)
	if err == nil {
		t.Fatal("expected error for openshell without gateway")
	}
}

func TestAddEnvironment_K8sRequiresNamespace(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	env := config.EnvironmentConfig{Type: "k8s"}
	err := AddEnvironment(cfgPath, "no-ns", env)
	if err == nil {
		t.Fatal("expected error for k8s without namespace")
	}
}

func TestAddEnvironment_EmptyName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	env := config.EnvironmentConfig{Type: "local"}
	err := AddEnvironment(cfgPath, "", env)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestAddEnvironment_InvalidName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	env := config.EnvironmentConfig{Type: "local"}
	err := AddEnvironment(cfgPath, "bad name", env)
	if err == nil {
		t.Fatal("expected error for name with spaces")
	}
}

func TestRemoveEnvironment_Success(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	env := config.EnvironmentConfig{
		Type:    "openshell",
		Gateway: "https://gw.example.com",
	}
	if err := AddEnvironment(cfgPath, "staging", env); err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}

	if err := RemoveEnvironment(cfgPath, "staging"); err != nil {
		t.Fatalf("RemoveEnvironment: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Environments["staging"]; ok {
		t.Error("environment 'staging' still exists after remove")
	}
}

func TestRemoveEnvironment_CannotRemoveLocal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	err := RemoveEnvironment(cfgPath, "local")
	if err == nil {
		t.Fatal("expected error when removing 'local'")
	}
}

func TestRemoveEnvironment_NotFound(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	err := RemoveEnvironment(cfgPath, "ghost")
	if err == nil {
		t.Fatal("expected error for nonexistent environment")
	}
}

func TestAddRemove_FullCycle(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Add
	if err := AddEnvironment(cfgPath, "test-env", config.EnvironmentConfig{Type: "local"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Verify
	cfg, _ := config.Load(cfgPath)
	if _, ok := cfg.Environments["test-env"]; !ok {
		t.Fatal("test-env not found after add")
	}

	// Remove
	if err := RemoveEnvironment(cfgPath, "test-env"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify gone
	cfg, _ = config.Load(cfgPath)
	if _, ok := cfg.Environments["test-env"]; ok {
		t.Fatal("test-env still exists after remove")
	}
}
