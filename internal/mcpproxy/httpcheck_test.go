package mcpproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckHTTP_401WithChallengeIsNeedsOAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://x/.well-known/oauth"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if got := CheckHTTPServer(context.Background(), srv.URL, nil); got != NeedsOAuth {
		t.Fatalf("got %v", got)
	}
}

func TestCheckHTTP_401WithoutChallengeIsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if got := CheckHTTPServer(context.Background(), srv.URL, nil); got != Reachable {
		t.Fatalf("got %v", got)
	}
}

func TestCheckHTTP_SendsHeadersAndInitialize(t *testing.T) {
	var auth, method, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, method, ctype = r.Header.Get("Authorization"), r.Method, r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	got := CheckHTTPServer(context.Background(), srv.URL, map[string]string{"Authorization": "Bearer k"})
	if got != Reachable || auth != "Bearer k" || method != http.MethodPost || ctype != "application/json" {
		t.Fatalf("got %v auth %q method %q ctype %q", got, auth, method, ctype)
	}
}

func TestCheckHTTP_ClosedPortIsUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	if got := CheckHTTPServer(context.Background(), "http://"+addr+"/mcp", nil); got != Unreachable {
		t.Fatalf("got %v", got)
	}
}

func TestCheckHTTP_SilentServerTimesOut(t *testing.T) {
	old := httpCheckTimeout
	httpCheckTimeout = 50 * time.Millisecond
	t.Cleanup(func() { httpCheckTimeout = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	if got := CheckHTTPServer(context.Background(), srv.URL, nil); got != Unreachable || time.Since(start) > time.Second {
		t.Fatalf("got %v after %v", got, time.Since(start))
	}
}

func TestCheckAll_RunsInParallel(t *testing.T) {
	entries := map[string]HTTPEntry{"a": {URL: "a"}, "b": {URL: "b"}, "c": {URL: "c"}}
	slow := func(context.Context, string, map[string]string) HTTPReach {
		time.Sleep(200 * time.Millisecond)
		return Reachable
	}
	start := time.Now()
	move, kept, _ := checkAll(context.Background(), entries, slow)
	if len(move) != 3 || len(kept) != 0 || time.Since(start) > 450*time.Millisecond {
		t.Fatalf("move %v kept %v took %v", move, kept, time.Since(start))
	}
}

func TestCheckAll_ReasonsAndSplit(t *testing.T) {
	entries := map[string]HTTPEntry{
		"ok":     {URL: "ok"},
		"oauth":  {URL: "oauth"},
		"hdr401": {URL: "hdr401", Headers: map[string]string{"A": "b"}},
		"down":   {URL: "down"},
	}
	check := func(_ context.Context, url string, _ map[string]string) HTTPReach {
		switch url {
		case "oauth", "hdr401":
			return NeedsOAuth
		case "down":
			return Unreachable
		}
		return Reachable
	}
	move, kept, oauth := checkAll(context.Background(), entries, check)
	if len(move) != 1 || move[0] != "ok" {
		t.Fatalf("move %v", move)
	}
	want := map[string]string{
		"oauth":  ReasonNeedsOAuth,
		"hdr401": "the server refused its headers (401)",
		"down":   "not reachable now; run install again when it is up",
	}
	for name, reason := range want {
		if keptReason(kept, name) != reason {
			t.Fatalf("%s: %q (kept %v)", name, keptReason(kept, name), kept)
		}
	}
	if len(oauth) != 1 || oauth[0].Server != "oauth" {
		t.Fatalf("oauth %v", oauth)
	}
}
