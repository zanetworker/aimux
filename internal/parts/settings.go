package parts

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zanetworker/aimux/internal/secretref"
)

// The section of the config file that says which parts are on, and how. Its shape and the reason for each key
// are in docs/superpowers/specs/2026-11-25-parts-settings.md.

// What an engine's type and auth can be.
const (
	TypeCodexAppServer = "codex-app-server" // the Codex program, spoken to over its standard input and output
	TypeMessages       = "messages"         // the Messages API at a url
	AuthCaller         = "caller"           // the session's own login is passed on
	AuthKey            = "key"              // the owner's key for that engine is sent; the caller's credentials stay behind
	APIMessages        = "messages"         // the one API the proxy serves to clients in this build

	EngineCodex     = "codex"
	EngineAnthropic = "anthropic"
	// AnthropicURL is where the built-in Anthropic engine sends. It is not a setting: a login goes nowhere else.
	AnthropicURL = "https://api.anthropic.com"
)

var engineName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Settings is what the owner wants on. The zero value is "nothing stated": aimux manages no job.
type Settings struct {
	// Stated: the file has a parts section with something in it.
	Stated bool
	// Parts holds only the parts the file names. A part that is absent is not managed.
	Parts map[string]PartSettings

	problems []Problem
}

// PartSettings is one part as the file states it. A field left empty was not stated, which for a job that
// exists means "as it is installed".
type PartSettings struct {
	Enabled *bool // nil only in a file with a problem: a part that is listed says true or false
	Port    int
	Serves  []string
	Engines map[string]Engine
}

// Engine is one engine as the file states it.
type Engine struct {
	Type        string
	Auth        string
	Enabled     *bool
	URL         string
	Key         *secretref.Ref
	ServiceTier string
	Binary      string
}

// EngineView is an engine with what the file left out filled in.
type EngineView struct {
	Name, Type, Auth, URL string
	On                    bool
	Key                   *secretref.Ref // where a key engine's key is kept
	KeyItem               string         // the Keychain item, when that is where
	ServiceTier, Binary   string
}

// Problem is one thing wrong with the section, said so the owner can find and fix it. It never repeats a value
// from the file: what is written where a key reference belongs may be a key.
type Problem struct {
	Path    string // "parts.inference-proxy.port"
	Line    int    // in the file; 0 when not known
	Message string
}

func (p Problem) String() string {
	where := p.Path
	if p.Line > 0 {
		where = fmt.Sprintf("line %d, %s", p.Line, p.Path)
	}
	if where == "" {
		return p.Message
	}
	return where + ": " + p.Message
}

// Validate returns what is wrong with the section; nothing is applied from a file that has a problem.
func (s Settings) Validate() []Problem { return slices.Clone(s.problems) }

// Engine is the named engine with defaults filled in. A name the file does not list gives the built-in's
// defaults for codex, and an engine that is off for any other.
func (p PartSettings) Engine(name string) EngineView {
	stated, listed := p.Engines[name]
	view := EngineView{Name: name, Type: stated.Type, Auth: stated.Auth, URL: stated.URL, ServiceTier: stated.ServiceTier, Binary: stated.Binary}
	view.On = listed && (stated.Enabled == nil || *stated.Enabled)
	switch name {
	case EngineCodex:
		view.Type, view.On = TypeCodexAppServer, true
	case EngineAnthropic:
		view.Type, view.Auth, view.URL = TypeMessages, AuthCaller, AnthropicURL
	default:
		if view.Auth == "" {
			view.Auth = AuthKey
		}
		view.Key = stated.Key
		if view.Key == nil {
			view.Key = &secretref.Ref{Keychain: &secretref.KeychainRef{Item: name + "-key"}}
		}
		if view.Key.Keychain != nil {
			view.KeyItem = view.Key.Keychain.Item
		}
	}
	return view
}

