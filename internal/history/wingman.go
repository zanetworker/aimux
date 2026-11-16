package history

import (
	"os"
	"path/filepath"
	"strings"
)

// WingmanSessionsDir is the working directory of Wingman's page-lane
// sessions (~/.aimux/wingman/sessions). Claude Code files their transcripts
// under ~/.claude/projects like any other session, and they hold untrusted
// web page text, so session lists and the search index skip them.
func WingmanSessionsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aimux", "wingman", "sessions")
}

// IsWingmanProjectDir reports whether name, a ~/.claude/projects directory
// name, is a session dir whose cwd is ~/.aimux/wingman or below it. It
// compares encoded names, the way Claude encodes a cwd ('/' and '.' to '-').
func IsWingmanProjectDir(name string) bool {
	base := encodeProjectDir(filepath.Dir(WingmanSessionsDir()))
	return name == base || strings.HasPrefix(name, base+"-")
}
