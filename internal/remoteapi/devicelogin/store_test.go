package devicelogin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func device(name string) Device {
	return Device{ID: "id-" + name, Name: name, Kind: KindSecureEnclave, PublicKey: "pk-" + name, Login: "me@example.com",
		Created: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
}

func newDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".aimux") // not there yet: the store creates it
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestStore_FileModes(t *testing.T) {
	dir := newDir(t)
	s := Open(dir)
	if err := s.Add(device("phone")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPending("laptop", sha256.Sum256([]byte("X")), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.Seen("id-phone", time.Now())
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("dir mode %o, want 700", m)
	}
	for _, f := range []string{devicesFile, pendingFile, seenFile, lockFile} {
		if m := mode(t, filepath.Join(dir, f)); m != 0o600 {
			t.Errorf("%s mode %o, want 600", f, m)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 4 {
		t.Errorf("dir holds %d entries, want the 4 files and no leftover temp file", len(entries))
	}
}

func TestStore_AddAndDevices(t *testing.T) {
	s := Open(newDir(t))
	got, err := s.Devices()
	if err != nil || len(got) != 0 {
		t.Fatalf("empty store: %v, %v", got, err)
	}
	web := device("browser")
	web.Kind, web.CredentialID = KindWebAuthn, "cred-1"
	for _, d := range []Device{device("phone"), web} {
		if err := s.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	got, err = Open(s.dir).Devices() // a second store reads what the first wrote
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != device("phone") || got[1] != web {
		t.Fatalf("devices = %+v", got)
	}
}

func TestStore_AddRejects(t *testing.T) {
	s := Open(newDir(t))
	if err := s.Add(device("phone")); err != nil {
		t.Fatal(err)
	}
	sameID := device("other")
	sameID.ID = "id-phone"
	badKind := device("tablet")
	badKind.Kind = "password"
	noID := device("watch")
	noID.ID = ""
	cases := map[string]Device{
		"duplicate name":    device("phone"),
		"empty name":        device(""),
		"33 characters":     device(strings.Repeat("a", 33)),
		"control character": device("ph\x1bone"),
		"duplicate id":      sameID,
		"unknown kind":      badKind,
		"missing id":        noID,
	}
	for what, d := range cases {
		if err := s.Add(d); err == nil {
			t.Errorf("%s: Add accepted %+v", what, d)
		}
	}
	got, _ := s.Devices()
	if len(got) != 1 {
		t.Fatalf("a rejected Add changed the file: %+v", got)
	}
}

func TestStore_AddNameBoundaries(t *testing.T) {
	s := Open(newDir(t))
	for _, name := range []string{"a", strings.Repeat("b", 32), strings.Repeat("é", 32)} {
		d := device(name)
		if err := s.Add(d); err != nil {
			t.Errorf("Add(%q): %v", name, err)
		}
	}
}

func TestStore_Remove(t *testing.T) {
	s := Open(newDir(t))
	for _, n := range []string{"phone", "browser"} {
		if err := s.Add(device(n)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Remove("phone")
	if err != nil || got != device("phone") {
		t.Fatalf("Remove = %+v, %v", got, err)
	}
	left, _ := s.Devices()
	if len(left) != 1 || left[0].Name != "browser" {
		t.Fatalf("left %+v", left)
	}
	if _, err := s.Remove("phone"); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("second Remove: %v, want ErrNoDevice", err)
	}
	if _, err := Open(newDir(t)).Remove("x"); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("Remove on an empty store: %v, want ErrNoDevice", err)
	}
}

func TestStore_CorruptDevicesFileIsAnErrorNamingIt(t *testing.T) {
	dir := newDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, devicesFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir)
	if _, err := s.Devices(); err == nil || !strings.Contains(err.Error(), devicesFile) {
		t.Fatalf("Devices: %v, want an error naming %s", err, devicesFile)
	}
	if err := s.Add(device("phone")); err == nil {
		t.Fatal("Add over a corrupt file must fail, not overwrite it")
	}
}

func TestStore_TakePending(t *testing.T) {
	s := Open(newDir(t))
	hash := sha256.Sum256([]byte("ABCD1234"))
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	if err := s.SetPending("phone", hash, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.TakePending(sha256.Sum256([]byte("WRONG")), now); ok || err != nil {
		t.Fatalf("wrong code: ok=%v err=%v", ok, err)
	}
	name, ok, err := s.TakePending(hash, now) // one wrong code does not burn it
	if !ok || err != nil || name != "phone" {
		t.Fatalf("TakePending = %q, %v, %v", name, ok, err)
	}
	if _, ok, _ := s.TakePending(hash, now); ok {
		t.Fatal("a pairing code worked twice")
	}
}

func TestStore_TakePendingExpiry(t *testing.T) {
	s := Open(newDir(t))
	hash := sha256.Sum256([]byte("ABCD1234"))
	expires := time.Date(2026, 10, 28, 9, 5, 0, 0, time.UTC)
	if err := s.SetPending("phone", hash, expires); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TakePending(hash, expires); ok {
		t.Fatal("a code was taken at its expiry instant")
	}
	if err := s.SetPending("phone", hash, expires); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TakePending(hash, expires.Add(-time.Nanosecond)); !ok {
		t.Fatal("a code was refused just before its expiry")
	}
}

func TestStore_SetPendingReplacesTheEarlierCode(t *testing.T) {
	s := Open(newDir(t))
	first, second := sha256.Sum256([]byte("FIRST")), sha256.Sum256([]byte("SECOND"))
	now := time.Now()
	if err := s.SetPending("phone", first, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPending("laptop", second, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TakePending(first, now); ok {
		t.Fatal("the replaced code still works")
	}
	if name, ok, _ := s.TakePending(second, now); !ok || name != "laptop" {
		t.Fatalf("got %q %v", name, ok)
	}
}

func TestStore_SetPendingStoresOnlyTheHash(t *testing.T) {
	dir := newDir(t)
	hash := sha256.Sum256([]byte("ABCD1234"))
	if err := Open(dir).SetPending("phone", hash, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, pendingFile)) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ABCD1234") || !strings.Contains(string(b), hex.EncodeToString(hash[:])) {
		t.Fatalf("pairing file %s", b)
	}
}

func TestStore_SetPendingRejectsANameInUseOrInvalid(t *testing.T) {
	s := Open(newDir(t))
	if err := s.Add(device("phone")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"phone", "", strings.Repeat("a", 33)} {
		if err := s.SetPending(name, sha256.Sum256([]byte("X")), time.Now().Add(time.Minute)); err == nil {
			t.Errorf("SetPending(%q) accepted", name)
		}
	}
}

func TestStore_CorruptPendingFileIsAnError(t *testing.T) {
	dir := newDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pendingFile), []byte(`{"codeHash":"zz"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Open(dir).TakePending(sha256.Sum256([]byte("X")), time.Now()); ok || err == nil {
		t.Fatalf("ok=%v err=%v, want an error", ok, err)
	}
}

// The CLI revokes while the server records a pairing: both effects stay.
func TestStore_ConcurrentRemoveAndAddKeepBoth(t *testing.T) {
	dir := newDir(t)
	cli, server := Open(dir), Open(dir)
	const n = 20
	for i := range n {
		if err := cli.Add(device(fmt.Sprintf("old-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range n {
			if _, err := cli.Remove(fmt.Sprintf("old-%d", i)); err != nil {
				errs <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range n {
			if err := server.Add(device(fmt.Sprintf("new-%d", i))); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	got, err := Open(dir).Devices()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("%d devices left, want the %d new ones", len(got), n)
	}
	for _, d := range got {
		if !strings.HasPrefix(d.Name, "new-") {
			t.Fatalf("a revoked device came back: %s", d.Name)
		}
	}
}

func TestStore_SeenAndLastSeen(t *testing.T) {
	dir := newDir(t)
	s := Open(dir)
	if got := s.LastSeen(); len(got) != 0 {
		t.Fatalf("LastSeen on an empty store = %v", got)
	}
	t1 := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	s.Seen("id-phone", t1)
	s.Seen("id-browser", t1)
	s.Seen("id-phone", t1.Add(time.Hour))
	got := Open(dir).LastSeen()
	if len(got) != 2 || !got["id-phone"].Equal(t1.Add(time.Hour)) || !got["id-browser"].Equal(t1) {
		t.Fatalf("LastSeen = %v", got)
	}
}

func TestStore_SeenWriteFailureIsIgnored(t *testing.T) {
	dir := newDir(t)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("a file where the directory should be"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir)
	s.Seen("id-phone", time.Now()) // must not panic
	if got := s.LastSeen(); len(got) != 0 {
		t.Fatalf("LastSeen = %v", got)
	}
}

func TestStore_ChangedSeesAnotherStoresWrite(t *testing.T) {
	dir := newDir(t)
	clock := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	server := Open(dir)
	server.now = func() time.Time { return clock }
	if server.Changed() {
		t.Fatal("Changed before anything happened")
	}
	if err := Open(dir).Add(device("phone")); err != nil { // the CLI pairs
		t.Fatal(err)
	}
	clock = clock.Add(500 * time.Millisecond)
	if server.Changed() {
		t.Fatal("Changed looked again within a second")
	}
	clock = clock.Add(600 * time.Millisecond)
	if !server.Changed() {
		t.Fatal("Changed missed a device added by another store")
	}
	clock = clock.Add(2 * time.Second)
	if server.Changed() {
		t.Fatal("Changed reported the same change twice")
	}
	if _, err := Open(dir).Remove("phone"); err != nil { // the CLI revokes
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Second)
	if !server.Changed() {
		t.Fatal("Changed missed a revoke by another store")
	}
}

func TestStore_TakePendingBurnsAfterFiveWrongCodes(t *testing.T) {
	right, wrong := sha256.Sum256([]byte("RIGHT")), sha256.Sum256([]byte("WRONG"))
	now := time.Now()
	for _, tt := range []struct {
		wrongTries int
		want       bool
	}{{4, true}, {5, false}} {
		dir := newDir(t)
		if err := Open(dir).SetPending("phone", right, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		for range tt.wrongTries {
			// a fresh Store each time: a server restart must not reset the count
			if _, ok, err := Open(dir).TakePending(wrong, now); ok || err != nil {
				t.Fatalf("wrong code: ok=%v err=%v", ok, err)
			}
		}
		name, ok, err := Open(dir).TakePending(right, now)
		if err != nil || ok != tt.want {
			t.Fatalf("after %d wrong codes the right one gave ok=%v err=%v, want ok=%v", tt.wrongTries, ok, err, tt.want)
		}
		if ok && name != "phone" {
			t.Fatalf("name %q", name)
		}
		if _, err := os.Stat(filepath.Join(dir, pendingFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("after %d wrong codes the pairing file is still there (%v)", tt.wrongTries, err)
		}
	}
}

func TestStore_SetPendingResetsTheWrongCount(t *testing.T) {
	dir := newDir(t)
	s := Open(dir)
	right, wrong := sha256.Sum256([]byte("RIGHT")), sha256.Sum256([]byte("WRONG"))
	now := time.Now()
	if err := s.SetPending("phone", sha256.Sum256([]byte("OLD")), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		_, _, _ = s.TakePending(wrong, now)
	}
	if err := s.SetPending("phone", right, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, _, _ = s.TakePending(wrong, now) // 1 against the new code, not 5
	if _, ok, _ := s.TakePending(right, now); !ok {
		t.Fatal("a new code inherited the old code's wrong tries")
	}
}

func TestStore_NameRejectsFormatCharacters(t *testing.T) {
	s := Open(newDir(t))
	for _, name := range []string{"phone\u202e", "\u200bphone", "ph\u2066one"} {
		if err := s.Add(device(name)); err == nil {
			t.Errorf("Add(%q) accepted a Unicode format character", name)
		}
		if err := s.SetPending(name, sha256.Sum256([]byte("X")), time.Now().Add(time.Minute)); err == nil {
			t.Errorf("SetPending(%q) accepted a Unicode format character", name)
		}
	}
}

// Names are unique without regard to case: "iPhone" and "iphone" would be
// two devices nobody can tell apart in `aimux remote devices`.
func TestStore_NamesUniqueIgnoringCase(t *testing.T) {
	s := Open(newDir(t))
	if err := s.Add(device("iPhone")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"iphone", "IPHONE", "iPhone"} {
		d := device(name)
		d.ID = "other-" + name
		if err := s.Add(d); err == nil {
			t.Errorf("Add(%q) accepted next to iPhone", name)
		}
		if err := s.SetPending(name, sha256.Sum256([]byte("X")), time.Now().Add(time.Minute)); err == nil {
			t.Errorf("SetPending(%q) accepted next to iPhone", name)
		}
	}
	other := device("iPad")
	other.ID = "ipad"
	if err := s.Add(other); err != nil {
		t.Fatalf("a different name is refused: %v", err)
	}
}
