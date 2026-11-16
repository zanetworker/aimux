// Package clipack describes CLI packs: a binary, the read-only commands an
// agent may run with it, the flags each command accepts and the sandbox
// network policy it needs. A pack is the allowlist between a model's tool
// call and a CLI's argv: Argv builds a command line only from declared
// flags whose values pass their type, pattern and bounds.
package clipack

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed packs/*.yaml
var builtin embed.FS

// Pack is one CLI pack, as written in packs/<name>.yaml.
type Pack struct {
	Name       string     `yaml:"name"`    // "gh"; tool names are <name>_<command>
	Binary     string     `yaml:"binary"`  // absolute path inside aimux-tools
	Version    string     `yaml:"version"` // CLI version written for, "major.minor"
	Credential *Cred      `yaml:"credential,omitempty"`
	Commands   []Command  `yaml:"commands"`
	Policy     []Endpoint `yaml:"policy"` // shapes as recorded by the Task 1 spike
}

// Cred names the OpenShell provider that carries the CLI's token, the
// Keychain item it is created from, and the variable the CLI reads.
type Cred struct {
	Provider string `yaml:"provider"`
	Keychain string `yaml:"keychain"`
	Env      string `yaml:"env"`
}

// Command is one read-only command: Argv, then the chosen flags in sorted
// key order, then Fixed, then "--" and the positional args.
type Command struct {
	Name        string          `yaml:"name"`  // "issue_list"
	Argv        []string        `yaml:"argv"`  // ["issue","list"]
	Fixed       []string        `yaml:"fixed"` // appended verbatim, e.g. ["--json","number,title,state,url"]
	Flags       map[string]Flag `yaml:"flags"` // key without dashes: "repo" -> --repo
	Args        []Flag          `yaml:"args"`  // positional, in order
	Description string          `yaml:"description"`
}

// Flag is one accepted value: a flag (keyed in Command.Flags) or a
// positional arg (named by Name).
type Flag struct {
	Name     string   `yaml:"name,omitempty"` // positional args only
	Type     string   `yaml:"type"`           // string | int | enum | bool
	Pattern  string   `yaml:"pattern,omitempty"`
	Values   []string `yaml:"values,omitempty"`
	Min      int      `yaml:"min,omitempty"` // int only
	Max      int      `yaml:"max,omitempty"` // int only
	Required bool     `yaml:"required,omitempty"`

	re *regexp.Regexp // Pattern as ^(?:Pattern)$, compiled by Load
}

// Endpoint is one network policy endpoint, in the shape OpenShell takes:
// Access and Rules are mutually exclusive.
type Endpoint struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Path        string `yaml:"path,omitempty"`
	Protocol    string `yaml:"protocol"`              // rest | graphql
	Enforcement string `yaml:"enforcement,omitempty"` // enforce
	Access      string `yaml:"access,omitempty"`      // read-only
	Rules       []Rule `yaml:"rules,omitempty"`
}

// Rule is one allow rule on an endpoint.
type Rule struct {
	Allow Allow `yaml:"allow"`
}

// Allow is what a rule lets through: a GraphQL operation type, or a REST
// method and path.
type Allow struct {
	OperationType string `yaml:"operation_type,omitempty"`
	Method        string `yaml:"method,omitempty"`
	Path          string `yaml:"path,omitempty"`
}

// profile is what a pack for one binary may set, fixed in code: a pack
// author cannot widen it. It bounds the commands, the fixed arguments, the
// credential variable and the hosts the policy opens, so a pack cannot
// point the CLI (and its token) at another host.
type profile struct {
	argv         [][]string // allowed argv, compared exactly
	fixedFlag    string     // the one flag fixed may set, followed by one value
	fixedValue   *regexp.Regexp
	credProvider string   // the only OpenShell provider
	credKeychain string   // the only Keychain item
	credEnv      string   // the only credential variable
	port         int      // the only endpoint port
	hosts        []string // the only endpoint hosts
	graphqlPath  string   // the only path a graphql endpoint may have
}

// profiles is keyed by the full binary path; the verb and flag denylists
// below stay as a backstop.
var profiles = map[string]profile{
	"/usr/bin/gh": { // #nosec G101 -- names of a provider, Keychain item and env variable, not secrets
		argv: [][]string{
			{"issue", "list"}, {"issue", "view"},
			{"pr", "list"}, {"pr", "view"},
			{"repo", "view"},
			{"release", "list"}, {"release", "view"},
			{"search", "issues"}, {"search", "prs"}, {"search", "repos"},
		},
		fixedFlag:    "--json",
		fixedValue:   regexp.MustCompile(`^[a-zA-Z,]+$`),
		credProvider: "aimux-github",
		credKeychain: "aimux-github",
		credEnv:      "GH_TOKEN",
		port:         443,
		hosts:        []string{"api.github.com"},
		graphqlPath:  "/graphql",
	},
}

func (pr profile) checkArgv(argv []string) error {
	for _, a := range pr.argv {
		if slices.Equal(a, argv) {
			return nil
		}
	}
	return fmt.Errorf("argv %q is not allowed for this binary", argv)
}

// checkFixed allows nothing, or exactly the profile's flag and one value.
func (pr profile) checkFixed(fixed []string) error {
	if len(fixed) == 0 {
		return nil
	}
	if len(fixed) != 2 || fixed[0] != pr.fixedFlag || !pr.fixedValue.MatchString(fixed[1]) {
		return fmt.Errorf("fixed %q: only %s followed by one value matching %s", fixed, pr.fixedFlag, pr.fixedValue)
	}
	return nil
}

func (pr profile) checkEndpoint(e Endpoint) error {
	if !slices.Contains(pr.hosts, e.Host) {
		return fmt.Errorf("host %q is not allowed for this binary", e.Host)
	}
	if e.Port != pr.port {
		return fmt.Errorf("port %d: this binary takes only %d", e.Port, pr.port)
	}
	if e.Protocol == "graphql" && e.Path != pr.graphqlPath {
		return fmt.Errorf("graphql path %q: want %s", e.Path, pr.graphqlPath)
	}
	return nil
}

// checkNoInlineFlags refuses syntax that Go RE2 (which checks values)
// and ECMA-262 (which reads the JSON Schema pattern) read differently or
// only one accepts: "(?" other than "(?:" (inline flags, named groups),
// POSIX classes "[:" inside a class, and the escapes \A \z \Q \p \P \C
// \0-\7 \s \S \b \B and \x{...}. Plain \d \w (ASCII in both) are fine.
func checkNoInlineFlags(pat string) error {
	inClass := false
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; {
		case c == '\\':
			if i+1 < len(pat) {
				n := pat[i+1]
				if strings.IndexByte("AzQpPC01234567sSbB", n) >= 0 || (n == 'x' && i+2 < len(pat) && pat[i+2] == '{') {
					return fmt.Errorf("pattern %q: escape \\%c reads differently in JSON Schema", pat, n)
				}
			}
			i++
		case inClass:
			if c == '[' && i+1 < len(pat) && pat[i+1] == ':' {
				return fmt.Errorf("pattern %q: no POSIX classes", pat)
			}
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
			// A ] right after [ or [^ is a literal, not the end of the class.
			if i+1 < len(pat) && pat[i+1] == '^' {
				i++
			}
			if i+1 < len(pat) && pat[i+1] == ']' {
				i++
			}
		case c == '(' && i+1 < len(pat) && pat[i+1] == '?':
			if i+2 >= len(pat) || pat[i+2] != ':' {
				return fmt.Errorf("pattern %q: only (?: groups, no inline flags or named groups", pat)
			}
		}
	}
	return nil
}

// deniedVerbs are subcommands that change state, reach credentials or run
// other code; no argv or fixed word may be one.
var deniedVerbs = map[string]bool{}

// deniedFlags open a browser, run a filter or template, or point the CLI
// at another host; no argv or fixed word may be one, with or without =value.
var deniedFlags = map[string]bool{
	"--web": true, "-w": true, "--jq": true, "-q": true,
	"--template": true, "-t": true, "--hostname": true, "--browser": true,
}

func init() {
	for _, v := range strings.Fields(`create edit delete close merge comment api
		auth extension alias config secret variable workflow run codespace gist
		reopen lock unlock transfer fork checkout review ready upload download
		rename archive unarchive set install upgrade sync pin unpin develop`) {
		deniedVerbs[v] = true
	}
}

// checkWord refuses a denied verb or flag.
func checkWord(w string) error {
	if deniedVerbs[w] {
		return fmt.Errorf("write or unsafe subcommand %q", w)
	}
	name, _, _ := strings.Cut(w, "=")
	if deniedFlags[name] {
		return fmt.Errorf("denied flag %q", name)
	}
	return nil
}

var (
	packNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	cmdNameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	wordRE     = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	keyRE      = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	versionRE  = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	safeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	alphaRE    = regexp.MustCompile(`^[a-z]+$`)
	hostRE     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

// Builtin returns the embedded pack packs/<name>.yaml.
func Builtin(name string) (Pack, error) {
	if !packNameRE.MatchString(name) {
		return Pack{}, fmt.Errorf("no built-in CLI pack %q", name)
	}
	data, err := builtin.ReadFile("packs/" + name + ".yaml")
	if err != nil {
		return Pack{}, fmt.Errorf("no built-in CLI pack %q", name)
	}
	return Load(data)
}

// Load decodes a pack strictly (unknown keys are errors) and checks it.
func Load(data []byte) (Pack, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var p Pack
	if err := dec.Decode(&p); err != nil {
		return Pack{}, fmt.Errorf("parse CLI pack: %w", err)
	}
	if err := p.prepare(); err != nil {
		return Pack{}, fmt.Errorf("CLI pack %q: %w", p.Name, err)
	}
	return p, nil
}

// prepare checks p and compiles its patterns in place.
func (p *Pack) prepare() error {
	if !packNameRE.MatchString(p.Name) {
		return fmt.Errorf("name %q must match %s", p.Name, packNameRE)
	}
	if !strings.HasPrefix(p.Binary, "/") {
		return fmt.Errorf("binary %q must be an absolute path", p.Binary)
	}
	prof, ok := profiles[p.Binary]
	if !ok {
		return fmt.Errorf("no profile for %s", p.Binary)
	}
	if !versionRE.MatchString(p.Version) {
		return fmt.Errorf("version %q must be major.minor", p.Version)
	}
	if c := p.Credential; c != nil {
		if !safeNameRE.MatchString(c.Provider) {
			return fmt.Errorf("credential provider %q must match %s", c.Provider, safeNameRE)
		}
		if !safeNameRE.MatchString(c.Keychain) {
			return fmt.Errorf("credential keychain %q must match %s", c.Keychain, safeNameRE)
		}
		if c.Provider != prof.credProvider || c.Keychain != prof.credKeychain {
			return fmt.Errorf("credential provider %q, keychain %q: this binary takes only %s, %s", c.Provider, c.Keychain, prof.credProvider, prof.credKeychain)
		}
		if c.Env != prof.credEnv {
			return fmt.Errorf("credential env %q: this binary takes only %s", c.Env, prof.credEnv)
		}
	}
	if len(p.Commands) == 0 {
		return errors.New("no commands")
	}
	seen := map[string]bool{}
	for i := range p.Commands {
		c := &p.Commands[i]
		if seen[c.Name] {
			return fmt.Errorf("command %q twice", c.Name)
		}
		seen[c.Name] = true
		if err := c.prepare(prof); err != nil {
			return fmt.Errorf("command %q: %w", c.Name, err)
		}
	}
	if len(p.Policy) == 0 {
		return errors.New("no policy endpoints")
	}
	for i, e := range p.Policy {
		if err := e.check(); err != nil {
			return fmt.Errorf("policy endpoint %d: %w", i, err)
		}
		if err := prof.checkEndpoint(e); err != nil {
			return fmt.Errorf("policy endpoint %d: %w", i, err)
		}
	}
	return nil
}

func (c *Command) prepare(prof profile) error {
	if !cmdNameRE.MatchString(c.Name) {
		return fmt.Errorf("name must match %s", cmdNameRE)
	}
	if len(c.Argv) == 0 {
		return errors.New("empty argv")
	}
	for _, w := range c.Argv {
		if err := checkWord(w); err != nil {
			return fmt.Errorf("argv: %w", err)
		}
		if !wordRE.MatchString(w) {
			return fmt.Errorf("argv word %q must match %s", w, wordRE)
		}
	}
	if err := prof.checkArgv(c.Argv); err != nil {
		return err
	}
	if err := prof.checkFixed(c.Fixed); err != nil {
		return err
	}
	for _, f := range c.Fixed {
		if f == "--" {
			return errors.New("fixed: \"--\" is reserved for the positional args")
		}
		if f == "" || checkControl(f) != nil {
			return fmt.Errorf("fixed argument %q is empty or has control characters", f)
		}
		if err := checkWord(f); err != nil {
			return fmt.Errorf("fixed: %w", err)
		}
		// Short flags can be bundled (-wq), so a fixed word takes long flags only.
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") {
			return fmt.Errorf("fixed: short flag %q; use the long form", f)
		}
	}
	for k, f := range c.Flags {
		if !keyRE.MatchString(k) {
			return fmt.Errorf("flag key %q must match %s (no dashes in front)", k, keyRE)
		}
		if f.Name != "" {
			return fmt.Errorf("flag %q: name is for positional args only", k)
		}
		if err := checkWord("--" + k); err != nil {
			return fmt.Errorf("flag key: %w", err)
		}
		if len(k) == 1 {
			if err := checkWord("-" + k); err != nil {
				return fmt.Errorf("flag key: %w", err)
			}
		}
		if err := f.prepare(); err != nil {
			return fmt.Errorf("flag %q: %w", k, err)
		}
		c.Flags[k] = f
	}
	for i := range c.Args {
		a := &c.Args[i]
		if !keyRE.MatchString(a.Name) {
			return fmt.Errorf("arg %d: name %q must match %s", i, a.Name, keyRE)
		}
		if _, dup := c.Flags[a.Name]; dup {
			return fmt.Errorf("arg %q has the name of a flag", a.Name)
		}
		for _, b := range c.Args[:i] {
			if b.Name == a.Name {
				return fmt.Errorf("arg %q twice", a.Name)
			}
		}
		if a.Type == "bool" {
			return fmt.Errorf("arg %q: a positional arg cannot be bool", a.Name)
		}
		if err := a.prepare(); err != nil {
			return fmt.Errorf("arg %q: %w", a.Name, err)
		}
	}
	return nil
}

func (f *Flag) prepare() error {
	if f.Type != "int" && (f.Min != 0 || f.Max != 0) {
		return errors.New("min and max are for int only")
	}
	if f.Type != "enum" && len(f.Values) > 0 {
		return errors.New("values are for enum only")
	}
	if f.Type != "string" && f.Pattern != "" {
		return errors.New("pattern is for string only")
	}
	switch f.Type {
	case "string":
		// Every string needs a pattern. Pack patterns are reviewed by a human
		// when a pack is written; Load only makes them mean what they say,
		// and Argv refuses flag-like values and control characters whatever
		// the pattern. It is wrapped as ^(?:pattern)$ so an
		// alternation or an escaped $ cannot loosen it, and the same wrapped
		// form goes into the JSON Schema, so both accept the same values.
		if f.Pattern == "" {
			return errors.New("string needs a pattern")
		}
		// \Q...\E quotes text in RE2 but not in ECMA, so it can hide "(?".
		if strings.Contains(f.Pattern, `\Q`) {
			return fmt.Errorf("pattern %q: no \\Q quoting", f.Pattern)
		}
		if err := checkNoInlineFlags(f.Pattern); err != nil {
			return err
		}
		re, err := regexp.Compile(f.anchored())
		if err != nil {
			return fmt.Errorf("pattern: %w", err)
		}
		f.re = re
	case "int":
		// A negative minimum would let a dash-led value through.
		if f.Min < 0 || f.Max < 1 || f.Min > f.Max {
			return fmt.Errorf("int needs 0 <= min <= max and max >= 1, got min %d max %d", f.Min, f.Max)
		}
	case "enum":
		if len(f.Values) == 0 {
			return errors.New("enum needs values")
		}
		for _, v := range f.Values {
			if err := checkString(v); err != nil || v == "" {
				return fmt.Errorf("enum value %q: empty, flag-like or has control characters", v)
			}
		}
	case "bool":
	default:
		return fmt.Errorf("type %q is not string, int, enum or bool", f.Type)
	}
	return nil
}

// anchored is Pattern as the whole-value regexp Load compiles.
func (f Flag) anchored() string { return "^(?:" + f.Pattern + ")$" }

func (e Endpoint) check() error {
	if !hostRE.MatchString(e.Host) {
		return fmt.Errorf("host %q must be a plain hostname (no wildcard, scheme, port or path)", e.Host)
	}
	h := strings.ToLower(e.Host)
	// An alphabetic last label refuses numeric host forms (2130706433,
	// 0x7f000001, 127.1) that resolvers read as IP addresses.
	if !alphaRE.MatchString(h[strings.LastIndex(h, ".")+1:]) {
		return fmt.Errorf("host %q: the last label must be letters", e.Host)
	}
	if net.ParseIP(h) != nil || h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".local") {
		return fmt.Errorf("host %q: no IP literals, localhost, .internal or .local names", e.Host)
	}
	if e.Protocol == "graphql" && e.Path == "" {
		return errors.New("a graphql endpoint needs a path")
	}
	if e.Port < 1 || e.Port > 65535 {
		return fmt.Errorf("port %d out of range", e.Port)
	}
	if e.Path != "" && !strings.HasPrefix(e.Path, "/") {
		return fmt.Errorf("path %q must start with /", e.Path)
	}
	// OpenShell reads an omitted enforcement as audit (log only).
	if e.Enforcement != "enforce" {
		return fmt.Errorf("enforcement %q: must be enforce", e.Enforcement)
	}
	if (e.Access == "") == (len(e.Rules) == 0) {
		return errors.New("exactly one of access and rules (OpenShell rejects both)")
	}
	if e.Protocol == "graphql" && e.Access != "" {
		return errors.New("a graphql endpoint takes rules allowing operation_type query, not access")
	}
	if e.Access != "" && e.Access != "read-only" {
		return fmt.Errorf("access %q: a pack is read-only", e.Access)
	}
	for i, r := range e.Rules {
		a := r.Allow
		switch e.Protocol {
		case "graphql":
			if a.OperationType != "query" || a.Method != "" || a.Path != "" {
				return fmt.Errorf("rule %d: a graphql rule allows only operation_type query", i)
			}
		case "rest":
			if (a.Method != "GET" && a.Method != "HEAD") || !strings.HasPrefix(a.Path, "/") || a.OperationType != "" {
				return fmt.Errorf("rule %d: a rest rule allows GET or HEAD on a path", i)
			}
		}
	}
	switch e.Protocol {
	case "rest", "graphql":
	default:
		return fmt.Errorf("protocol %q is not rest or graphql", e.Protocol)
	}
	return nil
}
