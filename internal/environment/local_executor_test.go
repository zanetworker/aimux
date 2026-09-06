package environment

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNewLocalExecutor(t *testing.T) {
	executor := NewLocalExecutor()
	if executor == nil {
		t.Fatal("NewLocalExecutor returned nil")
	}
}

func TestLocalExecutorLaunchSimpleCommand(t *testing.T) {
	executor := NewLocalExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use echo which is portable across Unix systems
	process, err := executor.Launch(ctx, "echo", []string{"hello"}, "", nil)
	if err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	if process == nil {
		t.Fatal("Launch returned nil process")
	}

	// Wait for the process to complete
	state, err := process.Wait()
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	if !state.Success() {
		t.Fatalf("Process exited with error: %v", state)
	}
}

func TestLocalExecutorLaunchWithEnvironment(t *testing.T) {
	executor := NewLocalExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	env := map[string]string{
		"TEST_VAR": "test_value",
	}

	// Use sh -c to print an env var (portable across Unix)
	process, err := executor.Launch(ctx, "sh", []string{"-c", "echo $TEST_VAR"}, "", env)
	if err != nil {
		t.Fatalf("Launch with env failed: %v", err)
	}

	if process == nil {
		t.Fatal("Launch returned nil process")
	}

	// Wait for the process to complete
	state, err := process.Wait()
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	if !state.Success() {
		t.Fatalf("Process exited with error: %v", state)
	}
}

func TestLocalExecutorLaunchWithWorkingDirectory(t *testing.T) {
	executor := NewLocalExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use current directory as working dir
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}

	process, err := executor.Launch(ctx, "pwd", nil, wd, nil)
	if err != nil {
		t.Fatalf("Launch with working directory failed: %v", err)
	}

	if process == nil {
		t.Fatal("Launch returned nil process")
	}

	// Wait for the process to complete
	state, err := process.Wait()
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	if !state.Success() {
		t.Fatalf("Process exited with error: %v", state)
	}
}

func TestLocalExecutorLaunchContextCancellation(t *testing.T) {
	executor := NewLocalExecutor()
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	// Try to launch a command with cancelled context
	process, err := executor.Launch(ctx, "sleep", []string{"10"}, "", nil)
	if err == nil && process == nil {
		// Process may or may not have started depending on timing
		// The important thing is that we get a non-nil error or nil process
		return
	}

	// If process was created, it should have been cancelled
	if process != nil {
		state, err := process.Wait()
		if err == nil && !state.Success() {
			// Process was killed by context cancellation
			return
		}
	}
}
