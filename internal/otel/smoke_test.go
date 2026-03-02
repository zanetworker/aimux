package otel

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestSmoke_ReceiverListens verifies the receiver actually binds to the port
// and responds to requests. This is a smoke test for the config-driven startup.
func TestSmoke_ReceiverListens(t *testing.T) {
	store := NewSpanStore()
	port := 14321
	receiver := NewReceiver(store, port)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()

	time.Sleep(100 * time.Millisecond)

	// POST with empty protobuf should return 200
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port),
		"application/x-protobuf",
		nil,
	)
	if err != nil {
		t.Fatalf("POST /v1/traces: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST empty body status = %d, want 200", resp.StatusCode)
	}

	// Stop and verify it stops cleanly
	receiver.Stop()
	time.Sleep(50 * time.Millisecond)

	// Should no longer be listening
	_, err = http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port),
		"application/x-protobuf",
		nil,
	)
	if err == nil {
		t.Error("expected connection error after Stop, but request succeeded")
	}
}
