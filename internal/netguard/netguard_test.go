package netguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:9090": true, "localhost:1": true, "LOCALHOST": true, "[::1]:80": true, "::1": true,
		"127.1.2.3:5": true, "evil.example:9090": false, "10.0.0.1:80": false, "": false, "[::1": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestLocalOnly(t *testing.T) {
	tests := []struct {
		name, host, origin string
		want               int
	}{
		{"local, no origin", "127.0.0.1:7381", "", http.StatusOK},
		{"local origin other port", "127.0.0.1:7381", "http://localhost:5173", http.StatusOK},
		{"dns rebinding", "evil.example:7381", "", http.StatusForbidden},
		{"cross-site page", "127.0.0.1:7381", "https://evil.example", http.StatusForbidden},
		{"null origin", "127.0.0.1:7381", "null", http.StatusForbidden},
	}
	h := LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/mcp", nil)
			r.Host = tt.host
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("got %d, want %d", w.Code, tt.want)
			}
		})
	}
}
