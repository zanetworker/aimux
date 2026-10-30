// Package gitremote finds the web address of a folder's git repository, so
// clients can link commits, issues and files in a reply.
package gitremote

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// knownHosts are the hosts that serve the /issues, /commit and /blob URLs
// clients build. Anything containing "gitlab" is taken as a GitLab server.
func supported(host string) bool {
	return host == "github.com" || strings.Contains(host, "gitlab")
}

// WebURL turns a git remote into the address of its web page, without ".git"
// and without credentials. An http(s) remote keeps its scheme and a
// non-default port (a server's web UI is where its git is); ssh, git and
// scp-like remotes become https on the bare host. Hosts other than GitHub and
// GitLab give false.
func WebURL(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	var host, path, port string
	scheme := "https"
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git") {
			return "", false
		}
		host, path = u.Hostname(), u.Path // Hostname drops userinfo and port
		if u.Scheme == "http" || u.Scheme == "https" {
			scheme, port = u.Scheme, u.Port()
			if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
				port = ""
			}
		}
	case strings.Contains(remote, "@") && strings.Contains(remote, ":"):
		// scp-like: user@host:path
		rest := remote[strings.Index(remote, "@")+1:]
		i := strings.Index(rest, ":")
		if i < 0 {
			return "", false // the colon is before the @: not user@host:path
		}
		host, path = rest[:i], rest[i+1:]
		if !hostOK(host) {
			return "", false
		}
	default:
		return "", false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	host = strings.ToLower(host)
	if host == "" || !supported(host) || !strings.Contains(path, "/") || strings.HasPrefix(path, "/") {
		return "", false
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host + "/" + path, true
}

// hostOK accepts only plain host names in an scp-like remote.
func hostOK(h string) bool {
	return h != "" && strings.Trim(h, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-") == ""
}

const (
	gitTimeout = 2 * time.Second
	cacheTTL   = 60 * time.Second
)

type entry struct {
	web, branch, root string
	ok                bool
	at                time.Time
}

var (
	mu       sync.Mutex
	cache    = map[string]entry{}
	cacheNow = time.Now
)

// Lookup returns the web address of dir's "origin" remote (with insteadOf
// rewrites applied), its current branch and the repository's root folder. Any failure (not a repo, no origin, unknown host, git too slow)
// is ok=false. Results are cached per folder for a minute; expired entries are
// dropped whenever a new one is written.
func Lookup(dir string) (web, branch, root string, ok bool) {
	if dir == "" {
		return "", "", "", false
	}
	mu.Lock()
	if e, hit := cache[dir]; hit && cacheNow().Sub(e.at) < cacheTTL {
		mu.Unlock()
		return e.web, e.branch, e.root, e.ok
	}
	mu.Unlock()
	web, branch, root, ok = lookup(dir)
	mu.Lock()
	now := cacheNow()
	for d, e := range cache { // sweep expired entries so the cache stays as small as the folders in use
		if now.Sub(e.at) >= cacheTTL {
			delete(cache, d)
		}
	}
	cache[dir] = entry{web, branch, root, ok, now}
	mu.Unlock()
	return web, branch, root, ok
}

func lookup(dir string) (string, string, string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	remote, err := runGit(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return "", "", "", false
	}
	web, ok := WebURL(remote)
	if !ok {
		return "", "", "", false
	}
	root, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return "", "", "", false
	}
	branch, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil { // a repo with no commit yet has no HEAD to resolve
		branch, err = runGit(ctx, dir, "symbolic-ref", "--short", "HEAD")
	}
	if err != nil || branch == "" || branch == "HEAD" { // detached: no branch to link to
		return "", "", "", false
	}
	return web, branch, root, true
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed command; dir is a session folder passed as a -C argument
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
