// Package devicelogin keeps the devices paired with the remote API and the
// pending pairing code. The CLI (pair, revoke) and the server (pairing,
// last seen) are separate processes sharing these files, so every write
// takes an exclusive flock, re-reads, changes and renames a temp file over
// the old one.
package devicelogin

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// Device kinds: an iPhone app's Secure Enclave key, or a web passkey.
const (
	KindSecureEnclave = "secure-enclave"
	KindWebAuthn      = "webauthn"
)

// Files in the store's directory (~/.aimux). remote-devices.json is not
// ours: it holds push registrations.
const (
	devicesFile = "remote-paired-devices.json"
	pendingFile = "remote-pairing.json"
	seenFile    = "remote-paired-seen.json"
	lockFile    = "remote-paired-devices.lock"
)

const maxNameLen = 32

// maxWrongCodes wrong codes in a row burn the pending code.
const maxWrongCodes = 5

// ErrNoDevice is returned by Remove for a name nobody paired.
var ErrNoDevice = errors.New("no paired device by that name")

// Device is a paired device. PublicKey is base64 SPKI DER; CredentialID is
// set for web passkeys; Login is the Tailscale login that paired it.
type Device struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	PublicKey    string    `json:"publicKey"`
	CredentialID string    `json:"credentialId,omitempty"`
	Login        string    `json:"login"`
	Created      time.Time `json:"created"`
}

// pending is the one outstanding pairing code: its hash, never the code.
type pending struct {
	Name     string    `json:"name"`
	CodeHash string    `json:"codeHash"` // hex SHA-256
	Expires  time.Time `json:"expires"`
	Attempts int       `json:"attempts"` // wrong codes so far; kept in the file so a restart does not reset it
}

// Store is the device files in one directory. It is safe for concurrent
// use, also by several Stores and processes on the same directory.
type Store struct {
	dir string
	now func() time.Time // tests set it

	mu        sync.Mutex // guards the Changed state below
	lastCheck time.Time
	lastStamp fileStamp
}

type fileStamp struct {
	mod  time.Time
	size int64
}

