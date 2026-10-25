package remoteapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireTailnetUser(t *testing.T) {
	const me = "adel@example.com"
	tests := []struct {
		name    string
		allowed []string
		login   string
		remote  string
		origin  string
		want    int
	}{
		{"allowlisted user via tailscale serve", []string{me}, me, "127.0.0.1:50000", "", http.StatusOK},
		{"login matching is case-insensitive", []string{me}, "Adel@Example.com", "[::1]:50000", "", http.StatusOK},
		{"no identity header", []string{me}, "", "127.0.0.1:50000", "", http.StatusUnauthorized},
		{"someone else on the tailnet", []string{me}, "guest@example.com", "127.0.0.1:50000", "", http.StatusForbidden},
		{"empty allowlist fails closed", nil, me, "127.0.0.1:50000", "", http.StatusForbidden},
		{"not through tailscale serve", []string{me}, me, "100.64.1.2:50000", "", http.StatusForbidden},
		{"browser page, even with the header", []string{me}, me, "127.0.0.1:50000", "https://evil.example", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			h := RequireTailnetUser(tt.allowed, []string{"mac.tail1.ts.net"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = LoginFrom(r.Context())
			}))
			r := httptest.NewRequest("GET", "/v1/sessions", nil)
			r.Host = "mac.tail1.ts.net:8443"
			r.RemoteAddr = tt.remote
			if tt.login != "" {
				r.Header.Set("Tailscale-User-Login", tt.login)
			}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status %d, want %d (body %q)", w.Code, tt.want, w.Body.String())
			}
			if tt.want != http.StatusOK {
				var body struct{ Error, Code string }
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code == "" || body.Error == "" {
					t.Fatalf("error body %q is not JSON with error and code", w.Body.String())
				}
			}
			if tt.want == http.StatusOK && seen == "" {
				t.Fatal("handler ran without the caller's login in the context")
			}
		})
	}
}

// A DNS-rebinding page reaches the loopback listener with its own Host, no
// Origin on a same-origin GET, and any header it likes.
func TestRequireTailnetUser_RejectsUntrustedHost(t *testing.T) {
	h := RequireTailnetUser([]string{"me@example.com"}, []string{"mac.tail1.ts.net"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, host := range []string{"evil.example:7382", "127.0.0.1:7382", "localhost:7382", "mac.tail1.ts.net.evil.example", ""} {
		r := httptest.NewRequest("GET", "/v1/sessions", nil)
		r.Host = host
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("Tailscale-User-Login", "me@example.com")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, w.Code)
		}
	}
	for _, host := range []string{"mac.tail1.ts.net", "MAC.tail1.ts.net:8443", "mac.tail1.ts.net."} {
		r := httptest.NewRequest("GET", "/v1/sessions", nil)
		r.Host = host
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("Tailscale-User-Login", "me@example.com")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("trusted Host %q: status %d, want 200", host, w.Code)
		}
	}
}

func TestRequireTailnetUser_NoTrustedHostsAdmitsNobody(t *testing.T) {
	h := RequireTailnetUser([]string{"me@example.com"}, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest("GET", "/v1/sessions", nil)
	r.Host = "mac.tail1.ts.net"
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Tailscale-User-Login", "me@example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 with no trusted hosts", w.Code)
	}
}

// The phone web client at /app calls the API from its own origin; every
// other origin, and any cross-site request, stays out.
func TestRequireTailnetUser_SameOriginPage(t *testing.T) {
	const me = "adel@example.com"
	tests := []struct {
		name, method, origin, site string
		want                       int
	}{
		{"page POST from its own origin", "POST", "https://mac.tail1.ts.net:8443", "same-origin", http.StatusOK},
		{"page GET from its own origin", "GET", "", "same-origin", http.StatusOK},
		{"address typed into the browser", "GET", "", "none", http.StatusOK},
		{"another port on the same host", "POST", "https://mac.tail1.ts.net:9999", "same-site", http.StatusForbidden},
		{"plain http origin", "POST", "http://mac.tail1.ts.net:8443", "", http.StatusForbidden},
		{"another site", "POST", "https://evil.example", "cross-site", http.StatusForbidden},
		{"cross-site GET without Origin (img, script tag)", "GET", "", "cross-site", http.StatusForbidden},
		{"same-site but other origin, GET", "GET", "", "same-site", http.StatusForbidden},
		{"opaque origin", "POST", "null", "cross-site", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := RequireTailnetUser([]string{me}, []string{"mac.tail1.ts.net"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			r := httptest.NewRequest(tt.method, "/v1/sessions", nil)
			r.Host = "mac.tail1.ts.net:8443"
			r.RemoteAddr = "127.0.0.1:50000"
			r.Header.Set("Tailscale-User-Login", me)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.site != "" {
				r.Header.Set("Sec-Fetch-Site", tt.site)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("status %d, want %d (body %q)", w.Code, tt.want, w.Body.String())
			}
		})
	}
}
