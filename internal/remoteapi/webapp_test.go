package remoteapi

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func get(t *testing.T, url string, login bool) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if login {
		req.Header.Set("Tailscale-User-Login", testLogin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestWebApp_ServedBehindTheGate(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	resp, body := get(t, srv.URL+"/app/", true)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, `src="app.js"`) {
		t.Fatalf("GET /app/: %d %q, body has app.js=%v", resp.StatusCode, resp.Header.Get("Content-Type"), strings.Contains(body, "app.js"))
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q lacks %q", csp, want)
		}
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("no nosniff header")
	}
	if resp, body := get(t, srv.URL+"/app/app.js", true); resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || body == "" {
		t.Fatalf("GET /app/app.js: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := get(t, srv.URL+"/app/", false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("page without a Tailscale identity: %d, want 401", resp.StatusCode)
	}
}

func TestWebApp_BarePathRedirects(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", srv.URL+"/app", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 3 || resp.Header.Get("Location") != "/app/" {
		t.Fatalf("GET /app: %d to %q, want a redirect to /app/", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestWebApp_UnknownFileIs404(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	if resp, _ := get(t, srv.URL+"/app/../../etc/passwd", true); resp.StatusCode == 200 {
		t.Fatal("path outside the app served")
	}
	if resp, _ := get(t, srv.URL+"/app/nope.js", true); resp.StatusCode != 404 {
		t.Fatalf("unknown file: %d, want 404", resp.StatusCode)
	}
}
