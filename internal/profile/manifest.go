package profile

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is the v2 profile: a security contract (tools, network, files,
// credentials, settings), where it runs, and which harness drives it.
// The v1 Profile store is only read for migration (see MigrateV1).
type Manifest struct {
	Name           string            `yaml:"name" json:"name"`
	Extends        string            `yaml:"extends" json:"extends"`
	Harness        string            `yaml:"harness" json:"harness"`
	Model          ModelRef          `yaml:"model" json:"model"`
	Runs           string            `yaml:"runs" json:"runs"` // host | acp | sandbox | run
	Dir            string            `yaml:"dir" json:"dir"`   // working directory carried over from v1; not a permission
	Tools          []string          `yaml:"tools" json:"tools"`
	Network        NetworkSpec       `yaml:"network" json:"network"`
	Files          FilesSpec         `yaml:"files" json:"files"`
	Credentials    []string          `yaml:"credentials" json:"credentials"`
	Settings       SettingsSpec      `yaml:"settings" json:"settings"`
	UnsafePatterns bool              `yaml:"unsafe_patterns" json:"unsafe_patterns"`
	Roles          map[string]string `yaml:"roles" json:"roles"` // role -> profile; targets are not validated yet
	Available      bool              `yaml:"-" json:"-"`
}

// ModelRef names the model provider and model.
type ModelRef struct {
	Provider string `yaml:"provider" json:"provider"`
	Name     string `yaml:"name" json:"name"`
}

// NetworkSpec is the egress policy. Allow items: model, gateway, web.
type NetworkSpec struct {
	Default string   `yaml:"default" json:"default"` // deny | allow
	Allow   []string `yaml:"allow" json:"allow"`
}

// FilesSpec lists readable and writable paths.
type FilesSpec struct {
	Read  []string `yaml:"read" json:"read"`
	Write []string `yaml:"write" json:"write"`
}

// SettingsSpec controls what user-level harness settings leak in.
type SettingsSpec struct {
	InheritUser bool `yaml:"inherit_user" json:"inherit_user"`
}

var (
	ErrUnknownProfile = errors.New("unknown profile")
	ErrWidens         = errors.New("child widens parent")
	ErrCycle          = errors.New("extends cycle")
	ErrWildcard       = errors.New("wildcard in tools")
)

const (
	defaultHarness = "claude-code"
	builtinPrefix  = "builtin:"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// unavailable builtins exist but cannot be used until a later plan ships.
var unavailable = map[string]bool{"research": true, "research-with-work": true}

var (
	validRuns  = map[string]bool{"host": true, "acp": true, "sandbox": true, "run": true}
	validAllow = map[string]bool{"model": true, "gateway": true, "web": true}
)

func decodeStrict(data []byte) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return m, errors.New("empty file")
		}
		return m, err
	}
	return m, nil
}

func builtins() map[string]Manifest {
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		panic(fmt.Sprintf("profile: embedded builtins unreadable: %v", err)) // embed is compile-time; cannot happen
	}
	out := make(map[string]Manifest, len(entries))
	for _, e := range entries {
		data, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("profile: read builtin %s: %v", e.Name(), err))
		}
		m, err := decodeStrict(data)
		if err != nil {
			panic(fmt.Sprintf("profile: builtin %s invalid: %v", e.Name(), err))
		}
		m.Available = !unavailable[m.Name]
		out[m.Name] = m
	}
	return out
}

// LoadDir reads every <name>.yaml in dir. A missing dir yields an empty map.
// A bad file (unknown field, name mismatch, shadows a builtin, no extends) is
// skipped and reported in the returned error (errors.Join); the map still holds
// every good profile, so one bad file never hides the rest.
func LoadDir(dir string) (map[string]Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Manifest{}, nil
		}
		return nil, fmt.Errorf("read profile dir %s: %w", dir, err)
	}
	bi := builtins()
	out := map[string]Manifest{}
	var errs []error
	for _, e := range entries {
		file := e.Name()
		if e.IsDir() || filepath.Ext(file) != ".yaml" {
			continue
		}
		m, err := loadFile(filepath.Join(dir, file), file, bi)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[m.Name] = m
	}
	return out, errors.Join(errs...)
}

