package tasks

import (
	"os/exec"
	"strings"
	"testing"
)

func TestNewProviderGWS(t *testing.T) {
	// Only run this test if gws is available
	if !GWSAvailable() {
		t.Skip("gws binary not available, skipping test")
	}

	p, err := NewProvider("gws", "")
	if err != nil {
		t.Fatalf("NewProvider(gws) failed: %v", err)
	}

	if _, ok := p.(*GWSProvider); !ok {
		t.Errorf("Expected *GWSProvider, got %T", p)
	}
}

func TestNewProviderMCPRequiresEndpoint(t *testing.T) {
	_, err := NewProvider("mcp", "")
	if err == nil {
		t.Fatal("Expected error when MCP endpoint is empty, got nil")
	}

	if !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("Expected 'cannot be empty' error, got: %v", err)
	}
}

func TestNewProviderMCPWithEndpoint(t *testing.T) {
	endpoint := "http://localhost:8080"
	p, err := NewProvider("mcp", endpoint)
	if err != nil {
		t.Fatalf("NewProvider(mcp) with endpoint failed: %v", err)
	}

	if _, ok := p.(*MCPProvider); !ok {
		t.Errorf("Expected *MCPProvider, got %T", p)
	}
}

func TestNewProviderUnknown(t *testing.T) {
	_, err := NewProvider("unknown-backend", "")
	if err == nil {
		t.Fatal("Expected error for unknown backend, got nil")
	}

	if !strings.Contains(err.Error(), "unknown backend") {
		t.Errorf("Expected 'unknown backend' error, got: %v", err)
	}
}

func TestNewProviderAutoFallback(t *testing.T) {
	// Temporarily hide gws binary if it exists
	gwsPath, _ := exec.LookPath("gws")
	hasGWS := gwsPath != ""

	tests := []struct {
		name        string
		endpoint    string
		wantErr     bool
		wantType    string
		skipIfNoGWS bool
	}{
		{
			name:        "auto with gws available",
			endpoint:    "",
			wantErr:     false,
			wantType:    "*tasks.GWSProvider",
			skipIfNoGWS: true,
		},
		{
			name:     "auto with mcp endpoint (no gws)",
			endpoint: "http://localhost:8080",
			wantErr:  false,
			wantType: "*tasks.MCPProvider",
		},
		{
			name:     "auto with no backend available",
			endpoint: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip tests that require gws if it's not available
			if tt.skipIfNoGWS && !hasGWS {
				t.Skip("gws not available, skipping test")
			}

			// Skip the gws test if gws is available but we're testing the no-gws case
			if tt.name == "auto with mcp endpoint (no gws)" && hasGWS {
				t.Skip("gws is available, cannot test mcp fallback")
			}

			// Skip the error test if gws is available
			if tt.name == "auto with no backend available" && hasGWS {
				t.Skip("gws is available, cannot test no-backend case")
			}

			p, err := NewProvider("auto", tt.endpoint)

			if tt.wantErr {
				if err == nil {
					t.Fatal("Expected error, got nil")
				}
				if !strings.Contains(err.Error(), "no backend available") {
					t.Errorf("Expected 'no backend available' error, got: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("NewProvider(auto) failed: %v", err)
			}

			// Simple type check based on interface implementation
			switch tt.wantType {
			case "*tasks.GWSProvider":
				if _, ok := p.(*GWSProvider); !ok {
					t.Errorf("Expected *GWSProvider, got %T", p)
				}
			case "*tasks.MCPProvider":
				if _, ok := p.(*MCPProvider); !ok {
					t.Errorf("Expected *MCPProvider, got %T", p)
				}
			}
		})
	}
}

func TestNewProviderEmptyBackendDefaultsToAuto(t *testing.T) {
	// Empty backend should behave like "auto"
	// This test will pass/fail based on system state, similar to auto tests
	if !GWSAvailable() {
		t.Skip("gws not available, cannot test auto behavior")
	}

	p, err := NewProvider("", "")
	if err != nil {
		t.Fatalf("NewProvider with empty backend failed: %v", err)
	}

	if _, ok := p.(*GWSProvider); !ok {
		t.Errorf("Expected *GWSProvider for auto-detect with gws available, got %T", p)
	}
}
