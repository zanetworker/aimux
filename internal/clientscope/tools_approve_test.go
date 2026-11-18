package clientscope

import "testing"

// tools_approve is off for every client by default; clients.yaml turns it
// on per client, and another client's setting does not leak.
func TestToolsApprove_OffByDefaultOnFromFile(t *testing.T) {
	for name, c := range Defaults().Clients {
		if c.ToolsApprove {
			t.Fatalf("%s may approve tools by default", name)
		}
	}
	s, err := Load(write(t, "clients:\n  device:\n    tools_approve: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Clients["device"].ToolsApprove || s.Clients["wingman"].ToolsApprove {
		t.Fatalf("device %v wingman %v, want only device", s.Clients["device"].ToolsApprove, s.Clients["wingman"].ToolsApprove)
	}
	// A file entry without the key keeps it off.
	s, err = Load(write(t, "clients:\n  device:\n    max_sessions: 3\n"))
	if err != nil || s.Clients["device"].ToolsApprove {
		t.Fatalf("err %v, approve %v", err, s.Clients["device"].ToolsApprove)
	}
}
