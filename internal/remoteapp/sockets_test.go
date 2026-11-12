package remoteapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/config"
)

// shortDir is a private temp dir with a short path: a unix socket path must
// fit in 104 bytes on macOS, which t.TempDir() can exceed. Never under
// ~/.aimux/clients.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ax")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// A socket's directory is created 0700 and the socket is 0600; a stale
// socket (nothing answers) is replaced; a live one, a file that is not a
// socket and a directory others can write are refused.
func TestServe_SocketPermissions(t *testing.T) {
	base := shortDir(t)
	path := filepath.Join(base, "clients", "wingman.sock")
	ln, err := listenSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("socket dir mode %v %v, want 0700", fi.Mode().Perm(), err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode %v %v, want a 0600 socket", fi.Mode(), err)
	}
	if _, err := listenSocket(path); err == nil || !strings.Contains(err.Error(), "answers") {
		t.Fatalf("second listen on a live socket = %v, want refused", err)
	}

	// A stale socket: bound, then its process gone (the file is left behind).
	stale := filepath.Join(base, "clients", "stale.sock")
	l2, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l2.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l2.Close()
	if _, err := os.Lstat(stale); err != nil {
		t.Fatalf("stale socket not left behind: %v", err)
	}
	l3, err := listenSocket(stale)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	_ = l3.Close()

	notSock := filepath.Join(base, "clients", "file.sock")
	if err := os.WriteFile(notSock, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenSocket(notSock); err == nil {
		t.Fatal("a regular file at the socket path was replaced")
	}
	if b, _ := os.ReadFile(notSock); string(b) != "keep me" { //nolint:gosec // G304: notSock is in the test temp dir
		t.Fatal("the regular file was touched")
	}

	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o777); err != nil { //nolint:gosec // G301: deliberately open dir, to prove listenSocket refuses it
		t.Fatal(err)
	}
	_ = os.Chmod(open, 0o777) //nolint:gosec // G302: undo the umask so the dir is really open
	if _, err := listenSocket(filepath.Join(open, "w.sock")); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("socket in a world-writable dir = %v, want refused", err)
	}
	_ = ln.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket left behind after close: %v", err)
	}
}

// socketScopes are the default scopes with the wingman socket under dir.
func socketScopes(dir string) clientscope.Scopes {
	s := clientscope.Defaults()
	w := s.Clients["wingman"]
	w.Socket = filepath.Join(dir, "clients", "wingman.sock")
	s.Clients["wingman"] = w
	return s
}

// unixClient talks HTTP over the socket at path, as aimux.local.
func unixClient(path string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}}
}

// runUntil runs s until ready reports true, then stops it and returns Run's
// error.
func runUntil(t *testing.T, s *Server, ready func() bool) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() && time.Now().Before(deadline) {
		select {
		case err := <-done:
			cancel()
			return err
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !ready() {
		t.Fatal("never ready")
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not stop")
		return nil
	}
}

// --local-only: no tailnet listener, so no allowed logins, no Host names,
// no device login; the wingman socket serves /v1 to aimux.local.
func TestServe_LocalOnlyNeedsNoAllow(t *testing.T) {
	var out, errOut syncBuffer
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	s, _ := testServer(t, cfg, &out, &errOut)
	s.cfg.RemoteAccess.Hostnames = nil
	s.opts.Allowed, s.opts.LocalOnly = nil, true
	dir := shortDir(t)
	s.opts.Scopes = socketScopes(dir)
	sock := s.opts.Scopes.Clients["wingman"].Socket
	var status int
	var body map[string]any
	err := runUntil(t, s, func() bool {
		resp, err := unixClient(sock).Get("http://aimux.local/v1/health")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		status = resp.StatusCode
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return true
	})
	if err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, errOut.String())
	}
	if status != http.StatusOK || body["capabilities"] == nil {
		t.Fatalf("health over the socket: %d %v", status, body)
	}
	if strings.Contains(out.String(), "tailscale serve") || !strings.Contains(out.String(), sock) {
		t.Fatalf("stdout %q: want the socket line and no tailnet line", out.String())
	}
	if strings.Contains(errOut.String(), "device_login is false") {
		t.Fatalf("local-only warned about device login: %q", errOut.String())
	}
}

