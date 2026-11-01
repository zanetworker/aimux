package claude

import (
	"path/filepath"
	"strings"
)

// CommandLineSessions returns the words of a Claude Code command line that
// may name the session it works on (`claude --resume <id>`,
// `--session-id=<id>`): every argument, and an argument's text after its
// last "=". Nil when the command is not claude. The caller keeps the words
// that are session ids.
func CommandLineSessions(args string) []string {
	fields := strings.Fields(args)
	if len(fields) == 0 || filepath.Base(fields[0]) != "claude" {
		return nil
	}
	var out []string
	for _, f := range fields[1:] {
		out = append(out, f)
		if i := strings.LastIndex(f, "="); i >= 0 {
			out = append(out, f[i+1:])
		}
	}
	return out
}
