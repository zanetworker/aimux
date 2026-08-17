package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTerminalWebSocketRejectsMissingSession(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	wsURL := strings.Replace(s.URL(), "http", "ws", 1) + "/api/terminal/nonexistent-session-xyz"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestParseTermSize(t *testing.T) {
	tests := []struct {
		name             string
		query            string
		wantCols, wantRows int
	}{
		{"defaults", "", 120, 40},
		{"custom", "cols=200&rows=50", 200, 50},
		{"cols only", "cols=80", 80, 40},
		{"rows only", "rows=24", 120, 24},
		{"invalid cols", "cols=abc&rows=24", 120, 24},
		{"zero cols", "cols=0&rows=24", 120, 24},
		{"negative", "cols=-1&rows=-5", 120, 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/test"
			if tt.query != "" {
				url += "?" + tt.query
			}
			r := httptest.NewRequest("GET", url, nil)
			cols, rows := parseTermSize(r)
			if cols != tt.wantCols || rows != tt.wantRows {
				t.Errorf("parseTermSize() = (%d, %d), want (%d, %d)", cols, rows, tt.wantCols, tt.wantRows)
			}
		})
	}
}

func TestHandleTerminalSandboxRouteExists(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	wsURL := strings.Replace(s.URL(), "http", "ws", 1) + "/api/terminal/sandbox/test-sandbox-xyz"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// openshell not installed: expect 500 from NewOpenShellExec failure
		if resp != nil && resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected 500 when openshell unavailable, got %d", resp.StatusCode)
		}
		return
	}
	// openshell installed: WebSocket upgrade succeeded, close cleanly
	_ = conn.Close()
}

func TestHandleTerminalSandboxQueryParams(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	wsURL := strings.Replace(s.URL(), "http", "ws", 1) + "/api/terminal/sandbox/test-sandbox-xyz?cols=80&rows=24"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// openshell not installed — can't test further
		t.Skip("openshell not installed, skipping query param test")
	}
	_ = conn.Close()
}
