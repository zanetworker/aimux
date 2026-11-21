package secretref

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Ref names one secret in exactly one backend. It is what files hold in
// place of a value.
type Ref struct {
	Keychain *KeychainRef `yaml:"keychain,omitempty" json:"keychain,omitempty"`
	File     *FileRef     `yaml:"file,omitempty" json:"file,omitempty"`
	Cmd      *CmdRef      `yaml:"cmd,omitempty" json:"cmd,omitempty"`
	Env      *EnvRef      `yaml:"env,omitempty" json:"env,omitempty"`
}

// KeychainRef is a Keychain item and, for a JSON item, a dotted path into
// it ("env.JIRA_TOKEN"; a key may itself hold dots). An empty Field is the
// whole item value.
type KeychainRef struct {
	Item  string `yaml:"item" json:"item"`
	Field string `yaml:"field,omitempty" json:"field,omitempty"`
}

// FileRef is a file you own with mode 0600 or stricter: the value of Key in
// it as a strict dotenv, or the whole file (trimmed) when Key is empty.
type FileRef struct {
	Path string `yaml:"path" json:"path"`
	Key  string `yaml:"key,omitempty" json:"key,omitempty"`
}

// CmdRef is a command whose standard output is the value. Argv[0] is an
// absolute path; no shell runs it.
type CmdRef struct {
	Argv    []string      `yaml:"argv" json:"argv"`
	Timeout time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	TTL     time.Duration `yaml:"ttl,omitempty" json:"ttl,omitempty"`
}

// EnvRef is a variable of aimux's own process environment (under launchd,
// launchd's, not your interactive shell's).
type EnvRef struct {
	Name string `yaml:"name" json:"name"`
}

const (
	defaultCmdTimeout = 5 * time.Second
	maxCmdTimeout     = 30 * time.Second
)

// Validate reports a reference that sets no backend, more than one, or an
// incomplete one.
func (r Ref) Validate() error {
	n := 0
	for _, set := range []bool{r.Keychain != nil, r.File != nil, r.Cmd != nil, r.Env != nil} {
		if set {
			n++
		}
	}
	switch {
	case n == 0:
		return errors.New("a secret reference needs one of keychain, file, cmd or env")
	case n > 1:
		return errors.New("a secret reference sets more than one of keychain, file, cmd and env")
	case r.Keychain != nil && r.Keychain.Item == "":
		return errors.New("keychain reference without an item")
	case r.File != nil && !filepath.IsAbs(expandHome(r.File.Path)):
		return fmt.Errorf("file reference path %q is not absolute", r.File.Path)
	case r.Cmd != nil && (len(r.Cmd.Argv) == 0 || !filepath.IsAbs(r.Cmd.Argv[0])):
		return errors.New("cmd reference needs argv with an absolute program path")
	case r.Cmd != nil && r.Cmd.Timeout > maxCmdTimeout:
		return fmt.Errorf("cmd reference timeout %s is over %s", r.Cmd.Timeout, maxCmdTimeout)
	case r.Env != nil && r.Env.Name == "":
		return errors.New("env reference without a name")
	}
	return nil
}

// Backend is the reference's backend name.
func (r Ref) Backend() string {
	switch {
	case r.Keychain != nil:
		return "keychain"
	case r.File != nil:
		return "file"
	case r.Cmd != nil:
		return "cmd"
	case r.Env != nil:
		return "env"
	}
	return "none"
}

// Refs are a server's secrets: environment variables for its process and
// headers for its HTTP requests.
type Refs struct {
	Env     map[string]Ref `yaml:"env,omitempty" json:"env,omitempty"`
	Headers map[string]Ref `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// Names is every secret's name, sorted: env:NAME and header:Name.
func (r Refs) Names() []string {
	var out []string
	for n := range r.Env {
		out = append(out, "env:"+n)
	}
	for n := range r.Headers {
		out = append(out, "header:"+n)
	}
	sort.Strings(out)
	return out
}

// Values are resolved secrets; they live in memory only.
type Values struct {
	Env     map[string]string
	Headers map[string]string
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := userHome(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
