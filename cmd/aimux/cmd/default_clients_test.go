package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/clientscope"
)

// The clients.yaml setup writes turns tools_approve on for Wingman with the
// mac-confirm step-up, and loads as the defaults do.
func TestWriteDefaultClients_ListsToolsApproveWithMacConfirm(t *testing.T) {
	home := t.TempDir()
	if err := writeDefaultClients(home, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".config", "aimux", "clients.yaml")
	b, err := os.ReadFile(p) //nolint:gosec // G304: the test's temp home
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "    tools_approve: true\n") || !strings.Contains(string(b), "    step_up_kinds: [mac-confirm]\n") {
		t.Fatalf("clients.yaml:\n%s", b)
	}
	s, err := clientscope.Load(p)
	if w := s.Clients["wingman"]; err != nil || !w.ToolsApprove || len(w.StepUpKinds) != 1 || w.StepUpKinds[0] != clientscope.StepUpMacConfirm {
		t.Fatalf("err %v, wingman %+v", err, s.Clients["wingman"])
	}
}
