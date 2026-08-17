package terminal

import (
	"strings"
	"testing"
)

func TestOpenShellConnectArgs(t *testing.T) {
	tests := []struct {
		name     string
		sandbox  string
		gateway  string
		insecure bool
		want     []string
	}{
		{
			name:    "bare sandbox, no gateway",
			sandbox: "ax-cl-1234",
			want:    []string{"sandbox", "connect", "ax-cl-1234"},
		},
		{
			name:    "with gateway endpoint",
			sandbox: "ax-cl-1234",
			gateway: "http://127.0.0.1:8090",
			want:    []string{"sandbox", "connect", "ax-cl-1234", "--gateway-endpoint", "http://127.0.0.1:8090"},
		},
		{
			name:     "insecure gateway",
			sandbox:  "ax-cl-1234",
			gateway:  "https://gw.example.com",
			insecure: true,
			want:     []string{"sandbox", "connect", "ax-cl-1234", "--gateway-endpoint", "https://gw.example.com", "--gateway-insecure"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openshellConnectArgs(tt.sandbox, tt.gateway, tt.insecure)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("openshellConnectArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenShellConnectArgs_EmptySandbox(t *testing.T) {
	// An empty sandbox name should still produce a valid connect command
	// (openshell connect with no name reconnects to the last-used sandbox),
	// but callers should avoid this; assert we don't inject an empty arg.
	got := openshellConnectArgs("", "", false)
	for _, a := range got {
		if a == "" {
			t.Errorf("openshellConnectArgs produced an empty argument: %v", got)
		}
	}
}
