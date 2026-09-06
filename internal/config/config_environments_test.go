package config

import (
	"os"
	"testing"
)

func TestEnvironmentsDefault(t *testing.T) {
	cfg := Default()
	if cfg.Environments == nil {
		t.Fatal("Environments map is nil")
	}
	if len(cfg.Environments) != 1 {
		t.Fatalf("Expected 1 default environment, got %d", len(cfg.Environments))
	}
	local, ok := cfg.Environments["local"]
	if !ok {
		t.Fatal("Expected 'local' environment in default config")
	}
	if local.Type != "local" {
		t.Fatalf("Expected local.Type='local', got '%s'", local.Type)
	}
}

func TestEnvironmentsLoadFromYAML(t *testing.T) {
	yaml := `
environments:
  local:
    type: local
  openshell:
    type: openshell
    gateway: "https://my-gateway.example.com"
    insecure: false
    image: "my-image:latest"
  k8s:
    type: k8s
    redis_url: "redis://localhost:6379"
    namespace: "agents"
    kubeconfig: "/home/user/.kube/config"
`
	tmpfile, err := os.CreateTemp("", "config_*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Environments) != 3 {
		t.Fatalf("Expected 3 environments, got %d", len(cfg.Environments))
	}

	// Check local
	local, ok := cfg.Environments["local"]
	if !ok {
		t.Fatal("Missing 'local' environment")
	}
	if local.Type != "local" {
		t.Fatalf("Expected local.Type='local', got '%s'", local.Type)
	}

	// Check openshell
	os, ok := cfg.Environments["openshell"]
	if !ok {
		t.Fatal("Missing 'openshell' environment")
	}
	if os.Type != "openshell" {
		t.Fatalf("Expected openshell.Type='openshell', got '%s'", os.Type)
	}
	if os.Gateway != "https://my-gateway.example.com" {
		t.Fatalf("Expected openshell.Gateway='https://my-gateway.example.com', got '%s'", os.Gateway)
	}
	if os.Image != "my-image:latest" {
		t.Fatalf("Expected openshell.Image='my-image:latest', got '%s'", os.Image)
	}
	if os.Insecure {
		t.Fatalf("Expected openshell.Insecure=false, got true")
	}

	// Check k8s
	k8s, ok := cfg.Environments["k8s"]
	if !ok {
		t.Fatal("Missing 'k8s' environment")
	}
	if k8s.Type != "k8s" {
		t.Fatalf("Expected k8s.Type='k8s', got '%s'", k8s.Type)
	}
	if k8s.RedisURL != "redis://localhost:6379" {
		t.Fatalf("Expected k8s.RedisURL='redis://localhost:6379', got '%s'", k8s.RedisURL)
	}
	if k8s.Namespace != "agents" {
		t.Fatalf("Expected k8s.Namespace='agents', got '%s'", k8s.Namespace)
	}
	if k8s.Kubeconfig != "/home/user/.kube/config" {
		t.Fatalf("Expected k8s.Kubeconfig='/home/user/.kube/config', got '%s'", k8s.Kubeconfig)
	}
}

func TestEnvironmentsBackwardCompatOpenShell(t *testing.T) {
	yaml := `
remote:
  backend: openshell
  gateway: "https://legacy-gateway.example.com"
  image: "legacy-image:v1"
`
	tmpfile, err := os.CreateTemp("", "config_*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatal(err)
	}

	// Should have default local + auto-created openshell from legacy config
	if len(cfg.Environments) != 2 {
		t.Fatalf("Expected 2 environments (local + openshell), got %d", len(cfg.Environments))
	}

	local, ok := cfg.Environments["local"]
	if !ok {
		t.Fatal("Missing 'local' environment")
	}
	if local.Type != "local" {
		t.Fatalf("Expected local.Type='local', got '%s'", local.Type)
	}

	os, ok := cfg.Environments["openshell"]
	if !ok {
		t.Fatal("Missing 'openshell' environment created from legacy config")
	}
	if os.Type != "openshell" {
		t.Fatalf("Expected openshell.Type='openshell', got '%s'", os.Type)
	}
	if os.Gateway != "https://legacy-gateway.example.com" {
		t.Fatalf("Expected openshell.Gateway='https://legacy-gateway.example.com', got '%s'", os.Gateway)
	}
	if os.Image != "legacy-image:v1" {
		t.Fatalf("Expected openshell.Image='legacy-image:v1', got '%s'", os.Image)
	}
}

func TestEnvironmentsBackwardCompatK8s(t *testing.T) {
	yaml := `
remote:
  backend: k8s
kubernetes:
  enabled: true
  redis_url: "redis://legacy-redis:6379"
  namespace: "legacy-agents"
  kubeconfig: "/etc/kube/config"
`
	tmpfile, err := os.CreateTemp("", "config_*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatal(err)
	}

	// Should have default local + auto-created k8s from legacy config
	if len(cfg.Environments) != 2 {
		t.Fatalf("Expected 2 environments (local + k8s), got %d", len(cfg.Environments))
	}

	k8s, ok := cfg.Environments["k8s"]
	if !ok {
		t.Fatal("Missing 'k8s' environment created from legacy config")
	}
	if k8s.Type != "k8s" {
		t.Fatalf("Expected k8s.Type='k8s', got '%s'", k8s.Type)
	}
	if k8s.RedisURL != "redis://legacy-redis:6379" {
		t.Fatalf("Expected k8s.RedisURL='redis://legacy-redis:6379', got '%s'", k8s.RedisURL)
	}
	if k8s.Namespace != "legacy-agents" {
		t.Fatalf("Expected k8s.Namespace='legacy-agents', got '%s'", k8s.Namespace)
	}
	if k8s.Kubeconfig != "/etc/kube/config" {
		t.Fatalf("Expected k8s.Kubeconfig='/etc/kube/config', got '%s'", k8s.Kubeconfig)
	}
}

func TestEnvironmentsNoBackwardCompatIfExplicitEnvironmentsPresent(t *testing.T) {
	yaml := `
remote:
  backend: openshell
  gateway: "https://should-be-ignored.example.com"
environments:
  local:
    type: local
  custom:
    type: openshell
    gateway: "https://explicit-gateway.example.com"
`
	tmpfile, err := os.CreateTemp("", "config_*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatal(err)
	}

	// Should have only what's in explicit environments section
	if len(cfg.Environments) != 2 {
		t.Fatalf("Expected 2 environments, got %d", len(cfg.Environments))
	}

	custom, ok := cfg.Environments["custom"]
	if !ok {
		t.Fatal("Missing 'custom' environment")
	}
	if custom.Gateway != "https://explicit-gateway.example.com" {
		t.Fatalf("Expected custom.Gateway='https://explicit-gateway.example.com', got '%s'", custom.Gateway)
	}
}
