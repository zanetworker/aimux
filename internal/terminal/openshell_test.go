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
			want:    []string{"sandbox", "exec", "--name", "ax-cl-1234", "--tty", "--", "bash", "-l"},
		},
		{
			name:    "with gateway endpoint",
			sandbox: "ax-cl-1234",
			gateway: "http://127.0.0.1:8090",
			want:    []string{"sandbox", "exec", "--name", "ax-cl-1234", "--tty", "--gateway-endpoint", "http://127.0.0.1:8090", "--", "bash", "-l"},
		},
		{
			name:     "insecure gateway",
			sandbox:  "ax-cl-1234",
			gateway:  "https://gw.example.com",
			insecure: true,
			want:     []string{"sandbox", "exec", "--name", "ax-cl-1234", "--tty", "--gateway-endpoint", "https://gw.example.com", "--gateway-insecure", "--", "bash", "-l"},
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
	got := openshellConnectArgs("", "", false)
	// With exec mode, --name "" produces an empty arg which callers should avoid.
	// Verify the structure is correct (sandbox exec --name <empty> --tty -- bash -l).
	if len(got) < 3 || got[0] != "sandbox" || got[1] != "exec" {
		t.Errorf("unexpected args structure: %v", got)
	}
}
