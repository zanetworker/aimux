package modelproxy

import (
	"fmt"
	"path/filepath"
)

// StablePort is the launchd-managed proxy port.
const StablePort = 7385

// RequestLogFile returns the proxy's request history for port. Each listener keeps its own file, so a dev proxy
// never interleaves lines with the stable one.
func RequestLogFile(home string, port int) string {
	name := "requests.jsonl"
	if port != StablePort {
		name = fmt.Sprintf("requests-%d.jsonl", port)
	}
	return filepath.Join(home, ".aimux", "logs", "model-proxy", name)
}

// RequestLogLimit keeps months of history in a file small enough to read: about 25,000 requests.
const RequestLogLimit = 8 << 20

// UsageFile returns the stable HUD snapshot path. Other listeners do not publish usage.
func UsageFile(home string, port int) string {
	if port != StablePort {
		return ""
	}
	return filepath.Join(home, ".aimux", "codex-usage.json")
}
