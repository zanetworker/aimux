package acprun

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestSessionLock(t *testing.T) {
	dir := t.TempDir()
	release, err := SessionLock(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SessionLock(dir, "s1"); !errors.Is(err, ErrInUseElsewhere) {
		t.Fatalf("second holder: %v, want ErrInUseElsewhere", err)
	}
	if other, err := SessionLock(dir, "s2"); err != nil {
		t.Fatalf("a different session must not be blocked: %v", err)
	} else {
		other()
	}
	release()
	again, err := SessionLock(dir, "s1")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
	if _, err := SessionLock(dir, "../escape"); err == nil {
		t.Fatal("a session id with a path separator was accepted")
	}
}

// Two aimux processes (here two managers sharing the lock directory) must
// not both drive one session.
func TestManager_ReservationAcrossManagers(t *testing.T) {
	dir := t.TempDir()
	reserve := func(id string) (func(), error) { return SessionLock(dir, id) }
	fs1, fs2 := &fakeRunStarter{}, &fakeRunStarter{}
	m1 := NewManager(ManagerOptions{Start: fs1.start, Reserve: reserve})
	m2 := NewManager(ManagerOptions{Start: fs2.start, Reserve: reserve})
	defer m2.Close()
	if _, err := send(m1, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := send(m2, "a"); !errors.Is(err, ErrInUseElsewhere) {
		t.Fatalf("second process: %v, want ErrInUseElsewhere", err)
	}
	if fs2.count() != 0 {
		t.Fatal("the second process started an adapter for a reserved session")
	}
	m1.Close() // releases the reservation with its adapter
	if _, err := send(m2, "a"); err != nil {
		t.Fatalf("after the first process let go: %v", err)
	}
}

func TestManager_ReservationReleasedWhenStartFails(t *testing.T) {
	dir := t.TempDir()
	reserve := func(id string) (func(), error) { return SessionLock(dir, id) }
	fs := &fakeRunStarter{fail: errors.New("npx: not found")}
	m := NewManager(ManagerOptions{Start: fs.start, Reserve: reserve})
	defer m.Close()
	if _, err := m.Send(context.Background(), "a", "/w", "hi", func(Event) {}); err == nil {
		t.Fatal("start should fail")
	}
	release, err := SessionLock(dir, "a")
	if err != nil {
		t.Fatalf("a failed start kept the session reserved: %v", err)
	}
	release()
}

func TestManager_ReservationReleasedOnReap(t *testing.T) {
	dir := t.TempDir()
	reserve := func(id string) (func(), error) { return SessionLock(dir, id) }
	fs := &fakeRunStarter{}
	m := NewManager(ManagerOptions{Start: fs.start, Reserve: reserve, IdleTimeout: 1})
	defer m.Close()
	_, _ = send(m, "a")
	if n := m.Reap(); n != 1 {
		t.Fatalf("reaped %d", n)
	}
	release, err := SessionLock(dir, "a")
	if err != nil {
		t.Fatalf("a reaped run kept the session reserved: %v", err)
	}
	release()
}

// Another aimux process holds the session: the reason given must be that,
// not "open in a terminal" (which is what that process's adapter looks like
// from here).
func TestRouter_ReportsInUseElsewhereBeforeOwnership(t *testing.T) {
	projects, registry := claudeFixture(t)
	dir := t.TempDir()
	reserve := func(id string) (func(), error) { return SessionLock(dir, id) }
	other := &Router{Lookup: ClaudeLookup(projects, registry), Manager: NewManager(ManagerOptions{Start: (&fakeRunStarter{}).start, Reserve: reserve})}
	if _, err := other.Send(context.Background(), "idle-1", "hi", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	defer other.Manager.Close()
	writeHolder(t, registry, os.Getppid(), "idle-1") // the other process's adapter, unknown to us
	me := &Router{Lookup: ClaudeLookup(projects, registry), Manager: NewManager(ManagerOptions{Start: (&fakeRunStarter{}).start, Reserve: reserve, ParentPID: func(int) int { return 1 }})}
	defer me.Manager.Close()
	if err := me.Check("idle-1"); !errors.Is(err, ErrInUseElsewhere) {
		t.Fatalf("got %v, want ErrInUseElsewhere", err)
	}
}
