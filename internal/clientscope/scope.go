// Package clientscope decides which profiles each client may ask for and the
// per-client limits. Config lives in ~/.config/aimux/clients.yaml; a missing
// file means Defaults().
package clientscope

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// wildcard grants every profile. Only the cli client may hold it.
const wildcard = "*"

// cliClient is the only client allowed the wildcard.
const cliClient = "cli"

const (
	wingmanClient = "wingman"
	deviceClient  = "device"
)

type Scopes struct {
	Clients map[string]Client `yaml:"clients"`
}

type Client struct {
	Profiles []string `yaml:"profiles"`
	// Socket is an absolute path, or "" for no socket. Load expands a
	// leading ~/ to the home directory and refuses any other relative path.
	Socket         string   `yaml:"socket"`
	MaxSessions    int      `yaml:"max_sessions"`
	TurnsPerMinute int      `yaml:"turns_per_minute"`
	StepUpKinds    []string `yaml:"step_up_kinds"`
}

// DefaultPath is where the scopes file lives.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "aimux", "clients.yaml")
}

// Defaults returns the built-in scopes. Limit 0 means unlimited.
func Defaults() Scopes {
	home, _ := os.UserHomeDir()
	base := []string{"page", "research", "work-readonly", "research-with-work"}
	return Scopes{Clients: map[string]Client{
		"wingman": {
			Profiles:       append([]string(nil), base...),
			Socket:         filepath.Join(home, ".aimux", "clients", "wingman.sock"),
			MaxSessions:    2,
			TurnsPerMinute: 20,
		},
		"device": {
			Profiles:    append(append([]string(nil), base...), "coding"),
			StepUpKinds: []string{"secure-enclave"},
		},
		cliClient: {Profiles: []string{wildcard}},
	}}
}

// Allowed reports whether client may ask for profile. Unknown clients, and a
// nil Scopes, allow nothing.
func (s Scopes) Allowed(client, profile string) bool {
	c, ok := s.Clients[client]
	if !ok {
		return false
	}
	for _, p := range c.Profiles {
		if p == profile || (p == wildcard && client == cliClient) {
			return true
		}
	}
	return false
}

// Limit returns the client's session and turns-per-minute caps (0 = unlimited).
func (s Scopes) Limit(client string) (maxSessions, turnsPerMinute int) {
	c := s.Clients[client]
	return c.MaxSessions, c.TurnsPerMinute
}

// fileClient is a client entry as written in the file. Pointer and nil-able
// fields distinguish "unset" (keep the default) from an explicit value.
type fileClient struct {
	Profiles       []string `yaml:"profiles"`
	Socket         *string  `yaml:"socket"`
	MaxSessions    *int     `yaml:"max_sessions"`
	TurnsPerMinute *int     `yaml:"turns_per_minute"`
	StepUpKinds    []string `yaml:"step_up_kinds"`
}

// Load reads path. A missing file yields Defaults(). A file entry overrides only
// the fields it sets; unset fields keep that client's defaults, and clients not
// in the file keep theirs. Unknown fields are errors. Also rejected: the
// wildcard for any client but cli, negative limits, coding for wingman, zero
// limits for wingman, and an empty step_up_kinds for device.
func Load(path string) (Scopes, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the clients.yaml in the user's own aimux config dir
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return Scopes{}, err
	}
	var file struct {
		Clients map[string]fileClient `yaml:"clients"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return Scopes{}, fmt.Errorf("clientscope: %s: %w", path, err)
	}
	out := Defaults()
	for name, f := range file.Clients {
		c := out.Clients[name]
		if err := f.validate(name); err != nil {
			return Scopes{}, fmt.Errorf("clientscope: %s: %w", path, err)
		}
		if f.Profiles != nil {
			c.Profiles = f.Profiles
		}
		if f.Socket != nil {
			sock, err := socketPath(name, *f.Socket)
			if err != nil {
				return Scopes{}, fmt.Errorf("clientscope: %s: %w", path, err)
			}
			c.Socket = sock
		}
		if f.MaxSessions != nil {
			c.MaxSessions = *f.MaxSessions
		}
		if f.TurnsPerMinute != nil {
			c.TurnsPerMinute = *f.TurnsPerMinute
		}
		if f.StepUpKinds != nil {
			c.StepUpKinds = f.StepUpKinds
		}
		out.Clients[name] = c
	}
	return out, nil
}

// socketPath is a socket path from the file as an absolute path: a leading
// ~/ becomes the home directory. "" (no socket) stays "". Anything still
// relative is an error, since it would depend on serve's working directory.
func socketPath(client, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	p := raw
	if rest, ok := strings.CutPrefix(raw, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("client %q: socket %q: resolve home directory: %w", client, raw, err)
		}
		p = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("client %q: socket %q must be an absolute path (or start with ~/)", client, raw)
	}
	return filepath.Clean(p), nil
}

func (f fileClient) validate(name string) error {
	for _, p := range f.Profiles {
		if p == wildcard && name != cliClient {
			return fmt.Errorf("client %q may not use the %q wildcard", name, wildcard)
		}
		if p == "coding" && name == wingmanClient {
			return fmt.Errorf("client %q may not use profile %q", name, p)
		}
	}
	for field, v := range map[string]*int{"max_sessions": f.MaxSessions, "turns_per_minute": f.TurnsPerMinute} {
		if v == nil {
			continue
		}
		if *v < 0 {
			return fmt.Errorf("client %q: %s must not be negative", name, field)
		}
		if *v == 0 && name == wingmanClient {
			return fmt.Errorf("client %q: %s must be greater than 0 (limits may be raised, not disabled)", name, field)
		}
	}
	if name == deviceClient && f.StepUpKinds != nil && len(f.StepUpKinds) == 0 {
		return fmt.Errorf("client %q: step_up_kinds must not be empty", name)
	}
	return nil
}
