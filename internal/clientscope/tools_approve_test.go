package clientscope

import (
	"strings"
	"testing"
)

// By default only Wingman may approve tools, each time after the person at
// the Mac confirms (mac-confirm); the device client keeps secure-enclave and
// approves nothing until clients.yaml turns it on.
func TestToolsApprove_DefaultsAndFile(t *testing.T) {
	d := Defaults().Clients
	if w := d["wingman"]; !w.ToolsApprove || len(w.StepUpKinds) != 1 || w.StepUpKinds[0] != StepUpMacConfirm {
		t.Fatalf("wingman default %+v, want tools_approve with mac-confirm", w)
	}
	if dev := d["device"]; dev.ToolsApprove || dev.StepUpKinds[0] != "secure-enclave" {
		t.Fatalf("device default %+v", dev)
	}
	if d["cli"].ToolsApprove {
		t.Fatal("cli may approve tools by default")
	}
	s, err := Load(write(t, "clients:\n  device:\n    tools_approve: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Clients["device"].ToolsApprove || !s.Clients["wingman"].ToolsApprove {
		t.Fatalf("device %v wingman %v, want both", s.Clients["device"].ToolsApprove, s.Clients["wingman"].ToolsApprove)
	}
	// A file can turn Wingman's off.
	s, err = Load(write(t, "clients:\n  wingman:\n    tools_approve: false\n"))
	if err != nil || s.Clients["wingman"].ToolsApprove {
		t.Fatalf("err %v, wingman approve %v", err, s.Clients["wingman"].ToolsApprove)
	}
	// A file entry without the key keeps it off.
	s, err = Load(write(t, "clients:\n  device:\n    max_sessions: 3\n"))
	if err != nil || s.Clients["device"].ToolsApprove {
		t.Fatalf("err %v, approve %v", err, s.Clients["device"].ToolsApprove)
	}
}

// mac-confirm is for local clients only: the person approving must be at
// the Mac, so the device client (tailnet) cannot use it.
func TestLoad_DeviceMacConfirmRefused(t *testing.T) {
	_, err := Load(write(t, "clients:\n  device:\n    step_up_kinds: [secure-enclave, mac-confirm]\n"))
	if err == nil || !strings.Contains(err.Error(), "mac-confirm") || !strings.Contains(err.Error(), "device") {
		t.Fatalf("err %v, want a refusal naming mac-confirm and device", err)
	}
	if _, err := Load(write(t, "clients:\n  wingman:\n    step_up_kinds: [mac-confirm]\n")); err != nil {
		t.Fatalf("wingman with mac-confirm: %v", err)
	}
}