// Load reads the parts section of the config file at path. A missing file or section is the zero value. An
// error is a file that cannot be read as YAML at all; anything wrong inside the section is a Problem.
func Load(path string) (Settings, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the owner's own config file
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	return Parse(data)
}

// Parse is Load for text already read.
func Parse(data []byte) (Settings, error) {
	doc, err := oneDocument(data)
	if err != nil {
		return Settings{}, err
	}
	if top := root(doc); top != nil {
		sections := 0
		for i := 0; i+1 < len(top.Content); i += 2 {
			if top.Content[i].Value != "parts" {
				continue
			}
			if sections++; sections > 1 {
				return Settings{}, fmt.Errorf("line %d: parts is written twice in the file", top.Content[i].Line)
			}
		}
		// A section brought in by a merge is real to YAML and invisible here: it is asked to be written out.
		if merge := child(top, "<<"); merge != nil && sections == 0 {
			var whole map[string]any
			if yaml.Unmarshal(data, &whole) == nil && whole["parts"] != nil {
				return Settings{}, fmt.Errorf("line %d: parts comes from a merge; write it out in the file itself", merge.Line)
			}
		}
	}
	section := child(root(doc), "parts")
	if section == nil || section.Tag == "!!null" {
		return Settings{}, nil
	}
	if section.Kind != yaml.MappingNode {
		return Settings{}, fmt.Errorf("line %d: parts has to be a list of parts by name", section.Line)
	}
	s := Settings{Parts: map[string]PartSettings{}}
	s.twice("parts", section)
	for i := 0; i+1 < len(section.Content); i += 2 {
		id, node := section.Content[i].Value, section.Content[i+1]
		s.Parts[id] = s.part(id, section.Content[i], node)
	}
	s.Stated = len(s.Parts) > 0
	return s, nil
}

// root is the top mapping of a document, or nil.
func root(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0]
	}
	return nil
}

// child is the value of key in a mapping, or nil.
func child(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// repeated is the second writing of a key that a mapping holds twice, or nil. YAML lets the later one win
// silently; here it is a mistake to be told about.
func repeated(mapping *yaml.Node) *yaml.Node {
	seen := map[string]bool{}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if seen[mapping.Content[i].Value] {
			return mapping.Content[i]
		}
		seen[mapping.Content[i].Value] = true
	}
	return nil
}

func (s *Settings) twice(path string, mapping *yaml.Node) {
	if repeat := repeated(mapping); repeat != nil {
		s.problem(path+"."+plain(repeat.Value), repeat, "this is written twice; keep one")
	}
}

func (s *Settings) problem(path string, node *yaml.Node, format string, args ...any) {
	s.problems = append(s.problems, Problem{Path: path, Line: node.Line, Message: fmt.Sprintf(format, args...)})
}

func (s *Settings) part(id string, key, node *yaml.Node) PartSettings {
	path := "parts." + id
	var out PartSettings
	known, ok := PartByID(id)
	switch {
	case !ok:
		s.problem(path, key, "no part is called that; the parts are: %s", strings.Join(partIDs(), ", "))
		return out
	case known.Always:
		s.problem(path, key, "this part is always on and has no settings")
		return out
	case node.Kind != yaml.MappingNode:
		s.problem(path, node, "a part is written as its settings, starting with enabled: true or enabled: false")
		return out
	}
	s.twice(path, node)
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, value := node.Content[i].Value, node.Content[i+1]
		field := path + "." + name
		proxyOnly := id == "inference-proxy"
		switch {
		case name == "enabled":
			out.Enabled = s.boolean(field, value)
		case name == "port" && proxyOnly:
			if value.Tag != "!!int" || value.Decode(&out.Port) != nil || out.Port < 1 || out.Port > 65535 {
				out.Port = 0
				s.problem(field, value, "a port is a number from 1 to 65535")
			}
		case name == "serves" && proxyOnly:
			if value.Decode(&out.Serves) != nil {
				s.problem(field, value, "serves is a list of APIs, for example [messages]")
			}
			for _, api := range out.Serves {
				if api != APIMessages {
					s.problem(field, value, "this build serves only the %s API; %q is not one it knows", APIMessages, plain(api))
				}
			}
		case name == "engines" && proxyOnly:
			out.Engines = s.engines(field, value)
		case name == "port" || name == "serves" || name == "engines":
			s.problem(field, node.Content[i], "%s is not a setting of this part", name)
		default:
			s.problem(field, node.Content[i], "%q is not a setting of a part", plain(name))
		}
	}
	switch {
	case out.Enabled == nil:
		s.problem(path+".enabled", key, "a part that is listed says enabled: true or enabled: false; to leave it as it is, take it out of the file")
	case *out.Enabled && !known.Available:
		s.problem(path, key, "%s is not available yet", known.Name)
	}
	return out
}

