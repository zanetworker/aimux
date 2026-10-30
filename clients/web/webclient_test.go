package webclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestHandler_ServesTheAppUnderAStrictPolicy(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	resp, body := get(t, srv.URL+"/app/")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(body, `src="app.js"`) {
		t.Fatalf("GET /app/: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
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
	for _, f := range []string{"app.js", "voice.js"} {
		if resp, body := get(t, srv.URL+"/app/"+f); resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || body == "" {
			t.Fatalf("GET /app/%s: %d %q", f, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

func TestHandler_OnlyItsOwnFiles(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	if resp, _ := get(t, srv.URL+"/app/nope.js"); resp.StatusCode != 404 {
		t.Fatalf("unknown file: %d, want 404", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+"/app/../../etc/passwd"); resp.StatusCode == 200 {
		t.Fatal("path outside the app served")
	}
}

func TestHandler_ServesModulesAsJavaScript(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	for _, f := range []string{"now.mjs", "all.mjs", "apiDate.mjs", "nowModel.mjs", "nowView.mjs", "rows.mjs", "tabs.mjs", "timeline.mjs", "chatView.mjs", "sessionInfo.mjs", "terminal.mjs", "markdown.mjs", "references.mjs", "render.mjs", "login.mjs", "chatLoad.mjs", "queue.mjs", "queueView.mjs"} {
		resp, body := get(t, srv.URL+"/app/"+f)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") || body == "" {
			t.Fatalf("GET /app/%s: %d %q", f, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

func TestHandler_DoesNotServeTests(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	for _, f := range []string{"now.test.mjs", "all.test.mjs", "nowModel.test.mjs", "sessionInfo.test.mjs", "terminal.test.mjs", "markdown.test.mjs", "references.test.mjs", "login.test.mjs", "chatLoad.test.mjs", "queue.test.mjs", "queueView.test.mjs", "chatView.test.mjs"} {
		if resp, _ := get(t, srv.URL+"/app/"+f); resp.StatusCode != 404 {
			t.Fatalf("GET /app/%s: %d, want 404", f, resp.StatusCode)
		}
	}
}
