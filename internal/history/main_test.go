package history

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points HOME at an empty folder, so code with a default path under
// the home folder (usage records, naming state) never writes the owner's files.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "aimux-history-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test home:", err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