func loadFile(path, file string, bi map[string]Manifest) (Manifest, error) {
	name := strings.TrimSuffix(file, ".yaml")
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", file, err)
	}
	m, err := decodeStrict(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", file, err)
	}
	if m.Name != name {
		return Manifest{}, fmt.Errorf("%s: name %q must equal file name %q", file, m.Name, name)
	}
	if _, ok := bi[name]; ok {
		return Manifest{}, fmt.Errorf("%s: cannot shadow builtin profile %q", file, name)
	}
	if m.Extends == "" {
		return Manifest{}, fmt.Errorf("%s: user profiles must declare extends (user-defined roots are not supported yet)", file)
	}
	m.Available = true
	return m, nil
}

// Resolve flattens name through its extends chain, enforcing narrow-only
// inheritance. Builtins are addressed as "builtin:<name>" or by bare name.
func Resolve(name string, user map[string]Manifest) (Manifest, error) {
	bi := builtins()
	m, err := resolve(name, bi, user, map[string]bool{})
	if err != nil {
		return Manifest{}, err
	}
	return m, validate(m)
}

// lookup finds ref. A bare name that is both a builtin and a user entry is an
// error (the user file shadows a builtin); "builtin:<name>" is always safe.
func lookup(ref string, bi, user map[string]Manifest) (Manifest, string, bool, error) {
	if n, ok := strings.CutPrefix(ref, builtinPrefix); ok {
		m, found := bi[n]
		return m, builtinPrefix + n, found, nil
	}
	if m, ok := bi[ref]; ok {
		if _, shadowed := user[ref]; shadowed {
			return Manifest{}, "", false, fmt.Errorf("%s.yaml: cannot shadow builtin profile %q (use builtin:%s to reach the builtin)", ref, ref, ref)
		}
		return m, builtinPrefix + ref, true, nil
	}
	m, ok := user[ref]
	return m, "user:" + ref, ok, nil
}

func resolve(ref string, bi, user map[string]Manifest, seen map[string]bool) (Manifest, error) {
	raw, key, ok, err := lookup(ref, bi, user)
	if err != nil {
		return Manifest{}, err
	}
	if !ok {
		return Manifest{}, fmt.Errorf("%w: %q", ErrUnknownProfile, ref)
	}
	if seen[key] {
		return Manifest{}, fmt.Errorf("%w: %q revisited", ErrCycle, ref)
	}
	seen[key] = true
	if strings.HasPrefix(key, "user:") {
		raw.Available = true // only builtins can be unavailable
	}
	if !raw.UnsafePatterns {
		for _, t := range raw.Tools {
			if strings.ContainsAny(t, "*?") {
				return Manifest{}, fmt.Errorf("%w: profile %q tool %q (set unsafe_patterns: true to allow)", ErrWildcard, raw.Name, t)
			}
		}
	}
	if raw.Extends == "" {
		return withRootDefaults(raw), nil
	}
	parent, err := resolve(raw.Extends, bi, user, seen)
	if err != nil {
		return Manifest{}, err
	}
	return narrow(parent, raw)
}

func withRootDefaults(m Manifest) Manifest {
	if m.Harness == "" {
		m.Harness = defaultHarness
	}
	if m.Runs == "" {
		m.Runs = "host"
	}
	if m.Network.Default == "" {
		m.Network.Default = "deny"
	}
	return m
}

// runsMayChange allows only moves toward more isolation: host -> acp -> sandbox.
// "run" is a separate mode that only stays "run".
func runsMayChange(from, to string) bool {
	rank := map[string]int{"host": 0, "acp": 1, "sandbox": 2}
	if from == to {
		return true
	}
	f, okF := rank[from]
	t, okT := rank[to]
	return okF && okT && t > f
}

func widens(profile, field string, parent, child []string) error {
	have := map[string]bool{}
	for _, p := range parent {
		have[p] = true
	}
	for _, c := range child {
		if !have[c] {
			return fmt.Errorf("%w: profile %q adds %q to %s", ErrWidens, profile, c, field)
		}
	}
	return nil
}

