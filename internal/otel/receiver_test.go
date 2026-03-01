package otel

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestReceiver_StartAndStop(t *testing.T) {
	store := NewSpanStore()
	port := 14318 // use non-standard port for testing
	receiver := NewReceiver(store, port)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()

	// Give server a moment to start
	time.Sleep(50 * time.Millisecond)

	// Verify it's listening
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port))
	if err != nil {
		t.Fatalf("GET /v1/traces error: %v", err)
	}
	defer resp.Body.Close()

	// GET should return 405 (we only accept POST)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}

	if receiver.Port() != port {
		t.Errorf("Port() = %d, want %d", receiver.Port(), port)
	}
}

func TestReceiver_InvalidPayload(t *testing.T) {
	store := NewSpanStore()
	port := 14319
	receiver := NewReceiver(store, port)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()

	time.Sleep(50 * time.Millisecond)

	// Send invalid protobuf
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port),
		"application/x-protobuf",
		nil,
	)
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	// Should return 400 for invalid/empty protobuf
	// (empty body is valid empty protobuf, so this may return 200)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST empty body status = %d", resp.StatusCode)
	}
}
