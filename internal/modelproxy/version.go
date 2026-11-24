package modelproxy

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// testedCodexVersion is the Codex major.minor the proxy was verified against (live test and Claude Code end to end).
// The lean catalog, omit_skill_descriptions, dynamic tools and thread/inject_items are internal or experimental in
// Codex, so another version gets a warning rather than a refusal.
const testedCodexVersion = "0.170"

// warnIfUntestedCodex writes a warning to stderr when `command --version` reports another major.minor, or cannot
// be read.
func warnIfUntestedCodex(ctx context.Context, command string, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "--version").Output() // #nosec G204 G702 -- the owner-configured Codex command, fixed args
	fields := strings.Fields(string(out))
	if err != nil || len(fields) == 0 {
		_, _ = fmt.Fprintf(stderr, "warning: could not read the Codex version from %q --version; the model proxy was tested with %s\n", command, testedCodexVersion)
		return
	}
	version := fields[len(fields)-1]
	if version != testedCodexVersion && !strings.HasPrefix(version, testedCodexVersion+".") {
		_, _ = fmt.Fprintf(stderr, "warning: Codex %s has not been tested with the model proxy (tested: %s); run its live test before relying on it\n", version, testedCodexVersion)
	}
}