// A second remote serve, finding the wingman socket answering, exits 0 and
// names the running one's pid without opening anything.
func TestServe_SecondInstanceExitsZero(t *testing.T) {
	var out, errOut syncBuffer
	s, home := testServer(t, config.Config{}, &out, &errOut)
	dir := shortDir(t)
	s.opts.Scopes = socketScopes(dir)
	ln, err := listenSocket(s.opts.Scopes.Clients["wingman"].Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() { // the "running" server: this process, holding each connection
		for { // until the client closes it, as an HTTP server would
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c); _ = c.Close() }()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // a serving Run would block
	defer cancel()
	start := time.Now()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("second instance: %v, want a clean exit", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("the second instance served instead of exiting")
	}
	if want := fmt.Sprintf("already running (pid %d)\n", os.Getpid()); out.String() != want {
		t.Fatalf("stdout %q, want %q", out.String(), want)
	}
	if _, err := os.Stat(filepath.Join(home, ".aimux", "logs", "remote-audit.log")); !os.IsNotExist(err) {
		t.Fatalf("the second instance opened the audit log: %v", err)
	}
}

const h3 = "device login is off: any local process could claim a tailnet login; pass --unsafe-no-device-login to run anyway"

// H3: the tailnet listener without device login is refused before anything
// opens.
func TestServe_RefusesWithoutDeviceLogin(t *testing.T) {
	var out, errOut syncBuffer
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	s, _ := testServer(t, cfg, &out, &errOut)
	if err := s.Run(context.Background()); err == nil || err.Error() != h3 {
		t.Fatalf("Run = %v, want %q", err, h3)
	}
	if out.String() != "" {
		t.Fatalf("stdout %q", out.String())
	}
}

// The unsafe flag runs the tailnet listener without device login, with the
// warning.
func TestServe_UnsafeFlagAllowsIt(t *testing.T) {
	var out, errOut syncBuffer
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	s, _ := testServer(t, cfg, &out, &errOut)
	s.opts.UnsafeNoDeviceLogin = true
	err := runUntil(t, s, func() bool { return strings.Contains(out.String(), "/v1 for") })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(errOut.String(), "device_login is false") {
		t.Fatalf("stderr %q, want the device login warning", errOut.String())
	}
}

// The device client is checked by device login on the tailnet; a socket
// for it would skip that, so build refuses it before listening.
func TestServe_DeviceSocketRefused(t *testing.T) {
	var out, errOut syncBuffer
	s, _ := testServer(t, config.Config{}, &out, &errOut)
	dir := shortDir(t)
	s.opts.Scopes = clientscope.Defaults()
	d := s.opts.Scopes.Clients["device"]
	d.Socket = filepath.Join(dir, "clients", "device.sock")
	s.opts.Scopes.Clients["device"] = d
	w := &wiring{}
	defer w.close()
	if err := s.build(w); err == nil || !strings.Contains(err.Error(), "client device cannot have a socket") {
		t.Fatalf("build = %v, want the device socket refused", err)
	}
	if _, err := os.Lstat(d.Socket); !os.IsNotExist(err) {
		t.Fatalf("device socket created: %v", err)
	}
}

// A clients.yaml socket written as ~/... is created under the home
// directory, never as a literal ~ folder under serve's working directory.
func TestServe_TildeSocketUnderHome(t *testing.T) {
	var out, errOut syncBuffer
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	s, _ := testServer(t, cfg, &out, &errOut)
	home := shortDir(t) // a socket path must stay under 104 bytes
	t.Setenv("HOME", home)
	cwd := shortDir(t)
	t.Chdir(cwd)
	file := filepath.Join(cwd, "clients.yaml")
	if err := os.WriteFile(file, []byte("clients:\n  wingman:\n    socket: ~/c/w.sock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scopes, err := clientscope.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.RemoteAccess.Hostnames = nil
	s.opts.Allowed, s.opts.LocalOnly, s.opts.Scopes = nil, true, scopes
	want := filepath.Join(home, "c", "w.sock")
	if err := runUntil(t, s, func() bool { _, err := os.Lstat(want); return err == nil }); err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(cwd, "~")); !os.IsNotExist(err) {
		t.Fatalf("a literal ~ folder under the working directory: %v", err)
	}
}
