package clientscope

import "testing"

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
