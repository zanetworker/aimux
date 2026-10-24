// Package remoteapi is aimux's API for clients on other devices (a phone,
// another laptop): list sessions, follow them, and continue idle ones. It is
// meant to sit behind `tailscale serve`, which proves who the caller is.
package remoteapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
)

type loginKey struct{}

// LoginFrom returns the Tailscale login of the caller, set by
// RequireTailnetUser.
func LoginFrom(ctx context.Context) string {
	s, _ := ctx.Value(loginKey{}).(string)
	return s
}

// RequireTailnetUser admits only requests that `tailscale serve` forwarded
// for an allowlisted user:
//   - the connection comes from loopback, where tailscale serve connects from
//     (the listener must also bind to loopback only);
//   - Tailscale-User-Login, which tailscale serve sets and strips from client
//     input, names a login in allowed (an empty list admits nobody);
//   - no Origin header: browsers send one, and no web page may use this API.
func RequireTailnetUser(allowed []string, next http.Handler) http.Handler {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[strings.ToLower(strings.TrimSpace(a))] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fromLoopback(r.RemoteAddr) || r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "forbidden", errors.New("use this API through tailscale serve"))
			return
		}
		login := strings.ToLower(strings.TrimSpace(r.Header.Get("Tailscale-User-Login")))
		if login == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", errors.New("no Tailscale identity on the request"))
			return
		}
		if !ok[login] {
			writeError(w, http.StatusForbidden, "forbidden", errors.New(login+" is not in remote_access.allowed_logins"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), loginKey{}, login)))
	})
}

func fromLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
