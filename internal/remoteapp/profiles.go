package remoteapp

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/zanetworker/aimux/internal/profile"
)

// profileRuns resolves a profile name to its Manifest.Runs, reading the user
// profiles in dir on every call so an edit applies to the next turn. A bad
// file is skipped (profile.LoadDir keeps the good ones).
func profileRuns(dir string) func(name string) (string, error) {
	return func(name string) (string, error) {
		user, _ := profile.LoadDir(dir) // per-file errors: the good profiles are still returned
		m, err := profile.Resolve(name, user)
		if err != nil {
			return "", err
		}
		return m.Runs, nil
	}
}

// profileDir is where user profiles live: $AIMUX_PROFILE_DIR, else
// ~/.config/aimux/profiles (as the aimux profile command reads them).
func profileDir(home string, getenv func(string) string) string {
	if d := getenv("AIMUX_PROFILE_DIR"); d != "" {
		return d
	}
	return filepath.Join(home, ".config", "aimux", "profiles")
}

// profileCatalog is the API's Profiles: every builtin and every user
// profile in dir, resolved through its extends chain (so a user child of
// coding has Runs host), read afresh on each call so an edit applies at
// once. A file that cannot be loaded or resolved is left out and logged to
// log, once until the problems change; the rest are still served.
func profileCatalog(dir string, log io.Writer) func() (map[string]profile.Manifest, error) {
	var mu sync.Mutex
	last := ""
	return func() (map[string]profile.Manifest, error) {
		user, loadErr := profile.LoadDir(dir)
		if user == nil {
			return nil, loadErr // the directory itself is unreadable
		}
		var problems []string
		if loadErr != nil {
			problems = append(problems, loadErr.Error())
		}
		out := map[string]profile.Manifest{}
		for _, n := range profile.BuiltinNames() {
			m, err := profile.Resolve("builtin:"+n, user)
			if err != nil {
				problems = append(problems, fmt.Sprintf("builtin profile %s: %v", n, err))
				continue
			}
			m.Name = n
			out[n] = m
		}
		for n := range user {
			m, err := profile.Resolve(n, user)
			if err != nil {
				problems = append(problems, fmt.Sprintf("profile %s: %v", n, err))
				continue
			}
			out[n] = m
		}
		sort.Strings(problems)
		msg := strings.Join(problems, "\n")
		mu.Lock()
		if msg != last && msg != "" {
			_, _ = fmt.Fprintf(log, "profiles in %s: %s\n", dir, msg)
		}
		last = msg
		mu.Unlock()
		return out, nil
	}
}
