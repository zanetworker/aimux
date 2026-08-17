package main

import (
	"testing"
)

func TestEnvOr(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		def      string
		envVal   string
		expected string
	}{
		{"uses default when unset", "TEST_ENVOR_MISSING_KEY", "fallback", "", "fallback"},
		{"uses env when set", "TEST_ENVOR_SET_KEY", "fallback", "override", "override"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != "" {
				t.Setenv(tt.key, tt.envVal)
			}
			got := envOr(tt.key, tt.def)
			if got != tt.expected {
				t.Errorf("envOr(%q, %q) = %q, want %q", tt.key, tt.def, got, tt.expected)
			}
		})
	}
}
