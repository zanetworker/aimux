package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// localOnly rejects requests that did not come from this machine's dashboard
// or a local client. See allowLocalRequest.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowLocalRequest(r) {
			http.Error(w, "forbidden: request is not from the local dashboard", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowLocalRequest requires a loopback Host on every request (blocks DNS
// rebinding) and, for requests that change state, a loopback Origin when one
// is sent (blocks cross-site form and text/plain POSTs, which skip CORS
// preflight). Any loopback port is accepted so the Vite dev server keeps
// working; WebSocket upgrades use the stricter checkLocalOrigin.
func allowLocalRequest(r *http.Request) bool {
	if !isLoopbackHost(r.Host) {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return isLoopbackHost(u.Host)
}

// defaultSessionRoots are the directories agent session files live in.
func defaultSessionRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".aimux", "archive"),
	}
}

func (s *Server) roots() []string {
	if s.sessionRoots != nil {
		return s.sessionRoots
	}
	return defaultSessionRoots()
}

// isAllowedSessionFile reports whether path is an absolute .jsonl or .json
// file inside one of roots after resolving "..", and symlinks when the file
// exists. Files that do not exist yet are allowed if their location is inside
// a root, so sidecar metadata can be written for new sessions.
func isAllowedSessionFile(path string, roots []string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	if ext := filepath.Ext(path); ext != ".jsonl" && ext != ".json" {
		return false
	}
	resolved := filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	} else if dir, err := filepath.EvalSymlinks(filepath.Dir(resolved)); err == nil {
		resolved = filepath.Join(dir, filepath.Base(resolved))
	}
	for _, root := range roots {
		if realRoot, err := filepath.EvalSymlinks(root); err == nil {
			root = realRoot
		}
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." {
			return true
		}
	}
	return false
}

// requireSessionFile writes a 403 and returns false when path is not an
// allowed session file.
func (s *Server) requireSessionFile(w http.ResponseWriter, path string) bool {
	if !isAllowedSessionFile(path, s.roots()) {
		http.Error(w, "forbidden: file is not an agent session file", http.StatusForbidden)
		return false
	}
	return true
}
