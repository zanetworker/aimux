package environment

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLocal_Exec_EchoHello(t *testing.T) {
	env := NewLocalEnvironment()
	out, err := env.Exec(context.Background(), "", "echo hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hello\n" {
		t.Errorf("got %q, want %q", out, "hello\n")
	}
}

func TestLocal_Exec_FailingCommand(t *testing.T) {
	env := NewLocalEnvironment()
	_, err := env.Exec(context.Background(), "", "exit 1")
	if err == nil {
		t.Fatal("expected error for exit 1")
	}
	if !strings.Contains(err.Error(), "local exec:") {
		t.Errorf("error should contain 'local exec:', got: %v", err)
	}
}

func TestLocal_Exec_OutputOnError(t *testing.T) {
	env := NewLocalEnvironment()
	out, err := env.Exec(context.Background(), "", "echo err >&2; exit 1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(out, "err") {
		t.Errorf("output should contain stderr, got: %q", out)
	}
}

func TestLocal_Exec_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := NewLocalEnvironment()
	_, err := env.Exec(ctx, "", "sleep 10")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestLocal_Exec_ContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	env := NewLocalEnvironment()
	_, err := env.Exec(ctx, "", "sleep 10")
	if err == nil {
		t.Fatal("expected error from timed-out context")
	}
}

func TestOpenShell_ExecArgs(t *testing.T) {
	tests := []struct {
		name     string
		sandbox  string
		gateway  string
		insecure bool
		command  string
		want     []string
	}{
		{
			name:    "basic",
			sandbox: "my-sandbox",
			command: "echo hi",
			want:    []string{"sandbox", "exec", "--name", "my-sandbox", "--", "sh", "-c", "echo hi"},
		},
		{
			name:    "with gateway",
			sandbox: "sb1",
			gateway: "https://gw.example.com",
			command: "ls",
			want:    []string{"sandbox", "exec", "--name", "sb1", "--gateway-endpoint", "https://gw.example.com", "--", "sh", "-c", "ls"},
		},
		{
			name:     "with gateway and insecure",
			sandbox:  "sb2",
			gateway:  "https://gw.example.com",
			insecure: true,
			command:  "whoami",
			want:     []string{"sandbox", "exec", "--name", "sb2", "--gateway-endpoint", "https://gw.example.com", "--gateway-insecure", "--", "sh", "-c", "whoami"},
		},
		{
			name:     "insecure without gateway",
			sandbox:  "sb3",
			insecure: true,
			command:  "pwd",
			want:     []string{"sandbox", "exec", "--name", "sb3", "--gateway-insecure", "--", "sh", "-c", "pwd"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := openshellExecArgs(tc.sandbox, tc.gateway, tc.insecure, tc.command)
			if len(got) != len(tc.want) {
				t.Fatalf("length mismatch: got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("arg[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestK8s_Exec_NotSupported(t *testing.T) {
	env := NewK8sEnvironment("test", K8sEnvironmentConfig{})
	_, err := env.Exec(context.Background(), "sandbox", "echo hi")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("error should mention 'not yet supported', got: %v", err)
	}
}
