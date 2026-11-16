// Package toolreg is the tool-source registry, ~/.config/aimux/tool-sources.yaml:
// the sources aimux may reach (MCP servers over HTTP, local notes folders)
// and the reviewed tool sets each exposes. It owns loading, validation and
// the one-time migration from ~/.aimux/wingman/readonly.json. One file holds
// both, so an endpoint cannot be paired with the wrong review.
package toolreg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source types.
const (
	TypeMCPHTTP    = "mcp-http"    // an MCP server reached over HTTP at URL
	TypeLocalNotes = "local-notes" // Markdown files under Root, served in process
)

// Source is one place tools come from. Its name in Registry.Sources is the
// server name the gateway serves it under, so pins, session tokens, sandbox
// endpoints (/mcp/<name>) and harness tool names (mcp__<name>__*) all key on
// it; renaming a source is a new server.
type Source struct {
	Type string `yaml:"type"`
	URL  string `yaml:"url,omitempty"`  // mcp-http only
	Root string `yaml:"root,omitempty"` // local-notes only: an absolute directory
}

// Set is a reviewed tool set: exact tool names a source may expose.
type Set struct {
	Source string   `yaml:"source"`
	Tools  []string `yaml:"tools,flow"`
}

// Registry is a loaded tool-sources.yaml. Load drops each malformed source
// or set into SourceErrors or SetErrors, by name, with the reason, so one
// bad entry leaves the rest working; a set on a dropped source is dropped
// too.
type Registry struct {
	Version      int               `yaml:"version"`
	Sources      map[string]Source `yaml:"sources"`
	Sets         map[string]Set    `yaml:"sets"`
	SourceErrors map[string]error  `yaml:"-"`
	SetErrors    map[string]error  `yaml:"-"`
}

// ErrNotConfigured is a set the registry does not define, or dropped.
var ErrNotConfigured = errors.New("tool set not configured")

