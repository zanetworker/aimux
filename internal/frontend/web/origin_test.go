package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestCheckLocalOrigin(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"same origin 127.0.0.1", "127.0.0.1:9090", "http://127.0.0.1:9090", true},
		{"same origin localhost", "localhost:9090", "http://localhost:9090", true},
		{"same origin ipv6 loopback", "[::1]:9090", "http://[::1]:9090", true},
		{"no origin from local client", "127.0.0.1:9090", "", true},
		{"foreign site", "127.0.0.1:9090", "https://evil.example", false},
		{"dns rebinding: foreign host equals origin", "evil.example:9090", "http://evil.example:9090", false},
		{"no origin but foreign host", "evil.example:9090", "", false},
		{"other local port", "127.0.0.1:9090", "http://127.0.0.1:5173", false},
		{"opaque null origin", "127.0.0.1:9090", "null", false},
		{"malformed origin", "127.0.0.1:9090", "://bad", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/terminal/x", nil)
			r.Host = tt.host
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if got := checkLocalOrigin(r); got != tt.want {
				t.Errorf("checkLocalOrigin(host=%q, origin=%q) = %v, want %v", tt.host, tt.origin, got, tt.want)
			}
		})
	}
}

func TestTerminalUpgraderRejectsForeignOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"https://evil.example"}})
	if err == nil {
		t.Fatal("foreign origin upgrade succeeded, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: got response %v, want 403", resp)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {srv.URL}})
	if err != nil {
		t.Fatalf("same origin upgrade failed: %v", err)
	}
	_ = conn.Close()
}

func TestAllowLocalRequest(t *testing.T) {
	tests := []struct {
		name   string
		method string
		host   string
		origin string
		want   bool
	}{
		{"dashboard GET", "GET", "127.0.0.1:9090", "", true},
		{"dashboard POST same origin", "POST", "127.0.0.1:9090", "http://127.0.0.1:9090", true},
		{"vite dev server POST", "POST", "127.0.0.1:3000", "http://localhost:5173", true},
		{"local CLI POST without origin", "POST", "localhost:9090", "", true},
		{"cross-site POST", "POST", "127.0.0.1:9090", "https://evil.example", false},
		{"cross-site DELETE", "DELETE", "127.0.0.1:9090", "https://evil.example", false},
		{"cross-site POST with null origin", "POST", "127.0.0.1:9090", "null", false},
		{"dns rebinding GET", "GET", "evil.example:9090", "", false},
		{"dns rebinding POST", "POST", "evil.example:9090", "http://evil.example:9090", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/api/agents/launch", nil)
			r.Host = tt.host
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if got := allowLocalRequest(r); got != tt.want {
				t.Errorf("allowLocalRequest(%s host=%q origin=%q) = %v, want %v", tt.method, tt.host, tt.origin, got, tt.want)
			}
		})
	}
}

func TestLocalOnlyMiddleware(t *testing.T) {
	called := false
	h := localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	r := httptest.NewRequest("POST", "/api/agents/launch", strings.NewReader(`{}`))
	r.Host = "127.0.0.1:9090"
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || called {
		t.Fatalf("cross-site POST: code=%d called=%v, want 403 and handler not called", w.Code, called)
	}

	r = httptest.NewRequest("GET", "/api/health", nil)
	r.Host = "127.0.0.1:9090"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called {
		t.Fatalf("local GET: handler not called (code=%d)", w.Code)
	}
}

func TestIsAllowedSessionFile(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	if err := os.MkdirAll(filepath.Join(projects, "p"), 0o750); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(projects, "p", "s.jsonl")
	if err := os.WriteFile(inside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(projects, "p", "link.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	roots := []string{projects}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"session file inside root", inside, true},
		{"not yet created file inside root", filepath.Join(projects, "p", "new.jsonl"), true},
		{"file outside root", outside, false},
		{"traversal out of root", filepath.Join(projects, "p", "..", "..", "secret.json"), false},
		{"symlink inside root pointing outside", link, false},
		{"wrong extension inside root", filepath.Join(projects, "p", "s.txt"), false},
		{"root prefix trick", projects + "-evil/s.jsonl", false},
		{"relative path", "p/s.jsonl", false},
		{"empty path", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAllowedSessionFile(tt.path, roots); got != tt.want {
				t.Errorf("isAllowedSessionFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestFastTraceRejectsFileOutsideSessionRoots(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("GET", "/api/trace?file=/etc/aimux-test.json", nil)
	w := httptest.NewRecorder()
	s.handleFastTrace(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("trace outside session roots: code=%d, want 403", w.Code)
	}
}

// K8s sessions use "k8s://<id>" identifiers that the K8s provider resolves
// through Redis; the local-file allowlist must not reject them.
func TestExportAcceptsK8sIdentifiers(t *testing.T) {
	s := &Server{}
	for _, path := range []string{"/api/sessions/abc/export/jsonl", "/api/sessions/abc/export/otel"} {
		r := httptest.NewRequest("POST", path+"?file=k8s://abc&provider=k8s", nil)
		w := httptest.NewRecorder()
		if strings.HasSuffix(path, "jsonl") {
			s.handleExportJSONL(w, r)
		} else {
			s.handleExportOTEL(w, r)
		}
		if w.Code == http.StatusForbidden {
			t.Fatalf("%s: k8s identifier rejected with 403", path)
		}
	}
	// A k8s:// identifier claimed for a local provider is still refused.
	r := httptest.NewRequest("POST", "/api/sessions/abc/export/jsonl?file=k8s://abc&provider=claude", nil)
	w := httptest.NewRecorder()
	s.handleExportJSONL(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("k8s:// with provider claude: %d, want 403", w.Code)
	}
}
