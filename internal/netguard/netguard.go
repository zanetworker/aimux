// Package netguard keeps aimux's local HTTP endpoints reachable only from
// this machine: no DNS rebinding and no requests from other web sites.
package netguard

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// IsLoopbackHost reports whether a Host header value (host or host:port)
// names this machine: localhost or a loopback IP.
func IsLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsLoopbackOrigin reports whether an Origin header is an http(s) origin on
// a loopback host. An empty Origin is not a loopback origin.
func IsLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return IsLoopbackHost(u.Host)
}

// LocalOnly rejects requests whose Host is not loopback, or that a browser
// sent from a page that is not itself on a loopback origin.
func LocalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if !IsLoopbackHost(r.Host) || (origin != "" && !IsLoopbackOrigin(origin)) {
			http.Error(w, "forbidden: not a local request", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