// Open returns the store kept in dir; nothing is read or created until used.
func Open(dir string) *Store {
	s := &Store{dir: dir, now: time.Now}
	s.lastStamp = s.stamp()
	return s
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// Devices returns the paired devices in the order they were paired.
// Writes rename a whole file into place, so reading needs no lock.
func (s *Store) Devices() ([]Device, error) {
	var ds []Device
	if err := s.read(devicesFile, &ds); err != nil {
		return nil, err
	}
	return ds, nil
}

// Add records a newly paired device. Its name (1-32 characters, no control
// characters) must be unused, ignoring case, and so must its id.
func (s *Store) Add(d Device) error {
	if err := validName(d.Name); err != nil {
		return err
	}
	if d.ID == "" {
		return errors.New("a paired device needs an id")
	}
	if d.Kind != KindSecureEnclave && d.Kind != KindWebAuthn {
		return fmt.Errorf("unknown device kind %q", d.Kind)
	}
	return s.locked(func() error {
		var ds []Device
		if err := s.read(devicesFile, &ds); err != nil {
			return err
		}
		for _, o := range ds {
			if strings.EqualFold(o.Name, d.Name) {
				return fmt.Errorf("a device named %q is already paired", o.Name)
			}
			if o.ID == d.ID {
				return fmt.Errorf("device id %s is already paired", d.ID)
			}
		}
		return s.write(devicesFile, append(ds, d))
	})
}

// Remove unpairs the device with this name and returns it.
func (s *Store) Remove(name string) (Device, error) {
	var removed Device
	err := s.locked(func() error {
		var ds []Device
		if err := s.read(devicesFile, &ds); err != nil {
			return err
		}
		for i, d := range ds {
			if d.Name == name {
				removed = d
				return s.write(devicesFile, append(ds[:i:i], ds[i+1:]...))
			}
		}
		return fmt.Errorf("%w: %q", ErrNoDevice, name)
	})
	return removed, err
}

// SetPending makes codeHash the one pairing code, for a device to be called
// name, until expires. It replaces any earlier code and refuses a name that
// is invalid or already paired (ignoring case).
func (s *Store) SetPending(name string, codeHash [32]byte, expires time.Time) error {
	if err := validName(name); err != nil {
		return err
	}
	return s.locked(func() error {
		var ds []Device
		if err := s.read(devicesFile, &ds); err != nil {
			return err
		}
		for _, d := range ds {
			if strings.EqualFold(d.Name, name) {
				return fmt.Errorf("a device named %q is already paired (revoke it first, or choose another name)", d.Name)
			}
		}
		return s.write(pendingFile, pending{Name: name, CodeHash: hex.EncodeToString(codeHash[:]), Expires: expires.UTC()})
	})
}

// TakePending uses up the pairing code if codeHash is it and it has not
// expired at now, returning the name it was made for. A wrong code counts
// against it, and the 5th wrong code in a row deletes it, as does expiry.
func (s *Store) TakePending(codeHash [32]byte, now time.Time) (name string, ok bool, err error) {
	err = s.locked(func() error {
		var p *pending
		if err := s.read(pendingFile, &p); err != nil || p == nil {
			return err
		}
		want, err := hex.DecodeString(p.CodeHash)
		if err != nil || len(want) != len(codeHash) {
			return fmt.Errorf("read %s: the code hash is not 32 bytes of hex", s.path(pendingFile))
		}
		if !now.Before(p.Expires) {
			return s.remove(pendingFile)
		}
		if subtle.ConstantTimeCompare(want, codeHash[:]) != 1 {
			if p.Attempts++; p.Attempts >= maxWrongCodes {
				return s.remove(pendingFile)
			}
			return s.write(pendingFile, p)
		}
		if err := s.remove(pendingFile); err != nil {
			return err
		}
		name, ok = p.Name, true
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return name, ok, nil
}

// Seen records that device id was used at t. Write errors are ignored on
// purpose: last seen is shown by `aimux remote devices` and decides nothing.
func (s *Store) Seen(id string, t time.Time) {
	_ = s.locked(func() error {
		seen := map[string]time.Time{}
		if err := s.read(seenFile, &seen); err != nil || seen == nil {
			seen = map[string]time.Time{} // unreadable: start over, it is display only
		}
		seen[id] = t.UTC()
		return s.write(seenFile, seen)
	})
}

// LastSeen returns when each device id was last used; an unreadable file
// reads as empty, since it is display only.
func (s *Store) LastSeen() map[string]time.Time {
	seen := map[string]time.Time{}
	if err := s.read(seenFile, &seen); err != nil || seen == nil {
		return map[string]time.Time{}
	}
	return seen
}

// Changed reports whether the devices file was written since Changed last
// looked (or since Open). It looks at most once a second; calls in between
// report false.
func (s *Store) Changed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.lastCheck.IsZero() && now.Sub(s.lastCheck) < time.Second {
		return false
	}
	s.lastCheck = now
	st := s.stamp()
	changed := st != s.lastStamp
	s.lastStamp = st
	return changed
}

// Recheck makes the next Changed (a second later at most) report a change,
// so a read that failed is tried again although the file did not change.
func (s *Store) Recheck() {
	s.mu.Lock()
	s.lastStamp = fileStamp{mod: time.Unix(0, 0)} // matches no real file, nor a missing one
	s.mu.Unlock()
}

func (s *Store) stamp() fileStamp {
	fi, err := os.Stat(s.path(devicesFile))
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size()}
}

func validName(name string) error {
	n := utf8.RuneCountInString(name)
	if n == 0 || n > maxNameLen || !utf8.ValidString(name) {
		return fmt.Errorf("a device name is 1 to %d characters", maxNameLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) { // Cf: invisible and bidi marks such as U+202E
			return errors.New("a device name cannot hold control or invisible formatting characters")
		}
	}
	return nil
}

// locked runs fn holding an exclusive flock on the lock file. flock locks
// belong to the open file, so two Stores in one process exclude each other
// as two processes do.
func (s *Store) locked(fn func() error) error {
	// 0700 applies only when this creates the directory; an existing ~/.aimux
	// keeps its mode (others create it 0750) and the 0600 files are the boundary.
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	f, err := os.OpenFile(s.path(lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open the device lock: %w", err)
	}
	defer func() { _ = f.Close() }() // closing releases the lock
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX) // #nosec G115 -- a file descriptor fits in an int
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("lock %s: %w", s.path(lockFile), err)
	}
	return fn()
}

// read decodes file into v; a missing file leaves v untouched.
func (s *Store) read(file string, v any) error {
	b, err := os.ReadFile(s.path(file))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path(file), err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("read %s: %w", s.path(file), err)
	}
	return nil
}

// write replaces file with v through a 0600 temp file in the same directory.
func (s *Store) write(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("save %s: %w", s.path(file), err)
	}
	tmp, err := os.CreateTemp(s.dir, "."+file+".*.tmp") // CreateTemp makes it 0600
	if err != nil {
		return fmt.Errorf("save %s: %w", s.path(file), err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("save %s: %w", s.path(file), err)
	}
	if err := tmp.Sync(); err != nil { // on disk before the rename makes it the file
		_ = tmp.Close()
		return fmt.Errorf("save %s: %w", s.path(file), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save %s: %w", s.path(file), err)
	}
	if err := os.Rename(tmp.Name(), s.path(file)); err != nil {
		return fmt.Errorf("save %s: %w", s.path(file), err)
	}
	return nil
}

func (s *Store) remove(file string) error {
	if err := os.Remove(s.path(file)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", s.path(file), err)
	}
	return nil
}
