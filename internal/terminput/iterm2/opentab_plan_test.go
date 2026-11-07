package iterm2

import (
	"os/exec"
	"testing"
)

// TestOpentabPlan_Node runs opentab.test.cjs, the unit tests of opentab.js's
// layout decision, with node (skipped when node is not installed).
func TestOpentabPlan_Node(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "--test", "opentab.test.cjs").CombinedOutput() // #nosec G204 -- fixed test file
	if err != nil {
		t.Fatalf("node --test opentab.test.cjs: %v\n%s", err, out)
	}
}
