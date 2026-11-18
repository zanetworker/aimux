package toolgateway

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Concurrent updates each read the file as the last one left it: no
// approval is lost to another writer's rename.
func TestPinStore_UpdateSerializesWriters(t *testing.T) {
	s := PinStore{Path: filepath.Join(t.TempDir(), "tools", "pins.json")}
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Update(func(f PinFile) (PinFile, bool, error) {
				if f["src"] == nil {
					f["src"] = map[string]PinRecord{}
				}
				f["src"][fmt.Sprintf("t%d", i)] = PinRecord{Hash: fmt.Sprintf("sha256:%d", i)}
				return f, true, nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(got["src"]) != n {
		t.Fatalf("%d records after %d concurrent updates: approvals were lost", len(got["src"]), n)
	}
}

func TestPinStore_UpdateNoChangeAndError(t *testing.T) {
	s := PinStore{Path: filepath.Join(t.TempDir(), "pins.json")}
	wrote, err := s.Update(func(f PinFile) (PinFile, bool, error) { return f, false, nil })
	if err != nil || wrote {
		t.Fatalf("no change: wrote %v err %v", wrote, err)
	}
	boom := errors.New("boom")
	if _, err := s.Update(func(f PinFile) (PinFile, bool, error) { return f, true, boom }); !errors.Is(err, boom) {
		t.Fatalf("err %v, want boom", err)
	}
	if f, _ := s.LoadFile(); len(f) != 0 {
		t.Fatalf("a failed update wrote %v", f)
	}
}

// A lock held too long fails the update with ErrPinsBusy after a bounded
// wait instead of hanging the caller.
func TestPinStore_UpdateGivesUpOnAHeldLock(t *testing.T) {
	s := PinStore{Path: filepath.Join(t.TempDir(), "pins.json")}
	old := pinLockWait
	pinLockWait = 200 * time.Millisecond
	t.Cleanup(func() { pinLockWait = old })
	unlock, err := lockPins(s.Path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	_, err = s.Update(func(f PinFile) (PinFile, bool, error) { return f, true, nil })
	if !errors.Is(err, ErrPinsBusy) {
		t.Fatalf("err %v, want ErrPinsBusy", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("gave up after %v, want about the bounded wait", d)
	}
}