// narrow merges child over parent. A nil child list inherits the parent's;
// an explicit list (even empty) must be a subset.
func narrow(parent, child Manifest) (Manifest, error) {
	out := parent
	out.Name, out.Extends, out.UnsafePatterns = child.Name, child.Extends, child.UnsafePatterns
	out.Available = parent.Available && child.Available

	if child.Harness != "" && child.Harness != parent.Harness {
		return Manifest{}, fmt.Errorf("%w: profile %q changes harness from %q to %q", ErrWidens, child.Name, parent.Harness, child.Harness)
	}
	if child.Runs != "" {
		if !runsMayChange(parent.Runs, child.Runs) {
			return Manifest{}, fmt.Errorf("%w: profile %q changes runs from %q to %q", ErrWidens, child.Name, parent.Runs, child.Runs)
		}
		out.Runs = child.Runs
	}
	if child.Dir != "" {
		out.Dir = child.Dir
	}
	if child.Model.Provider != "" {
		out.Model.Provider = child.Model.Provider
	}
	if child.Model.Name != "" {
		out.Model.Name = child.Model.Name
	}
	if child.Roles != nil {
		out.Roles = child.Roles
	}
	lists := []struct {
		field         string
		parent, child []string
		dst           *[]string
	}{
		{"tools", parent.Tools, child.Tools, &out.Tools},
		{"network.allow", parent.Network.Allow, child.Network.Allow, &out.Network.Allow},
		{"files.read", parent.Files.Read, child.Files.Read, &out.Files.Read},
		{"files.write", parent.Files.Write, child.Files.Write, &out.Files.Write},
		{"credentials", parent.Credentials, child.Credentials, &out.Credentials},
	}
	for _, l := range lists {
		if l.child == nil {
			continue
		}
		if err := widens(child.Name, l.field, l.parent, l.child); err != nil {
			return Manifest{}, err
		}
		*l.dst = l.child
	}
	if d := child.Network.Default; d != "" {
		if parent.Network.Default == "deny" && d == "allow" {
			return Manifest{}, fmt.Errorf("%w: profile %q changes network.default from deny to allow", ErrWidens, child.Name)
		}
		out.Network.Default = d
	}
	// An absent inherit_user reads as false, which only narrows.
	if child.Settings.InheritUser && !parent.Settings.InheritUser {
		return Manifest{}, fmt.Errorf("%w: profile %q sets settings.inherit_user true over false", ErrWidens, child.Name)
	}
	out.Settings.InheritUser = child.Settings.InheritUser
	return out, nil
}

func validate(m Manifest) error {
	if !validRuns[m.Runs] {
		return fmt.Errorf("profile %q: runs %q must be host, acp, sandbox or run", m.Name, m.Runs)
	}
	if m.Network.Default != "deny" && m.Network.Default != "allow" {
		return fmt.Errorf("profile %q: network.default %q must be deny or allow", m.Name, m.Network.Default)
	}
	for _, a := range m.Network.Allow {
		if !validAllow[a] {
			return fmt.Errorf("profile %q: network.allow %q must be model, gateway or web", m.Name, a)
		}
	}
	return nil
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// Digest is "sha256:<hex>" over the canonical JSON of the resolved manifest:
// fixed field order, sorted sets, nil lists equal to empty ones.
func (m Manifest) Digest() string {
	c := m
	c.Tools = sortedCopy(m.Tools)
	c.Credentials = sortedCopy(m.Credentials)
	c.Network.Allow = sortedCopy(m.Network.Allow)
	c.Files.Read = sortedCopy(m.Files.Read)
	c.Files.Write = sortedCopy(m.Files.Write)
	if c.Roles == nil {
		c.Roles = map[string]string{}
	}
	data, err := json.Marshal(c)
	if err != nil {
		panic(fmt.Sprintf("profile: marshal manifest: %v", err)) // plain strings and bools; cannot fail
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// MigrateV1 turns saved v1 profiles into manifests on builtin:base.
// Mode has no v2 equivalent and is dropped. A v1 name that collides with a
// builtin is renamed to <name>-v1 (then <name>-v1-2, ...) so it can still be
// loaded; the second return maps old name -> new name for every rename, for the
// caller to report.
func MigrateV1(old []Profile) ([]Manifest, map[string]string) {
	bi := builtins()
	used := map[string]bool{}
	for _, p := range old {
		used[p.Name] = true
	}
	out := make([]Manifest, 0, len(old))
	renames := map[string]string{}
	for _, p := range old {
		name := p.Name
		if _, clash := bi[name]; clash {
			name = p.Name + "-v1"
			for i := 2; used[name]; i++ {
				name = fmt.Sprintf("%s-v1-%d", p.Name, i)
			}
			used[name] = true
			renames[p.Name] = name
		}
		out = append(out, Manifest{
			Name:      name,
			Extends:   builtinPrefix + "base",
			Runs:      "host",
			Model:     ModelRef{Provider: p.Provider, Name: p.Model},
			Dir:       p.Dir,
			Available: true,
		})
	}
	return out, renames
}

// BuiltinNames is the names of the builtin profiles, sorted.
func BuiltinNames() []string {
	bi := builtins()
	names := make([]string, 0, len(bi))
	for n := range bi {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
