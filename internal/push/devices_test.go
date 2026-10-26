package push

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fake APNs device tokens: 32 bytes of hex.
var (
	tokenA = strings.Repeat("a1", 32)
	tokenB = strings.Repeat("fe", 32)
)

func TestDeviceStore_AddListRemoveSurviveReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "devices.json")
	s := &DeviceStore{Path: path}
	if ds, err := s.List(); err != nil || len(ds) != 0 {
		t.Fatalf("empty store: %v %v", ds, err)
	}
	if err := s.Add(Device{Token: tokenA, Environment: EnvSandbox}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Device{Token: tokenB, Environment: EnvProduction}); err != nil {
		t.Fatal(err)
	}
	// Registering again (the app does it on every launch) updates, not duplicates.
	if err := s.Add(Device{Token: tokenA, Environment: EnvProduction, Login: "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	ds, err := (&DeviceStore{Path: path}).List()
	if err != nil || len(ds) != 2 {
		t.Fatalf("after reload: %+v %v", ds, err)
	}
	if ds[0].Token != tokenA || ds[0].Environment != EnvProduction || ds[0].Login != "me@example.com" || ds[0].Added.IsZero() {
		t.Fatalf("re-registered device: %+v", ds[0])
	}
	if err := s.Remove(tokenA); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(tokenA); err != nil {
		t.Fatalf("removing an unknown token: %v, want nil", err)
	}
	ds, _ = s.List()
	if len(ds) != 1 || ds[0].Token != tokenB {
		t.Fatalf("after remove: %+v", ds)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store file mode: %v %v", fi, err)
	}
}

func TestDeviceStore_RejectsBadInput(t *testing.T) {
	s := &DeviceStore{Path: filepath.Join(t.TempDir(), "d.json")}
	for _, d := range []Device{
		{Token: "not-hex", Environment: EnvSandbox},
		{Token: "abcd", Environment: EnvSandbox}, // too short to be an APNs token
		{Token: strings.Repeat("a", 201), Environment: EnvSandbox},
		{Token: tokenA, Environment: "staging"},
		{Token: tokenA},
	} {
		if err := s.Add(d); !errors.Is(err, ErrBadDevice) {
			t.Errorf("Add(%q, %q) = %v, want ErrBadDevice", d.Token, d.Environment, err)
		}
	}
	// Tokens are case-insensitive hex; stored lower-case so a re-registration matches.
	if err := s.Add(Device{Token: strings.ToUpper(tokenA), Environment: EnvSandbox}); err != nil {
		t.Fatal(err)
	}
	if ds, _ := s.List(); len(ds) != 1 || ds[0].Token != tokenA {
		t.Fatalf("stored %+v", ds)
	}
}

func TestDeviceStore_CorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&DeviceStore{Path: path}).List(); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("got %v, want an error naming the file", err)
	}
}

type recorder struct {
	mu  sync.Mutex
	got []Message
	err error
}

func (r *recorder) Notify(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, m)
	return r.err
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func TestSend_TriesEveryNotifierAndJoinsErrors(t *testing.T) {
	failing := &recorder{err: errors.New("relay down")}
	ok := &recorder{}
	err := Send(context.Background(), []Notifier{failing, ok}, Message{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "relay down") {
		t.Fatalf("got %v", err)
	}
	if ok.count() != 1 {
		t.Fatal("a failing notifier stopped the next one")
	}
	if err := Send(context.Background(), nil, Message{}); err != nil {
		t.Fatalf("no notifiers: %v", err)
	}
}

// blocker blocks until its context ends.
type blocker struct{}

func (blocker) Notify(ctx context.Context, _ Message) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestSend_ASlowNotifierDoesNotHoldUpTheOthers(t *testing.T) {
	ok := &recorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- Send(ctx, []Notifier{blocker{}, ok}, Message{Title: "t"}) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && ok.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if ok.count() != 1 {
		t.Fatal("the second notifier waited for the blocked first one")
	}
	if err := <-done; err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("Send: %v after %v, want the blocked notifier's error once the context ends", err, time.Since(start))
	}
}

type retracter struct {
	recorder
	retracted []string
}

func (r *retracter) Retract(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retracted = append(r.retracted, m.ApprovalID)
	return nil
}

func TestRetract_OnlyNotifiersThatCan(t *testing.T) {
	r := &retracter{}
	if err := Retract(context.Background(), []Notifier{&recorder{}, r}, Message{ApprovalID: "ap1"}); err != nil {
		t.Fatal(err)
	}
	if len(r.retracted) != 1 || r.retracted[0] != "ap1" {
		t.Fatalf("retracted %v", r.retracted)
	}
}
