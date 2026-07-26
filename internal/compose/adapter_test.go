package compose

import (
	"strings"
	"testing"
)

func TestOTELSandboxEnv(t *testing.T) {
	env := otelSandboxEnv("http://localhost:4318", "test-session-1")
	if env == nil {
		t.Fatal("expected non-nil env")
	}
	if env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
		t.Errorf("CLAUDE_CODE_ENABLE_TELEMETRY = %q, want 1", env["CLAUDE_CODE_ENABLE_TELEMETRY"])
	}
	if env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" {
		t.Errorf("OTEL_EXPORTER_OTLP_PROTOCOL = %q", env["OTEL_EXPORTER_OTLP_PROTOCOL"])
	}
	endpoint := env["OTEL_EXPORTER_OTLP_ENDPOINT"]
	if endpoint != "http://host.openshell.internal:4318" {
		t.Errorf("endpoint = %q", endpoint)
	}
	logsEndpoint := env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"]
	if !strings.Contains(logsEndpoint, "aimux_session=test-session-1") {
		t.Errorf("logs endpoint missing session ID: %q", logsEndpoint)
	}
	attrs := env["OTEL_RESOURCE_ATTRIBUTES"]
	if !strings.Contains(attrs, "test-session-1") {
		t.Errorf("resource attrs missing session ID: %q", attrs)
	}
}

func TestOTELSandboxEnv_Empty(t *testing.T) {
	env := otelSandboxEnv("", "")
	if env != nil {
		t.Errorf("expected nil env for empty endpoint, got %v", env)
	}
}

func TestOTELHostPort(t *testing.T) {
	tests := []struct {
		endpoint, want string
	}{
		{"http://localhost:4318", "host.openshell.internal:4318"},
		{"http://host.openshell.internal:4318", "host.openshell.internal:4318"},
		{"https://collector.example.com:443", "host.openshell.internal:443"},
		{"", ""},
	}
	for _, tt := range tests {
		got := otelHostPort(tt.endpoint)
		if got != tt.want {
			t.Errorf("otelHostPort(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
	}
}

func TestSandboxSessionName(t *testing.T) {
	tests := []struct {
		provider, sandbox, want string
	}{
		{"claude", "sb-123", "aimux-remote-claude-sb-123"},
		{"codex", "my-box", "aimux-remote-codex-my-box"},
	}
	for _, tt := range tests {
		got := SandboxSessionName(tt.provider, tt.sandbox)
		if got != tt.want {
			t.Errorf("SandboxSessionName(%q, %q) = %q, want %q", tt.provider, tt.sandbox, got, tt.want)
		}
	}
}
