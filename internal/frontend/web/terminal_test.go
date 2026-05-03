package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTerminalWebSocketRejectsMissingSession(t *testing.T) {
	s := NewServer(0)
	go s.Start()
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
