package mcpproxy

import (
	"context"
	"testing"
)

// After SetStarter, new backends come from the new starter on the same pool
// (so the daemon's reaper, session close and shutdown still reach them), a
// retired backend with a lease in flight closes only when it is released,
// and Close closes what the new starter made.
func TestPool_SetStarterKeepsOnePoolOwner(t *testing.T) {
	old, next := newFakeStarter(), newFakeStarter()
	p := NewPool(old.start, PoolOptions{})
	ctx := context.Background()
	inflight, err := p.Lease(ctx, "jira")
	if err != nil {
		t.Fatal(err)
	}
	p.SetStarter(next.start)
	closeRetired := p.RetireServer("jira")
	closeRetired()
	oldB := old.backends[0]
	if oldB.closed.Load() {
		t.Fatal("retired backend closed with a lease in flight")
	}
	if _, err := inflight.ListToolsRaw(ctx); err != nil {
		t.Fatalf("in-flight lease failed: %v", err)
	}
	_ = inflight.Close()
	if !oldB.closed.Load() {
		t.Fatal("retired backend not closed after its last lease")
	}
	b, err := p.Lease(ctx, "jira")
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if next.starts["jira"] != 1 || old.starts["jira"] != 1 {
		t.Fatalf("starts old=%v next=%v, want one each", old.starts, next.starts)
	}
	p.Close()
	for _, nb := range next.backends {
		if !nb.closed.Load() {
			t.Fatal("Close left a backend of the new starter open")
		}
	}
}
