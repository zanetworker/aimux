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
//   - the Host is one of trustedHosts, this machine's tailnet names, which
//     tailscale serve passes through. A DNS-rebinding page reaches loopback
//     with its own Host (and may set any header), so loopback alone is not
//     proof of proxying. An empty list admits nobody;
//   - the connection comes from loopback, where tailscale serve connects from
//     (the listener must also bind to loopback only);
//   - Tailscale-User-Login, which tailscale serve sets and strips from client
//     input, names a login in allowed (an empty list admits nobody);
//   - no web page but aimux's own: a request with an Origin must come from
//     the very origin it is sent to (the web client at /app, served from
//     here), and a browser's Sec-Fetch-Site, when sent, must be same-origin
//     or none (typed into the address bar). Cross-site pages, including a
//     plain <img> or <script> GET that carries no Origin, are refused.
func RequireTailnetUser(allowed, trustedHosts []string, next http.Handler) http.Handler {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[strings.ToLower(strings.TrimSpace(a))] = true
	}
	hosts := map[string]bool{}
	for _, h := range trustedHosts {
		if h = normalizeHost(h); h != "" {
			hosts[h] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts[normalizeHost(r.Host)] {
			writeError(w, http.StatusForbidden, "forbidden", errors.New("untrusted Host; use this API through tailscale serve"))
			return
		}
		if !fromLoopback(r.RemoteAddr) || !fromOwnPage(r) {
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

// normalizeHost lowercases a Host value and drops its port and trailing dot.
func normalizeHost(hostport string) string {
	host := strings.TrimSpace(hostport)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// fromOwnPage reports whether a browser request comes from a page served by
// this origin (or from no page at all). tailscale serve terminates HTTPS, so
// the origin is https and the Host it forwards.
func fromOwnPage(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	return origin == "" || strings.EqualFold(origin, "https://"+r.Host)
}

func fromLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
