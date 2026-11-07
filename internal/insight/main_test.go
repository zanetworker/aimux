package insight

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points HOME at an empty folder, so the default usage path never
// writes the owner's file.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "aimux-insight-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test home:", err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
