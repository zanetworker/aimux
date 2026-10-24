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
			h := RequireTailnetUser(tt.allowed, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = LoginFrom(r.Context())
			}))
			r := httptest.NewRequest("GET", "/v1/sessions", nil)
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
