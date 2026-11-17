package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortSock is a socket path under /tmp: macOS limits socket paths to 104
// bytes, which t.TempDir() can exceed.
func shortSock(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, ".aimux", ControlSocketName)
}

type fakeControl struct {
	mu      sync.Mutex
	put     []Lease
	renewed []string
	deleted []string
	putErr  error
	known   map[string]bool
}

func (f *fakeControl) PutLease(_ context.Context, l Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	f.put = append(f.put, l)
	return nil
}

func (f *fakeControl) Renew(session string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.known[session] {
		return ErrUnknownLease
	}
	f.renewed = append(f.renewed, session)
	return nil
}

func (f *fakeControl) DeleteLease(session string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, session)
}

func (f *fakeControl) Observed() map[string]Observed {
	return map[string]Observed{"jira": {LiveSource: LiveSource{Err: errors.New("down")}, Endpoint: "https://jira"}}
}

func (f *fakeControl) Status() ControlStatus { return ControlStatus{Version: "v1", Leases: 2} }

func serveFake(t *testing.T, f *fakeControl) (ControlClient, string) {
	t.Helper()
	path := shortSock(t)
	ln, err := ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = ServeControl(ctx, ln, f); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return ControlClient{Path: path}, path
}

func TestControl_PutRenewDeleteRoundTrip(t *testing.T) {
	f := &fakeControl{known: map[string]bool{"sess-1": true}}
	c, _ := serveFake(t, f)
	ctx := context.Background()
	l := testLease(time.Now())
	if err := c.PutLease(ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := c.Renew(ctx, "sess-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteLease(ctx, "sess-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.put) != 1 || f.put[0].Token != leaseTok || f.put[0].Pins["jira"]["search"].Hash != "sha256:a" {
		t.Fatalf("put = %+v", f.put)
	}
	if len(f.renewed) != 1 || len(f.deleted) != 1 {
		t.Fatalf("renewed %v deleted %v", f.renewed, f.deleted)
	}
	obs, err := c.Observed(ctx)
	if err != nil || obs["jira"].Err == nil || obs["jira"].Err.Error() != "down" || obs["jira"].Endpoint != "https://jira" {
		t.Fatalf("Observed = %+v %v", obs, err)
	}
	st, err := c.Status(ctx)
	if err != nil || st.Leases != 2 || st.Version != "v1" {
		t.Fatalf("Status = %+v %v", st, err)
	}
}

func TestControl_RenewUnknownMapsToErrUnknownLease(t *testing.T) {
	c, _ := serveFake(t, &fakeControl{})
	if err := c.Renew(context.Background(), "gone", time.Now().Add(time.Hour)); !errors.Is(err, ErrUnknownLease) {
		t.Fatalf("Renew = %v, want ErrUnknownLease", err)
	}
}

func TestControl_SocketMode0600(t *testing.T) {
	_, path := serveFake(t, &fakeControl{})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v, want 0700", di.Mode().Perm())
	}
}

func TestListenControl_RemovesDeadSocket(t *testing.T) {
	path := shortSock(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	dead, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	// Closing a unix listener removes its file; keep it so the path stays
	// a socket nobody answers on, as after a crash.
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = dead.Close()
	ln, err := ListenControl(path)
	if err != nil {
		t.Fatalf("a dead socket must be replaced: %v", err)
	}
	_ = ln.Close()
}

func TestListenControl_RefusesLiveSocket(t *testing.T) {
	_, path := serveFake(t, &fakeControl{})
	if _, err := ListenControl(path); err == nil || !strings.Contains(err.Error(), "another tool gateway is running") {
		t.Fatalf("ListenControl on a live socket = %v", err)
	}
}

func TestListenControl_RefusesRegularFile(t *testing.T) {
	path := shortSock(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenControl(path); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("ListenControl on a file = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "x" { //nolint:gosec // test temp file
		t.Fatal("the file was changed")
	}
}

func TestControlClient_NoSocketIsNotRunning(t *testing.T) {
	c := ControlClient{Path: shortSock(t)}
	if err := c.PutLease(context.Background(), testLease(time.Now())); !errors.Is(err, ErrGatewayNotRunning) {
		t.Fatalf("PutLease without a worker = %v, want ErrGatewayNotRunning", err)
	}
}

func TestControl_TokenNeverInErrorText(t *testing.T) {
	c, _ := serveFake(t, &fakeControl{putErr: errors.New("lease for session sess-1 refused")})
	err := c.PutLease(context.Background(), testLease(time.Now()))
	if err == nil || strings.Contains(err.Error(), leaseTok) {
		t.Fatalf("PutLease error %v must exist and not carry the token", err)
	}
}

func TestPeerUID_Self(t *testing.T) {
	path := shortSock(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := net.Dial("unix", path)
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			_ = c.Close()
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	uid, err := peerUID(c)
	if err != nil || uid != os.Getuid() {
		t.Fatalf("peerUID = %d %v, want %d", uid, err, os.Getuid())
	}
}

type bigObserved struct{ *fakeControl }

func (bigObserved) Observed() map[string]Observed {
	tools := map[string]json.RawMessage{}
	pad := strings.Repeat("x", 4096)
	for i := 0; i < 600; i++ { // about 2.4 MiB of schemas
		name := fmt.Sprintf("tool_%03d", i)
		tools[name] = json.RawMessage(fmt.Sprintf(`{"name":%q,"description":%q}`, name, pad))
	}
	return map[string]Observed{"big": {LiveSource: LiveSource{Tools: tools}}}
}

func TestControlClient_ObservedLargerThanRequestLimit(t *testing.T) {
	path := shortSock(t)
	ln, err := ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeControl(ctx, ln, bigObserved{&fakeControl{}}) }()
	obs, err := ControlClient{Path: path}.Observed(context.Background())
	if err != nil || len(obs["big"].Tools) != 600 {
		t.Fatalf("Observed = %d tools, %v", len(obs["big"].Tools), err)
	}
}

func TestListenControl_ConcurrentStartsOneWins(t *testing.T) {
	path := shortSock(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	dead, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = dead.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var lns []net.Listener
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ln, err := ListenControl(path); err == nil {
				mu.Lock()
				lns = append(lns, ln)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(lns) != 1 {
		t.Fatalf("%d concurrent starts got the socket, want 1", len(lns))
	}
	_ = lns[0].Close()
	ln, err := ListenControl(path)
	if err != nil {
		t.Fatalf("after the winner closed, a new start must succeed: %v", err)
	}
	_ = ln.Close()
}

// An existing folder with a looser mode is tightened to 0700.
func TestListenControl_TightensExistingFolder(t *testing.T) {
	path := shortSock(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the loose mode under test
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the loose mode under test
		t.Fatal(err)
	}
	ln, err := ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v, want 0700", di.Mode().Perm())
	}
}
