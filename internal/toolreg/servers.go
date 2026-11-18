package toolreg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zanetworker/aimux/internal/secretref"
)

// Split files: servers.yaml says what exists and how to start or reach it;
// tool-sets.yaml says what sandboxes may use. Nothing in servers.yaml grants
// a sandbox anything.
const (
	serversFile = "servers.yaml"
	setsFile    = "tool-sets.yaml"
)

// ServersPath is ~/.config/aimux/servers.yaml.
func ServersPath(home string) string {
	return filepath.Join(home, ".config", "aimux", serversFile)
}

// SetsPath is ~/.config/aimux/tool-sets.yaml.
func SetsPath(home string) string { return filepath.Join(home, ".config", "aimux", setsFile) }

// Server is one MCP server (or a built-in source kind) in servers.yaml. Its
// transport follows its fields: command only is stdio, url only is remote
// HTTP, command and url is managed HTTP (aimux starts it, then connects).
type Server struct {
	Command       []string          `yaml:"command,omitempty,flow"`
	Cwd           string            `yaml:"cwd,omitempty"`
	URL           string            `yaml:"url,omitempty"`
	Env           map[string]string `yaml:"env,omitempty"`
	Secrets       secretref.Refs    `yaml:"secrets,omitempty"`
	KeepRunning   bool              `yaml:"keep_running,omitempty"`
	AdoptExisting bool              `yaml:"adopt_existing,omitempty"`
	Ready         *ReadyProbe       `yaml:"ready,omitempty"`
	// Kind is a built-in source kind (local-notes, cli, http-openapi), or
	// mcp-http or empty for an MCP server.
	Kind       string `yaml:"type,omitempty"`
	Root       string `yaml:"root,omitempty"`
	Pack       string `yaml:"pack,omitempty"`
	Spec       string `yaml:"spec,omitempty"`
	BaseURL    string `yaml:"base_url,omitempty"`
	Credential string `yaml:"credential,omitempty"`
	Authorship string `yaml:"authorship,omitempty"`
}

// ReadyProbe is how a managed server shows it is up: an MCP initialize POST
// to its url (mcp, the default) or a TCP connect.
type ReadyProbe struct {
	Type    string        `yaml:"type,omitempty"`
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// Transport is stdio, http, managed-http, or the built-in kind.
func (s Server) Transport() string {
	switch s.Kind {
	case TypeLocalNotes, TypeCLI, TypeHTTPOpenAPI:
		return s.Kind
	}
	switch {
	case len(s.Command) > 0 && s.URL != "":
		return "managed-http"
	case len(s.Command) > 0:
		return "stdio"
	case s.URL != "":
		return "http"
	}
	return ""
}

// Inventory is a loaded servers.yaml. A malformed server is left out with
// its reason in Errors, so one bad entry leaves the rest working.
type Inventory struct {
	Version int               `yaml:"version"`
	Servers map[string]Server `yaml:"servers"`
	Errors  map[string]error  `yaml:"-"`
}

// LoadInventory reads servers.yaml at path.
func LoadInventory(path string) (Inventory, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the user's own servers.yaml
	if err != nil {
		return Inventory{}, fmt.Errorf("read servers %s: %w", path, err)
	}
	var raw struct {
		Version int                  `yaml:"version"`
		Servers map[string]yaml.Node `yaml:"servers"`
	}
	if err := singleDoc(data, &raw, true); err != nil {
		return Inventory{}, fmt.Errorf("servers %s: %w", path, err)
	}
	if raw.Version != 1 {
		return Inventory{}, fmt.Errorf("servers %s: version must be 1, got %d", path, raw.Version)
	}
	inv := Inventory{Version: 1, Servers: map[string]Server{}, Errors: map[string]error{}}
	for name, node := range raw.Servers {
		var s Server
		b, _ := yaml.Marshal(&node)
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
			// The decoder's text can quote a field's value, which may be a
			// literal secret: report only where it failed.
			inv.Errors[name] = fmt.Errorf("servers %s: server %q (line %d): a field is unknown or has the wrong type", path, name, node.Line)
			continue
		}
		if err := checkServer(name, s); err != nil {
			inv.Errors[name] = fmt.Errorf("servers %s: server %q: %w", path, name, err)
			continue
		}
		inv.Servers[name] = s
	}
	return inv, nil
}

