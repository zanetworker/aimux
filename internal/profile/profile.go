package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Profile stores a named bundle of flags (provider, dir, model, mode).
type Profile struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Dir      string `json:"dir,omitempty"`
	Model    string `json:"model,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

// Store manages a collection of named profiles persisted to a JSON file.
type Store struct {
	path     string
	profiles map[string]Profile
}

// NewStore creates a new Store backed by the given file path.
func NewStore(path string) *Store {
	return &Store{path: path, profiles: make(map[string]Profile)}
}

// DefaultPath returns the default profiles file location (~/.aimux/profiles.json).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aimux", "profiles.json")
}

// Load reads profiles from disk. Missing file is not an error.
func (s *Store) Load() error {
	data, err := os.ReadFile(s.path) // #nosec G304
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read profiles: %w", err)
	}
	var profiles []Profile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return fmt.Errorf("parse profiles: %w", err)
	}
	s.profiles = make(map[string]Profile, len(profiles))
	for _, p := range profiles {
		s.profiles[p.Name] = p
	}
	return nil
}

// save writes the current profiles to disk.
func (s *Store) save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create profiles dir: %w", err)
	}
	profiles := s.List()
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profiles: %w", err)
	}
	return os.WriteFile(s.path, data, 0o600)
}

// Get returns a profile by name.
func (s *Store) Get(name string) (Profile, bool) {
	p, ok := s.profiles[name]
	return p, ok
}

// Save adds or updates a profile and persists to disk.
func (s *Store) Save(p Profile) error {
	s.profiles[p.Name] = p
	return s.save()
}

// Delete removes a profile by name and persists to disk.
func (s *Store) Delete(name string) error {
	if _, ok := s.profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}
	delete(s.profiles, name)
	return s.save()
}

// List returns all profiles sorted alphabetically by name.
func (s *Store) List() []Profile {
	result := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// Names returns all profile names sorted alphabetically.
func (s *Store) Names() []string {
	names := make([]string, 0, len(s.profiles))
	for name := range s.profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