// wikiAlias is the set built-in profiles have asked for since the wiki was
// the only notes source; notesSet is what `aimux tools add notes` names it
// (its default --name is notes).
const (
	wikiAlias = "wiki:readonly"
	notesSet  = "notes:readonly"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// DefaultPath is where the registry lives.
func DefaultPath(home string) string {
	return filepath.Join(home, ".config", "aimux", "tool-sources.yaml")
}

// LegacyPath is the read-only config the registry replaces.
func LegacyPath(home string) string {
	return filepath.Join(home, ".aimux", "wingman", "readonly.json")
}

// Default is the registry a new install starts with: the standard local MCP
// servers and their reviewed read-only tools. It has no notes source; one is
// added only when the user configures a folder.
func Default() Registry {
	return Registry{
		Version: 1,
		Sources: map[string]Source{
			"slack":  {Type: TypeMCPHTTP, URL: "http://127.0.0.1:7381/mcp/slack"},
			"jira":   {Type: TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"},
			"google": {Type: TypeMCPHTTP, URL: "http://127.0.0.1:8000/mcp"},
		},
		Sets: map[string]Set{
			"jira:readonly":  {Source: "jira", Tools: []string{"search_issues_jql", "get_issue_details", "get_comments", "get_jira_issues"}},
			"slack:readonly": {Source: "slack", Tools: []string{"search_messages", "get_thread", "get_thread_messages", "get_channel_history", "get_user_profile"}},
			"google:readonly": {Source: "google", Tools: []string{"search_gmail_messages", "get_gmail_message_content", "get_gmail_thread_content", "search_drive_files", "get_doc_as_markdown",
				"read_sheet_values", "get_spreadsheet_info", "get_drive_file_content", "get_presentation", "get_events", "list_calendars"}},
		},
	}
}

// Load reads and validates path. A missing file is an error wrapping
// fs.ErrNotExist; anything else that cannot be read, a dangling link
// included, is an error that does not. Unknown keys, a second YAML document, bad YAML and a version other than 1 fail the
// whole file with an error naming it; a malformed source or set only drops
// that entry (see Registry).
func Load(path string) (Registry, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the user's own tool-sources.yaml
	if errors.Is(err, fs.ErrNotExist) {
		if _, lerr := os.Lstat(path); lerr == nil { // the name exists: a dangling link
			return Registry{}, fmt.Errorf("read tool sources %s: it is a link to a missing file", path)
		}
	}
	if err != nil {
		return Registry{}, fmt.Errorf("read tool sources %s: %w", path, err)
	}
	return parse(path, data)
}

func parse(path string, data []byte) (Registry, error) {
	var r Registry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil && !errors.Is(err, io.EOF) {
		return Registry{}, fmt.Errorf("tool sources %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Registry{}, fmt.Errorf("tool sources %s: holds more than one YAML document", path)
	}
	if r.Version != 1 {
		return Registry{}, fmt.Errorf("tool sources %s: version must be 1, got %d", path, r.Version)
	}
	r.SourceErrors, r.SetErrors = map[string]error{}, map[string]error{}
	for n, s := range r.Sources {
		if err := checkSource(n, s); err != nil {
			r.SourceErrors[n] = fmt.Errorf("tool sources %s: source %q: %w", path, n, err)
			delete(r.Sources, n)
		}
	}
	for n, s := range r.Sets {
		if err := r.checkSet(n, s); err != nil {
			r.SetErrors[n] = fmt.Errorf("tool sources %s: set %q: %w", path, n, err)
			delete(r.Sets, n)
		}
	}
	return r, nil
}

func checkSource(name string, s Source) error {
	if !safeName.MatchString(name) {
		return errors.New("name must match [A-Za-z0-9_-]+")
	}
	switch s.Type {
	case TypeMCPHTTP:
		if s.Root != "" {
			return errors.New("root is for local-notes sources")
		}
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("url %q must be an http or https URL with a host", s.URL)
		}
	case TypeLocalNotes:
		if s.URL != "" {
			return errors.New("url is for mcp-http sources")
		}
		if !filepath.IsAbs(s.Root) {
			return fmt.Errorf("root %q must be an absolute directory", s.Root)
		}
	default:
		return fmt.Errorf("type %q must be %s or %s", s.Type, TypeMCPHTTP, TypeLocalNotes)
	}
	return nil
}

func (r Registry) checkSet(name string, s Set) error {
	parts := strings.Split(name, ":")
	if len(parts) != 2 || !safeName.MatchString(parts[0]) || !safeName.MatchString(parts[1]) {
		return errors.New("name must be <name>:<kind>, each [A-Za-z0-9_-]+")
	}
	if _, ok := r.Sources[s.Source]; !ok {
		if err, bad := r.SourceErrors[s.Source]; bad {
			return fmt.Errorf("source %q is invalid: %w", s.Source, err)
		}
		return fmt.Errorf("unknown source %q", s.Source)
	}
	if len(s.Tools) == 0 {
		return errors.New("lists no tools")
	}
	seen := map[string]bool{}
	for _, t := range s.Tools {
		if !safeName.MatchString(t) {
			return fmt.Errorf("tool %q must be an exact name matching [A-Za-z0-9_-]+", t)
		}
		if seen[t] {
			return fmt.Errorf("tool %q is listed twice", t)
		}
		seen[t] = true
	}
	return nil
}

// Resolve returns the set a profile names. wiki:readonly, which built-in
// profiles ask for, falls back to notes:readonly when the registry does not
// define it; a wiki:readonly that is defined but broken reports its own
// error. A set that is not defined, or was dropped, is ErrNotConfigured.
func (r Registry) Resolve(name string) (Set, error) {
	if s, ok := r.Sets[name]; ok {
		return s, nil
	}
	if err, ok := r.SetErrors[name]; ok {
		return Set{}, fmt.Errorf("%w: %w", ErrNotConfigured, err)
	}
	if name == wikiAlias {
		if s, ok := r.Sets[notesSet]; ok {
			return s, nil
		}
	}
	return Set{}, fmt.Errorf("%w: %q is not in tool-sources.yaml", ErrNotConfigured, name)
}

// Marshal renders r as the file Load reads.
func Marshal(r Registry) ([]byte, error) { return marshal(r, nil) }

// marshal is Marshal with extra comment lines after the header.
func marshal(r Registry, notes []string) ([]byte, error) {
	r.Version = 1
	var b bytes.Buffer
	b.WriteString("# Tool sources aimux may reach, and the reviewed tool sets each exposes.\n" +
		"# A tool not listed in a set is never exposed. Unknown keys are errors.\n")
	for _, n := range notes {
		b.WriteString("# " + n + "\n")
	}
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return nil, fmt.Errorf("encode tool sources: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode tool sources: %w", err)
	}
	return b.Bytes(), nil
}

// WriteNew writes r to path, which must not exist (an existing file is the
// user's: the error wraps fs.ErrExist and it is left alone). r must load
// back with no error of any kind. The file is written 0600 through a temp
// file and hard-linked into place, so it appears whole or not at all and is
// never replaced; a missing directory is created 0700.
func WriteNew(path string, r Registry) error {
	data, err := Marshal(r)
	if err != nil {
		return err
	}
	return writeNew(path, data)
}

func writeNew(path string, data []byte) error {
	if err := clean(path, data); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".tool-sources-*.yaml") // created 0600
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // after the link only the name goes
	if _, err := f.Write(data); err != nil {
		_ = f.Close() // the write error is the one to report
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close() // the sync error is the one to report
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Link(tmp, path); err != nil { // unlike rename, never replaces path
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// clean fails data that does not load back with every source and set intact.
func clean(path string, data []byte) error {
	r, err := parse(path, data)
	if err != nil {
		return err
	}
	var problems []error
	for _, m := range []map[string]error{r.SourceErrors, r.SetErrors} {
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			problems = append(problems, m[n])
		}
	}
	return errors.Join(problems...)
}

// legacyConfig is readonly.json as `aimux browser setup` wrote it.
type legacyConfig struct {
	HTTP map[string]struct {
		URL     string
		Headers map[string]string
	}
	WikiRoot string
	Allow    map[string][]string
}

// Migration is what MigrateReport did: whether it wrote the registry, and
// each allowlist entry it dropped, with why.
type Migration struct {
	Migrated bool
	Dropped  []string
}

// Migrate is MigrateReport without the dropped entries.
func Migrate(oldPath, regPath string) (migrated bool, err error) {
	m, err := MigrateReport(oldPath, regPath)
	return m.Migrated, err
}

// MigrateReport carries oldPath (readonly.json) into a new registry at
// regPath, once. It does nothing when anything exists at regPath, whatever
// it holds (a dangling link too), or when oldPath does not. Each HTTP
// backend becomes an mcp-http source, the wiki root a local-notes source
// named wiki (the server name its pins and tools already use), and each
// allowlist entry the set <name>:readonly. An allowlist entry with no
// backend (the wiki with an empty root) was never served: it is dropped,
// listed in Dropped and noted in the file. A legacy file that does not
// carry over cleanly otherwise (unknown keys, HTTP headers) is an error and
// nothing is written. Only after the registry is written and loads back is
// oldPath renamed to oldPath.migrated.
func MigrateReport(oldPath, regPath string) (Migration, error) {
	if _, err := os.Lstat(regPath); err == nil {
		return Migration{}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Migration{}, fmt.Errorf("check %s: %w", regPath, err)
	}
	data, err := os.ReadFile(oldPath) //nolint:gosec // G304: the user's own readonly.json
	if errors.Is(err, fs.ErrNotExist) {
		return Migration{}, nil
	}
	if err != nil {
		return Migration{}, fmt.Errorf("read %s: %w", oldPath, err)
	}
	r, dropped, err := fromLegacy(data)
	if err != nil {
		return Migration{}, fmt.Errorf("migrate %s: %w", oldPath, err)
	}
	notes := make([]string, 0, len(dropped))
	for _, d := range dropped {
		notes = append(notes, "Migrated from "+oldPath+": "+d)
	}
	out, err := marshal(r, notes)
	if err != nil {
		return Migration{}, err
	}
	if err := writeNew(regPath, out); errors.Is(err, fs.ErrExist) {
		return Migration{}, nil // written meanwhile by someone else: theirs
	} else if err != nil {
		return Migration{}, fmt.Errorf("migrate %s: %w", oldPath, err)
	}
	if _, err := Load(regPath); err != nil {
		return Migration{}, fmt.Errorf("migrate %s: %w", oldPath, err)
	}
	m := Migration{Migrated: true, Dropped: dropped}
	if err := os.Rename(oldPath, oldPath+".migrated"); err != nil {
		return m, fmt.Errorf("migrated %s to %s but could not rename it: %w", oldPath, regPath, err)
	}
	return m, nil
}

func fromLegacy(data []byte) (Registry, []string, error) {
	var old legacyConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&old); err != nil {
		return Registry{}, nil, fmt.Errorf("parse: %w", err)
	}
	r := Registry{Version: 1, Sources: map[string]Source{}, Sets: map[string]Set{}}
	for n, h := range old.HTTP {
		if len(h.Headers) > 0 { // the values may be credentials: never echo them
			return Registry{}, nil, fmt.Errorf("backend %q sets HTTP headers, and header support is not carried over: tool-sources.yaml has no field for them", n)
		}
		if n == "wiki" {
			return Registry{}, nil, fmt.Errorf("an HTTP backend named wiki collides with the wiki root")
		}
		r.Sources[n] = Source{Type: TypeMCPHTTP, URL: h.URL}
	}
	if old.WikiRoot != "" {
		r.Sources["wiki"] = Source{Type: TypeLocalNotes, Root: old.WikiRoot}
	}
	var dropped []string
	for _, n := range slices.Sorted(maps.Keys(old.Allow)) {
		if _, ok := r.Sources[n]; !ok {
			dropped = append(dropped, fmt.Sprintf("set %q dropped: backend %s is not configured (it was never served)", n+":readonly", n))
			continue
		}
		r.Sets[n+":readonly"] = Set{Source: n, Tools: old.Allow[n]}
	}
	data, err := Marshal(r)
	if err != nil {
		return Registry{}, nil, err
	}
	if err := clean("(migrated)", data); err != nil {
		return Registry{}, nil, err
	}
	return r, dropped, nil
}

// ReadHome reads the registry under home and writes nothing: the registry
// when anything is at its path (Load), else a readonly.json not migrated
// yet, converted in memory as Migrate would write it, else Default(). It is
// what snapshot reloads and the read-only commands use; migration
// (MigrateReport) happens at remote serve start, in aimux setup and browser
// setup, and before aimux tools add or remove writes.
func ReadHome(home string) (Registry, error) {
	reg := DefaultPath(home)
	if _, err := os.Lstat(reg); !errors.Is(err, fs.ErrNotExist) {
		return Load(reg)
	}
	old := LegacyPath(home)
	data, err := os.ReadFile(old) //nolint:gosec // G304: the user's own readonly.json
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Registry{}, fmt.Errorf("read %s: %w", old, err)
	}
	r, _, err := fromLegacy(data)
	if err != nil {
		return Registry{}, fmt.Errorf("read %s (not migrated yet): %w", old, err)
	}
	out, err := Marshal(r)
	if err != nil {
		return Registry{}, err
	}
	return parse(old+" (not migrated yet)", out)
}