func (s *Settings) boolean(path string, node *yaml.Node) *bool {
	var value bool
	if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" || node.Decode(&value) != nil {
		s.problem(path, node, "write true or false")
		return nil
	}
	return &value
}

func (s *Settings) engines(path string, node *yaml.Node) map[string]Engine {
	if node.Tag == "!!null" {
		// "engines:" with nothing under it states that there are none beyond Codex. That is not the same as
		// leaving the key out, which means "as installed": the map is empty, not nil.
		return map[string]Engine{}
	}
	if node.Kind != yaml.MappingNode {
		s.problem(path, node, "engines is a list of engines by name")
		return nil
	}
	out := map[string]Engine{}
	s.twice(path, node)
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		if !engineName.MatchString(name) {
			s.problem(path+"."+plain(name), node.Content[i], "%q cannot be an engine's name: it is typed before the colon of a model name, so lower-case letters, digits and hyphens, starting with a letter", plain(name))
			continue
		}
		out[name] = s.engine(path+"."+name, name, node.Content[i], node.Content[i+1])
	}
	return out
}

func (s *Settings) engine(path, name string, key, node *yaml.Node) Engine {
	var out Engine
	if node.Tag == "!!null" {
		node = &yaml.Node{Kind: yaml.MappingNode, Line: key.Line} // "anthropic:" with nothing under it is an engine with no settings
	}
	if node.Kind != yaml.MappingNode {
		s.problem(path, node, "an engine is written as its settings")
		return out
	}
	stated := map[string]*yaml.Node{}
	s.twice(path, node)
	for i := 0; i+1 < len(node.Content); i += 2 {
		field, value := node.Content[i].Value, node.Content[i+1]
		stated[field] = value
		at := path + "." + field
		switch field {
		case "enabled":
			out.Enabled = s.boolean(at, value)
		case "type", "auth", "url", "service_tier", "binary":
			var text string
			if value.Kind != yaml.ScalarNode || value.Decode(&text) != nil {
				s.problem(at, value, "write one value here")
				continue
			}
			switch field {
			case "type":
				out.Type = text
			case "auth":
				out.Auth = text
			case "url":
				out.URL = text
			case "service_tier":
				out.ServiceTier = text
			case "binary":
				out.Binary = text
			}
		case "key":
			ref, ok := keyReference(value)
			if !ok {
				s.problem(at, value, "key says where the key is kept, never the key itself: one of {keychain: {item: ...}}, {env: {name: ...}}, {file: {path: ...}}, {cmd: {argv: [...]}}")
				continue
			}
			out.Key = &ref
		default:
			s.problem(at, node.Content[i], "%q is not a setting of an engine", plain(field))
		}
	}
	not := func(field, why string) {
		if value, ok := stated[field]; ok {
			s.problem(path+"."+field, value, "%s", why)
		}
	}
	switch name {
	case EngineCodex:
		if out.Type != "" && out.Type != TypeCodexAppServer {
			s.problem(path+".type", stated["type"], "codex is the Codex program; its type is %s and can be left out", TypeCodexAppServer)
		}
		if out.Enabled != nil && !*out.Enabled {
			s.problem(path+".enabled", stated["enabled"], "codex is built into the proxy and cannot be turned off")
		}
		if out.ServiceTier != "" && out.ServiceTier != "priority" && out.ServiceTier != "standard" {
			s.problem(path+".service_tier", stated["service_tier"], "the tier is priority or standard, not %q", plain(out.ServiceTier))
		}
		for _, field := range []string{"url", "auth", "key"} {
			not(field, field+" is not a setting of codex: the Codex program uses its own login")
		}
	case EngineAnthropic:
		if out.Type != "" && out.Type != TypeMessages {
			s.problem(path+".type", stated["type"], "anthropic speaks the %s API; its type can be left out", TypeMessages)
		}
		if out.Auth != "" && out.Auth != AuthCaller {
			s.problem(path+".auth", stated["auth"], "anthropic is sent the session's own login (auth: %s), which can be left out", AuthCaller)
		}
		not("url", "anthropic always goes to "+AnthropicURL+": a login is sent nowhere else. For another address, add an engine under a name of its own")
		not("key", "anthropic uses the session's own login and has no key")
		not("service_tier", "service_tier is a setting of codex")
		not("binary", "binary is a setting of codex")
	default:
		switch {
		case out.Type == "":
			s.problem(path+".type", key, "an engine under a name of its own says its type; the one this build knows is %s", TypeMessages)
		case out.Type != TypeMessages:
			s.problem(path+".type", stated["type"], "this build knows the type %s for a named engine, not %q", TypeMessages, plain(out.Type))
		}
		if out.Auth != "" && out.Auth != AuthKey {
			s.problem(path+".auth", stated["auth"], "a named engine is sent the owner's key (auth: %s); a session's login is only ever sent to Anthropic", AuthKey)
		}
		if out.URL == "" {
			s.problem(path+".url", key, "a %s engine needs its url", TypeMessages)
		} else if why := checkOrigin(out.URL); why != "" {
			s.problem(path+".url", stated["url"], "%s", why)
		}
		not("service_tier", "service_tier is a setting of codex")
		not("binary", "binary is a setting of codex")
	}
	return out
}

