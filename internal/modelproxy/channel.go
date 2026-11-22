package modelproxy

import "path/filepath"

// StablePort is the launchd-managed proxy port.
const StablePort = 7385

// UsageFile returns the stable HUD snapshot path. Other listeners do not publish usage.
func UsageFile(home string, port int) string {
	if port != StablePort {
		return ""
	}
	return filepath.Join(home, ".aimux", "codex-usage.json")
}
