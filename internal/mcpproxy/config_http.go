package mcpproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// HTTPEntry is one Claude Code HTTP server that may move behind the proxy.
// Headers hold resolved values and are never printed.
type HTTPEntry struct {
	Raw      json.RawMessage   // as in the Claude config
	URL      string            // variables resolved
	Headers  map[string]string // resolved values; never printed
	Literals map[string]string // header name -> raw token, from redactHeaders
}

// KeptDirect is a server install leaves direct, with the reason.
type KeptDirect struct {
	Server string `json:"server"`
	Reason string `json:"reason"`
}

// ReasonNeedsOAuth is the kept-direct reason for servers that sign in with
// OAuth, which the proxy cannot do yet.
const ReasonNeedsOAuth = "needs OAuth (later)"

// Static kept-direct reasons.
const (
	reasonSSE          = "SSE transport (not supported behind the proxy)"
	reasonHelper       = "headersHelper (headers made by a script)"
	reasonBadHeaders   = "headers is not a map of strings"
	reasonNoKeychain   = "headers need the macOS Keychain"
	reasonKeychainName = "name has characters a Keychain item name cannot carry"
)

// LoadClaudeHTTPServers returns the type "http" entries that may move and
// the HTTP and SSE entries kept direct with their reason, sorted by name.
// Entries that point at the proxy on port are left out of both. An entry
// with headers moves only when canStoreSecrets (a Keychain to hold them).
func LoadClaudeHTTPServers(path string, port int, canStoreSecrets bool) (map[string]HTTPEntry, []KeptDirect, error) {
	_, members, err := readMCPServers(path)
	if err != nil {
		return nil, nil, err
	}
	proxyPrefix := fmt.Sprintf("http://127.0.0.1:%d/mcp/", port)
	move := map[string]HTTPEntry{}
	var kept []KeptDirect
	keep := func(name, reason string) { kept = append(kept, KeptDirect{Server: name, Reason: reason}) }
	for name, raw := range members {
		var e struct {
			Type          string          `json:"type"`
			URL           string          `json:"url"`
			Headers       json.RawMessage `json:"headers"`
			HeadersHelper json.RawMessage `json:"headersHelper"`
			OAuth         json.RawMessage `json:"oauth"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue // not an object: not an HTTP server this code knows
		}
		switch {
		case e.Type == "sse":
			keep(name, reasonSSE)
			continue
		case e.Type != "http" || strings.HasPrefix(e.URL, proxyPrefix) || strings.HasPrefix(expandValue(e.URL), proxyPrefix):
			continue
		case e.OAuth != nil:
			keep(name, ReasonNeedsOAuth)
			continue
		case e.HeadersHelper != nil:
			keep(name, reasonHelper)
			continue
		}
		if v := urlSecretVar(e.URL); v != "" {
			keep(name, "uses ${"+v+"} in its URL (query or userinfo); keep it direct until URL secrets are supported")
			continue
		}
		_, literals, err := redactHeaders(raw)
		if err != nil {
			keep(name, reasonBadHeaders)
			continue
		}
		entry := HTTPEntry{Raw: raw, URL: expandValue(e.URL), Literals: literals}
		unset := unsetVar(e.URL)
		if len(literals) > 0 {
			entry.Headers = make(map[string]string, len(literals))
			for h, lit := range literals {
				var v string
				_ = json.Unmarshal([]byte(lit), &v) // a string token: redactHeaders checked
				if u := unsetVar(v); u != "" && unset == "" {
					unset = u
				}
				entry.Headers[h] = expandValue(v)
			}
		}
		switch {
		case unset != "":
			keep(name, "uses ${"+unset+"}, which is unset in this shell")
		case len(literals) > 0 && !canStoreSecrets:
			keep(name, reasonNoKeychain)
		case len(literals) > 0 && !keychainName(name):
			keep(name, reasonKeychainName)
		default:
			move[name] = entry
		}
	}
	sort.Slice(kept, func(a, b int) bool { return kept[a].Server < kept[b].Server })
	return move, kept, nil
}

// unsetVar is the first ${VAR} in s without a default whose variable is
// unset or empty (expandValue would drop it), or "".
func unsetVar(s string) string {
	for _, m := range claudeVar.FindAllStringSubmatch(s, -1) {
		if m[2] == "" && os.Getenv(m[1]) == "" {
			return m[1]
		}
	}
	return ""
}

// keychainName reports whether a server name can be part of a Keychain item
// name Store accepts.
func keychainName(name string) bool {
	ok := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)
	}
	for _, r := range name {
		if !ok(r) {
			return false
		}
	}
	return name != ""
}

// urlSecretVar is the first ${VAR} in the userinfo or the query of a URL
// as written, or "": a resolved value there would be saved in plain text
// and shown in every connection error.
func urlSecretVar(raw string) string {
	rest := raw
	if _, after, ok := strings.Cut(rest, "://"); ok {
		rest = after
	}
	authority, _, _ := strings.Cut(rest, "/")
	authority, _, _ = strings.Cut(authority, "?")
	var parts []string
	if userinfo, _, ok := strings.Cut(authority, "@"); ok {
		parts = append(parts, userinfo)
	}
	if _, query, ok := strings.Cut(raw, "?"); ok {
		parts = append(parts, query)
	}
	for _, p := range parts {
		if m := claudeVar.FindStringSubmatch(p); m != nil {
			return m[1]
		}
	}
	return ""
}