// keyReference reads a key reference strictly: a field it does not know is a mistake, not something to skip,
// since a misspelt field would quietly point at a different secret.
func keyReference(node *yaml.Node) (secretref.Ref, bool) {
	var ref secretref.Ref
	if node.Kind != yaml.MappingNode {
		return ref, false
	}
	text, err := yaml.Marshal(node)
	if err != nil {
		return ref, false
	}
	decoder := yaml.NewDecoder(bytes.NewReader(text))
	decoder.KnownFields(true)
	if decoder.Decode(&ref) != nil || ref.Validate() != nil {
		return ref, false
	}
	return ref, true
}

// checkOrigin says what is wrong with an engine's address, or nothing. The owner's key and every conversation go
// there, so it is a bare origin, and https unless it is on this machine.
func checkOrigin(address string) string {
	origin, err := url.Parse(address)
	bare := err == nil && origin.User == nil && origin.RawQuery == "" && origin.Fragment == "" && (origin.Path == "" || origin.Path == "/")
	port := 1
	if err == nil && origin.Port() != "" {
		port, _ = strconv.Atoi(origin.Port())
	}
	if err != nil || origin.Hostname() == "" || port < 1 || port > 65535 || !bare || (origin.Scheme != "https" && origin.Scheme != "http") {
		return "the url is an address with a host and nothing after it, such as https://gateway.example"
	}
	if host := origin.Hostname(); origin.Scheme != "https" && host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return "an address on another machine has to be https"
	}
	return ""
}

// plain is text from the file as a message may repeat it: a short name, or a placeholder.
func plain(text string) string {
	if len(text) > 40 || strings.ContainsAny(text, "\n\r\t") {
		return "..."
	}
	return text
}

func partIDs() []string {
	var ids []string
	for _, p := range Parts() {
		if !p.Always {
			ids = append(ids, p.ID)
		}
	}
	return ids
}