func checkServer(name string, s Server) error {
	if !safeName.MatchString(name) {
		return fmt.Errorf("name may hold only letters, digits, _ and -")
	}
	if s.Kind != "" && s.Kind != TypeMCPHTTP && s.Transport() != s.Kind {
		return fmt.Errorf("unknown type %q", s.Kind)
	}
	if s.Transport() == "" {
		return errors.New("needs a command, a url, or a type")
	}
	if len(s.Command) > 0 && s.Command[0] == "" {
		return errors.New("command starts with an empty program")
	}
	if s.URL != "" && !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
		return fmt.Errorf("url %q is not http or https", s.URL)
	}
	for _, part := range []map[string]secretref.Ref{s.Secrets.Env, s.Secrets.Headers} {
		for n, ref := range part {
			if err := ref.Validate(); err != nil {
				return fmt.Errorf("secret %s: %w", n, err)
			}
		}
	}
	return nil
}

// setEntry is a tool-sets.yaml set; server names its server ("source" is
// accepted too, as tool-sources.yaml wrote it).
type setEntry struct {
	Server string   `yaml:"server,omitempty"`
	Source string   `yaml:"source,omitempty"`
	Tools  []string `yaml:"tools,flow"`
}

// splitMode reports whether the directory holds servers.yaml or
// tool-sets.yaml; then tool-sources.yaml is not read.
func splitMode(dir string) bool {
	for _, f := range []string{serversFile, setsFile} {
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

// loadSplit builds the gateway's Registry from servers.yaml and
// tool-sets.yaml in dir. Only servers a set references become Sources: a
// server no set names is local-only and never reaches the sandbox gateway.
func loadSplit(dir string, setsData []byte) (Registry, error) {
	inv := Inventory{Servers: map[string]Server{}, Errors: map[string]error{}}
	if _, err := os.Lstat(filepath.Join(dir, serversFile)); err == nil {
		var err error
		if inv, err = LoadInventory(filepath.Join(dir, serversFile)); err != nil {
			return Registry{}, err
		}
	}
	setsPath := filepath.Join(dir, setsFile)
	if setsData == nil {
		b, err := os.ReadFile(setsPath) //nolint:gosec // G304: the user's own tool-sets.yaml
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Registry{}, fmt.Errorf("read tool sets %s: %w", setsPath, err)
		}
		setsData = b
	}
	var raw struct {
		Version int                 `yaml:"version"`
		Sets    map[string]setEntry `yaml:"sets"`
	}
	raw.Version = 1
	if len(bytes.TrimSpace(setsData)) > 0 {
		if err := singleDoc(setsData, &raw, true); err != nil {
			return Registry{}, fmt.Errorf("tool sets %s: %w", setsPath, err)
		}
		if raw.Version != 1 {
			return Registry{}, fmt.Errorf("tool sets %s: version must be 1, got %d", setsPath, raw.Version)
		}
	}
	r := Registry{Version: 1, Sources: map[string]Source{}, Sets: map[string]Set{}, SourceErrors: map[string]error{}, SetErrors: map[string]error{}}
	for name, e := range raw.Sets {
		server := e.Server
		if server == "" {
			server = e.Source
		}
		s, ok := inv.Servers[server]
		switch {
		case !ok && inv.Errors[server] != nil:
			r.SetErrors[name] = fmt.Errorf("tool sets %s: set %q: server %q is broken: %w", setsPath, name, server, inv.Errors[server])
			continue
		case !ok:
			r.SetErrors[name] = fmt.Errorf("tool sets %s: set %q: no server %q in servers.yaml", setsPath, name, server)
			continue
		case s.Transport() == "stdio":
			r.SetErrors[name] = fmt.Errorf("tool sets %s: set %q: server %q is stdio; a sandbox set needs an HTTP server", setsPath, name, server)
			continue
		}
		r.Sources[server] = sourceOf(s)
		r.Sets[name] = Set{Source: server, Tools: e.Tools}
	}
	return validated(setsPath, r), nil
}

// sourceOf is the gateway's view of a server a set references.
func sourceOf(s Server) Source {
	switch s.Kind {
	case TypeLocalNotes, TypeCLI, TypeHTTPOpenAPI:
		return Source{Type: s.Kind, Root: s.Root, Pack: s.Pack, Spec: s.Spec, BaseURL: s.BaseURL, Credential: s.Credential, Authorship: s.Authorship}
	}
	return Source{Type: TypeMCPHTTP, URL: s.URL, Authorship: s.Authorship}
}

func singleDoc(data []byte, into any, known bool) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(known)
	if err := dec.Decode(into); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("holds more than one YAML document")
	}
	return nil
}

var legacyNoteOnce sync.Once

// LegacyNote is told once per process that tool-sources.yaml is read.
var LegacyNote = func(path string) {
	_, _ = fmt.Fprintf(os.Stderr, "%s is read for this release; run aimux servers migrate\n", path)
}
