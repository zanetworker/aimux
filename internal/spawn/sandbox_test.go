package spawn

import (
	"testing"
)

func TestSandboxSessionName(t *testing.T) {
	tests := []struct {
		provider    string
		sandboxName string
		want        string
	}{
		{"claude", "happy-fox", "aimux-remote-claude-happy-fox"},
		{"codex", "test-123", "aimux-remote-codex-test-123"},
		{"gemini", "sb-abc", "aimux-remote-gemini-sb-abc"},
	}
	for _, tt := range tests {
		got := SandboxSessionName(tt.provider, tt.sandboxName)
		if got != tt.want {
			t.Errorf("SandboxSessionName(%q, %q) = %q, want %q", tt.provider, tt.sandboxName, got, tt.want)
		}
	}
}

func TestOpenshellProviderName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude", "claude"},
		{"codex", "codex"},
		{"gemini", ""},
		{"unknown", ""},
	}
	for _, tt := range tests {
		got := openshellProviderName(tt.input)
		if got != tt.want {
			t.Errorf("openshellProviderName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestOTELSandboxEnv_FullURL(t *testing.T) {
	env := otelSandboxEnv("http://localhost:4318", "test-session")
	if env == nil {
		t.Fatal("expected non-nil env")
	}
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://host.openshell.internal:4318" {
		t.Errorf("endpoint: %q", env["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
	if env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" {
		t.Errorf("protocol: %q", env["OTEL_EXPORTER_OTLP_PROTOCOL"])
	}
	if env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"] != "http://host.openshell.internal:4318/v1/logs" {
		t.Errorf("logs endpoint: %q", env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"])
	}
}

func TestOTELSandboxEnv_CustomPort(t *testing.T) {
	env := otelSandboxEnv("http://localhost:9090", "test-session")
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://host.openshell.internal:9090" {
		t.Errorf("endpoint: %q", env["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
}

func TestOTELSandboxEnv_NoPort(t *testing.T) {
	env := otelSandboxEnv("http://localhost", "test-session")
	if env["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://host.openshell.internal:4318" {
		t.Errorf("endpoint: %q", env["OTEL_EXPORTER_OTLP_ENDPOINT"])
	}
}

func TestOTELSandboxEnv_Empty(t *testing.T) {
	env := otelSandboxEnv("", "test-session")
	if env != nil {
		t.Errorf("expected nil for empty endpoint, got %v", env)
	}
}
