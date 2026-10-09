package history

import "strings"

// Automated sessions are runs nobody typed into: `claude -p` from cron and
// scripts, session analyzers, and similar. They usually far outnumber
// interactive sessions, so session lists and search hide them by default.
// This is the single definition both history (lists) and internal/search
// (search) use.

// DefaultAutomatedPrefixes are first-prompt openings of known automated runs.
var DefaultAutomatedPrefixes = []string{
	"YOU ARE A SESSION ANALYZER", "Evaluate session", "Tag each library item",
	"LINKS ARE MANDATORY", "Read the JSON file", "Run a prompt audit",
}

var tempDirPrefixes = []string{"/private/var/folders/", "/var/folders/", "/tmp/", "/private/tmp/"}

// IsAutomated reports whether a session was started by a program: Claude
// Code records an "sdk-*" entrypoint for SDK and `claude -p` runs; scripts
// often run from a temp directory; and some runs are recognisable by their
// first prompt.
func IsAutomated(entrypoint, cwd, firstPrompt string, prefixes []string) bool {
	if strings.HasPrefix(entrypoint, "sdk") {
		return true
	}
	for _, p := range tempDirPrefixes {
		if strings.HasPrefix(cwd, p) {
			return true
		}
	}
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(firstPrompt, p) {
			return true
		}
	}
	return false
}
