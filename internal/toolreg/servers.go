package toolreg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	return parseInventory(path, data)
}

// parseInventory is LoadInventory on data already read.
func parseInventory(path string, data []byte) (Inventory, error) {
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
		if key := unknownField(&node, reflect.TypeOf(Server{})); key != "" {
			inv.Errors[name] = fmt.Errorf("servers %s: server %q (line %d): unknown field %q", path, name, node.Line, key)
			continue
		}
		if err := node.Decode(&s); err != nil { // resolves aliases
			// The decoder's text can quote a field's value, which may be a
			// literal secret: report only where it failed.
			inv.Errors[name] = fmt.Errorf("servers %s: server %q (line %d): a field has the wrong type", path, name, node.Line)
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
	if s.URL != "" {
		u, err := url.Parse(s.URL)
		if err != nil || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) || u.Host == "" {
			return errors.New("url is not an http or https address with a host")
		}
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

// splitMode reports whether servers.yaml exists in dir; then
// tool-sources.yaml is not read. MigrateSplit writes it last, so a crash
// before it leaves the old file in charge.
func splitMode(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, serversFile))
	return err == nil
}

// loadSplit builds the gateway's Registry from servers.yaml and
// tool-sets.yaml in dir. Only servers a set references become Sources: a
// server no set names is local-only and never reaches the sandbox gateway.
func loadSplit(dir string, setsData []byte) (Registry, error) {
	return loadSplitData(dir, nil, setsData)
}

// loadSplitData is loadSplit with servers.yaml's and tool-sets.yaml's
// content given (nil: read from dir), so a migration can check its result
// before writing anything.
func loadSplitData(dir string, serversData, setsData []byte) (Registry, error) {
	inv := Inventory{Servers: map[string]Server{}, Errors: map[string]error{}}
	serversPath := filepath.Join(dir, serversFile)
	if serversData == nil {
		if _, err := os.Lstat(serversPath); err == nil {
			b, err := os.ReadFile(serversPath) //nolint:gosec // G304: the user's own servers.yaml
			if err != nil {
				return Registry{}, fmt.Errorf("read servers %s: %w", serversPath, err)
			}
			serversData = b
		}
	}
	if serversData != nil {
		var err error
		if inv, err = parseInventory(serversPath, serversData); err != nil {
			return Registry{}, err
		}
	}
	setsPath := filepath.Join(dir, setsFile)
	if setsData == nil {
		b, err := os.ReadFile(setsPath) //nolint:gosec // G304: the user's own tool-sets.yaml
		if errors.Is(err, fs.ErrNotExist) {
			if _, lerr := os.Lstat(setsPath); lerr == nil {
				return Registry{}, fmt.Errorf("read tool sets %s: it is a link to a missing file", setsPath)
			}
		} else if err != nil {
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
	r := Registry{Version: 1, Sources: map[string]Source{}, Sets: map[string]Set{}, SourceErrors: map[string]error{}, SetErrors: map[string]error{}, Identities: map[string]string{}}
	ids := map[string]string{} // each server hashed once
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
		if floating, why := Floating(s.Command); floating {
			r.SetErrors[name] = fmt.Errorf("tool sets %s: set %q: server %q: %s", setsPath, name, server, why)
			continue
		}
		if s.Transport() == "http" || s.Transport() == "managed-http" {
			id, ok := ids[server]
			if !ok {
				var err error
				if id, err = s.Identity(HashFile); err != nil {
					// Held, not served: approvals are bound to an identity.
					r.SetErrors[name] = fmt.Errorf("tool sets %s: set %q: server %q: identity: %w", setsPath, name, server, err)
					continue
				}
				ids[server] = id
			}
			if s.Transport() == "managed-http" {
				r.SetErrors[name] = fmt.Errorf("tool sets %s: set %q: server %q: %s", setsPath, name, server, ManagedHeldReason)
				continue
			}
			r.Identities[server] = id
		}
		r.Sources[server] = sourceOf(s)
		r.Sets[name] = Set{Source: server, Tools: e.Tools}
	}
	r = validated(setsPath, r)
	used := map[string]bool{}
	for _, set := range r.Sets {
		used[set.Source] = true
	}
	for name := range r.Sources {
		if !used[name] { // named only by sets that were rejected
			delete(r.Sources, name)
			delete(r.Identities, name)
		}
	}
	return r, nil
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
		return sanitized(err)
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

var yamlLineRE = regexp.MustCompile(`line (\d+)`)

// sanitized is a YAML decode error without the values the decoder quotes
// (a value may be a literal secret): only the line numbers are kept.
func sanitized(err error) error {
	var lines []string
	for _, m := range yamlLineRE.FindAllStringSubmatch(err.Error(), -1) {
		lines = append(lines, m[1])
	}
	if len(lines) == 0 {
		return errors.New("not valid YAML for this file")
	}
	return fmt.Errorf("unknown field or wrong type at line %s", strings.Join(lines, ", "))
}

// unknownField is the dotted path of the first mapping key, at any depth,
// that t's yaml tags do not name (aliases followed), or "".
func unknownField(n *yaml.Node, t reflect.Type) string {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t.Kind() == reflect.Map && n.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if p := unknownField(n.Content[i+1], t.Elem()); p != "" {
				return n.Content[i].Value + "." + p
			}
		}
	case t.Kind() == reflect.Struct && n.Kind == yaml.MappingNode && t != reflect.TypeOf(time.Duration(0)):
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if k == "<<" {
				continue
			}
			ft, ok := fields[k]
			if !ok {
				return k
			}
			if p := unknownField(n.Content[i+1], ft); p != "" {
				return k + "." + p
			}
		}
	}
	return ""
}
