package sandboxlaunch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// uuidRE is the canonical form Claude Code names its transcripts with.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Archive copies a finished session's transcript out of the sandbox to
// archiveDir/openshell-<sandbox>/<sessionID>.jsonl (0600). The search index
// scans archiveDir/<any project>/*.jsonl, so the copy stays searchable after
// the sandbox is gone.
func Archive(ctx context.Context, b Backend, sandbox, sessionID, archiveDir string) error {
	if !uuidRE.MatchString(sessionID) {
		return fmt.Errorf("session id %q is not a UUID", sessionID)
	}
	if !sandboxNameRE.MatchString(sandbox) {
		return fmt.Errorf("invalid sandbox name %q", sandbox)
	}
	local := filepath.Join(archiveDir, "openshell-"+sandbox, sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return err
	}
	if err := b.Download(ctx, sandbox, "/sandbox/.claude/projects/-sandbox/"+sessionID+".jsonl", local); err != nil {
		return fmt.Errorf("archive session %s: %w", sessionID, err)
	}
	return os.Chmod(local, 0o600)
}
