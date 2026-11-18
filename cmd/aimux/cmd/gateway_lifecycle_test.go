package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// gateway install, uninstall and an install's rollback take one lock: a
// second one while the first runs fails clearly, after a short wait.
func TestWithGatewayLifecycle_Serializes(t *testing.T) {
	home := t.TempDir()
	old := gatewayLockWait
	gatewayLockWait = 200 * time.Millisecond
	t.Cleanup(func() { gatewayLockWait = old })
	inner := errors.New("unset")
	err := withGatewayLifecycle(home, func() error {
		inner = withGatewayLifecycle(home, func() error { return nil })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if inner == nil || !strings.Contains(inner.Error(), "another aimux mcp proxy gateway install or uninstall") {
		t.Fatalf("nested lifecycle: %v, want a refusal", inner)
	}
	ran := false
	if err := withGatewayLifecycle(home, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("after release: err %v ran %v", err, ran)
	}
}
