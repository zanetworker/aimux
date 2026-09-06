package sdk

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/environment"
)

func TestSDKSession_FullLoop(t *testing.T) {
	binary := buildMockSDK(t)
	env := environment.NewLocalEnvironment()

	ctx := context.Background()
	sess, err := NewSDKSession(ctx, binary, env, "")
	if err != nil {
		t.Fatalf("NewSDKSession: %v", err)
	}
	defer sess.Stop()

	output, err := sess.Run(ctx, "echo sdk-mode-works")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The mock SDK uses the prompt as a bash command.
	// Dispatcher routes it to LocalEnvironment.Exec → "sh -c echo sdk-mode-works".
	// Mock SDK receives the tool_result and sends the output as text + completion.
	if output == "" {
		t.Error("expected non-empty output")
	}
	if !strings.Contains(output, "sdk-mode-works") {
		t.Errorf("output should contain command output; got %q", output)
	}
}

func TestSDKSession_SubprocessError(t *testing.T) {
	binary := buildMockSDK(t)
	env := environment.NewLocalEnvironment()

	ctx := context.Background()
	sess, err := NewSDKSession(ctx, binary, env, "")
	if err != nil {
		t.Fatalf("NewSDKSession: %v", err)
	}
	defer sess.Stop()

	// Send a malformed JSON line directly to trigger an error event from mock-sdk.
	// The mock-sdk returns an error event for unmarshal failures.
	// Instead, we test by sending a valid prompt, then immediately stopping the
	// subprocess, which causes a receive error (EOF).
	// A cleaner approach: use the mock's error path. But the mock only returns
	// error for unmarshal issues. Let's test the context-cancel path instead.
	cancelCtx, cancel := context.WithCancel(ctx)

	sess2, err := NewSDKSession(cancelCtx, binary, env, "")
	if err != nil {
		t.Fatalf("NewSDKSession: %v", err)
	}
	defer sess2.Stop()

	// Cancel the context, which kills the subprocess.
	cancel()

	// Give the subprocess a moment to die.
	time.Sleep(50 * time.Millisecond)

	_, err = sess2.Run(context.Background(), "test")
	if err == nil {
		t.Error("expected error after context cancel, got nil")
	}
}

func TestSDKSession_StopCleanup(t *testing.T) {
	binary := buildMockSDK(t)
	env := environment.NewLocalEnvironment()

	ctx := context.Background()
	sess, err := NewSDKSession(ctx, binary, env, "")
	if err != nil {
		t.Fatalf("NewSDKSession: %v", err)
	}

	if err := sess.Stop(); err != nil {
		t.Errorf("Stop returned error: %v", err)
	}
}

func TestSDKSession_InvalidBinary(t *testing.T) {
	env := environment.NewLocalEnvironment()
	ctx := context.Background()

	_, err := NewSDKSession(ctx, "/nonexistent/binary", env, "")
	if err == nil {
		t.Fatal("expected error for nonexistent binary, got nil")
	}
	if !strings.Contains(err.Error(), "sdk session") {
		t.Errorf("error should mention sdk session; got %q", err.Error())
	}
}

func TestSDKSession_CollectedOutput(t *testing.T) {
	s := &SDKSession{}
	if got := s.collectedOutput(); got != "" {
		t.Errorf("empty session should return empty string; got %q", got)
	}

	s.output = []string{"hello ", "world"}
	if got := s.collectedOutput(); got != "hello world" {
		t.Errorf("expected 'hello world'; got %q", got)
	}
}
