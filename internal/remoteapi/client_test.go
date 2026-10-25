package remoteapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aimuxapi "github.com/zanetworker/aimux/api"
)

// A browser client is any handler the server is given: served at /app/,
// behind the same identity check as /v1. The server knows nothing else
// about it.
func TestAPI_MountsAClientBehindTheIdentityCheck(t *testing.T) {
	var seen string
	client := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		_, _ = io.WriteString(w, "client")
	})
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Client: client}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()

	if resp, body := get(t, srv.URL+"/app/index.html", true); resp.StatusCode != 200 || body != "client" || seen != "/app/index.html" {
		t.Fatalf("client: %d %q (saw %q)", resp.StatusCode, body, seen)
	}
	if resp, _ := get(t, srv.URL+"/app/", false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("client without identity: %d, want 401", resp.StatusCode)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", srv.URL+"/app", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 3 || resp.Header.Get("Location") != "/app/" {
		t.Fatalf("GET /app: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestAPI_NoClientNoApp(t *testing.T) {
	api := &API{Broker: NewBroker(BrokerDeps{}, BrokerOptions{})}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	if resp, _ := get(t, srv.URL+"/app/", true); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/app/ with no client: %d, want 404", resp.StatusCode)
	}
}

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

// The server hands out its own contract, so a client (or a code generator)
// gets the exact spec of the server it talks to.
func TestAPI_ServesItsContract(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	resp, body := get(t, srv.URL+"/v1/openapi.yaml", true)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "yaml") {
		t.Fatalf("GET /v1/openapi.yaml: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if body != string(aimuxapi.RemoteV1) {
		t.Fatal("served contract differs from the embedded spec")
	}
	if resp, _ := get(t, srv.URL+"/v1/openapi.yaml", false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without identity: %d, want 401", resp.StatusCode)
	}
}
