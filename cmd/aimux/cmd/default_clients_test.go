package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/clientscope"
)

// The clients.yaml setup writes names tools_approve for Wingman, off, and
// loads as the defaults do.
func TestWriteDefaultClients_ListsToolsApproveOff(t *testing.T) {
	home := t.TempDir()
	if err := writeDefaultClients(home, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".config", "aimux", "clients.yaml")
	b, err := os.ReadFile(p) //nolint:gosec // G304: the test's temp home
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "    tools_approve: false\n") {
		t.Fatalf("clients.yaml:\n%s", b)
	}
	s, err := clientscope.Load(p)
	if err != nil || s.Clients["wingman"].ToolsApprove {
		t.Fatalf("err %v, wingman approve %v", err, s.Clients["wingman"].ToolsApprove)
	}
}
