package push

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// APNs environments: a build from Xcode gets sandbox tokens, a TestFlight
// or App Store build gets production ones, and each works only with its own
// Apple server.
const (
	EnvSandbox    = "sandbox"
	EnvProduction = "production"
)

// ErrBadDevice rejects a registration that cannot be an APNs device.
var ErrBadDevice = errors.New("not an APNs device token and environment")

// Device is a phone that asked for approval notifications.
type Device struct {
	Token       string    `json:"token"` // lower-case hex
	Environment string    `json:"environment"`
	Login       string    `json:"login"` // the Tailscale login that registered it
	Added       time.Time `json:"added"`
}

// DeviceStore keeps registered devices in a JSON file, so they survive a
// restart of the server.
type DeviceStore struct {
	Path string
	mu   sync.Mutex
}

// Add registers d, replacing an earlier registration of the same token.
func (s *DeviceStore) Add(d Device) error {
	d.Token = strings.ToLower(d.Token)
	if !validToken(d.Token) || (d.Environment != EnvSandbox && d.Environment != EnvProduction) {
		return ErrBadDevice
	}
	d.Added = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	ds, err := s.read()
	if err != nil {
		return err
	}
	for i := range ds {
		if ds[i].Token == d.Token {
			ds[i] = d
			return s.write(ds)
		}
	}
	return s.write(append(ds, d))
}

// Remove forgets a token; an unknown one is not an error.
func (s *DeviceStore) Remove(token string) error {
	token = strings.ToLower(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	ds, err := s.read()
	if err != nil {
		return err
	}
	kept := ds[:0]
	for _, d := range ds {
		if d.Token != token {
			kept = append(kept, d)
		}
	}
	if len(kept) == len(ds) {
		return nil
	}
	return s.write(kept)
}

// List returns the registered devices.
func (s *DeviceStore) List() ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

func (s *DeviceStore) read() ([]Device, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read push devices: %w", err)
	}
	var ds []Device
	if err := json.Unmarshal(b, &ds); err != nil {
		return nil, fmt.Errorf("read push devices %s: %w", s.Path, err)
	}
	return ds, nil
}

func (s *DeviceStore) write(ds []Device) error {
	b, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("save push devices: %w", err)
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("save push devices: %w", err)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		return fmt.Errorf("save push devices: %w", err)
	}
	return nil
}

// validToken accepts the hex form of an APNs device token: 32 bytes today,
// documented by Apple as variable length, so up to 100 bytes.
func validToken(t string) bool {
	if len(t) < 64 || len(t) > 200 || len(t)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(t)
	return err == nil
}
