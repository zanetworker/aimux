package parts

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A service's job file, and the part of it aimux decides. aimux owns the program and the arguments that come
// from the settings. Everything else in the file (its environment, its log paths, when it restarts) and every
// other argument is the owner's and stays as it is. A file that cannot be read that way is refused, not rewritten.

const argumentsKey = "<key>ProgramArguments</key>"

var argumentString = regexp.MustCompile(`(?s)<string>(.*?)</string>|<string\s*/>`)

// topKeys are the keys of the job file's top dictionary, read as XML reads them.
func topKeys(plist []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(plist))
	var keys []string
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			if depth != 0 || len(keys) == 0 {
				return nil, errors.New("this is not a launchd job file")
			}
			return keys, nil
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 3 && element.Name.Local == "key" { // plist > dict > key
				var name string
				if err := decoder.DecodeElement(&name, &element); err != nil {
					return nil, errors.New("this is not a launchd job file")
				}
				depth--
				keys = append(keys, name)
			}
		case xml.EndElement:
			depth--
		case xml.Comment:
			// A comment can hold what looks like the list of arguments; the search below works on text.
			return nil, errors.New("the job file has a comment in it, which aimux does not edit around; change the job by hand")
		}
	}
}

// argumentsSpan is where the list of arguments is in a launchd job file: from after <array> to before </array>.
func argumentsSpan(plist []byte) (start, end int, err error) {
	keys, err := topKeys(plist)
	if err != nil {
		return 0, 0, err
	}
	lists := 0
	for _, key := range keys {
		switch key {
		case "Program":
			return 0, 0, errors.New("the job file names its program under a separate Program key, which aimux does not change; edit it by hand")
		case "ProgramArguments":
			lists++
		}
	}
	// Once at the top, and nowhere else as text: then the text search below can only find that one.
	if lists != 1 || bytes.Count(plist, []byte(argumentsKey)) != 1 {
		return 0, 0, errors.New("the job file does not have exactly one ProgramArguments; edit it by hand")
	}
	key := bytes.Index(plist, []byte(argumentsKey))
	rest := plist[key+len(argumentsKey):]
	open := bytes.Index(rest, []byte("<array>"))
	if open < 0 || len(bytes.TrimSpace(rest[:open])) != 0 {
		return 0, 0, errors.New("ProgramArguments in the job file is not a list")
	}
	start = key + len(argumentsKey) + open + len("<array>")
	length := bytes.Index(plist[start:], []byte("</array>"))
	if length < 0 {
		return 0, 0, errors.New("ProgramArguments in the job file is not closed")
	}
	return start, start + length, nil
}

// JobArgs reads the program and its arguments from a launchd job file.
func JobArgs(plist []byte) ([]string, error) {
	start, end, err := argumentsSpan(plist)
	if err != nil {
		return nil, err
	}
	inside := plist[start:end]
	// Only strings, and nothing between them: anything else is something aimux would lose by rewriting the list.
	if len(bytes.TrimSpace(argumentString.ReplaceAll(inside, nil))) != 0 {
		return nil, errors.New("ProgramArguments in the job file holds something other than plain strings; edit it by hand")
	}
	var args []string
	for _, match := range argumentString.FindAllSubmatch(inside, -1) {
		var text string
		if err := xml.Unmarshal(append(append([]byte("<s>"), match[1]...), "</s>"...), &text); err != nil {
			return nil, errors.New("an argument in the job file could not be read")
		}
		args = append(args, text)
	}
	if len(args) == 0 {
		return nil, errors.New("ProgramArguments in the job file is empty")
	}
	return args, nil
}

// SetJobArgs is plist with its arguments replaced by args, and every other byte as it was. The same arguments
// give back the same file.
func SetJobArgs(plist []byte, args []string) ([]byte, error) {
	current, err := JobArgs(plist)
	if err != nil {
		return nil, err
	}
	if slices.Equal(current, args) {
		return plist, nil
	}
	if len(args) == 0 {
		return nil, errors.New("a job needs a program to run")
	}
	start, end, _ := argumentsSpan(plist)
	// Indented as the file indents them: the list's own line, and its first entry.
	lineStart := bytes.LastIndexByte(plist[:start], '\n') + 1
	base := string(plist[lineStart : start-len("<array>")])
	if strings.TrimSpace(base) != "" {
		base = "" // the list starts on a line that holds something else: only space is indentation
	}
	inside := plist[start:end]
	first := bytes.Index(inside, []byte("<string"))
	entry := base + "\t"
	if at := bytes.LastIndexByte(inside[:first], '\n'); at >= 0 && strings.TrimSpace(string(inside[at+1:first])) == "" {
		entry = string(inside[at+1 : first])
	}
	eol := "\n"
	if bytes.Contains(inside, []byte("\r\n")) {
		eol = "\r\n"
	}
	var list bytes.Buffer
	list.WriteString(eol)
	for _, arg := range args {
		list.WriteString(entry + "<string>")
		if err := xml.EscapeText(&list, []byte(arg)); err != nil {
			return nil, err
		}
		list.WriteString("</string>" + eol)
	}
	list.WriteString(base)
	out := append([]byte{}, plist[:start]...)
	out = append(out, list.Bytes()...)
	out = append(out, plist[end:]...)
	// What was written has to read back as what was asked: XML cannot hold every character.
	if written, err := JobArgs(out); err != nil || !slices.Equal(written, args) {
		return nil, errors.New("an argument holds a character a job file cannot hold")
	}
	return out, nil
}

// proxyFlags are the arguments of the model proxy that come from the settings.
type proxyFlags struct {
	port, tier, binary string
	anthropic          bool
	upstreams          map[string]string // name -> address
	keys               map[string]string // name -> key reference, as JSON
}

// The model proxy's flags that take a value, and the ones that may repeat.
var proxyValueFlags = map[string]bool{"--port": true, "--service-tier": true, "--codex-command": true, "--messages-upstream": true, "--messages-upstream-key": true}

// split takes a service's arguments apart: the program, what aimux owns, and the rest in the order it was.
func split(service Service, args []string) (program string, owned proxyFlags, other []string, err error) {
	owned.upstreams, owned.keys = map[string]string{}, map[string]string{}
	if len(args) < 1+len(service.Serve) || !slices.Equal(args[1:1+len(service.Serve)], service.Serve) {
		return "", owned, nil, fmt.Errorf("the job does not start the %s the way aimux starts it (%s); aimux leaves it alone", service.Name, strings.Join(service.Serve, " "))
	}
	program = args[0]
	rest := args[1+len(service.Serve):]
	if service.ID != "model-proxy" {
		return program, owned, slices.Clone(rest), nil // nothing of this service's is set from the file yet
	}
	for i := 0; i < len(rest); i++ {
		flag, value, joined := strings.Cut(rest[i], "=")
		switch {
		case rest[i] == "--":
			// What follows is not flags, and a flag written after it would not be one either.
			return "", owned, nil, errors.New("the job's arguments hold a bare --, after which aimux cannot add a setting; change the job by hand")
		case flag == "--anthropic":
			owned.anthropic = true
			if joined {
				if owned.anthropic, err = strconv.ParseBool(value); err != nil {
					return "", owned, nil, errors.New("the job's --anthropic has a value that is neither true nor false")
				}
			}
			continue
		case !proxyValueFlags[flag]:
			other = append(other, rest[i])
			continue
		case !joined && i+1 < len(rest):
			i++
			value = rest[i]
		case !joined:
			return "", owned, nil, fmt.Errorf("the job's %s has no value", flag)
		}
		switch flag {
		case "--port":
			owned.port = value
		case "--service-tier":
			owned.tier = value
		case "--codex-command":
			owned.binary = value
		default:
			name, what, ok := strings.Cut(value, "=")
			if !ok {
				return "", owned, nil, fmt.Errorf("the job's %s is not name=value", flag)
			}
			into := owned.upstreams
			if flag == "--messages-upstream-key" {
				into = owned.keys
			}
			if _, twice := into[name]; twice {
				return "", owned, nil, fmt.Errorf("the job gives %s for %q twice", flag, cutName(name))
			}
			into[name] = what
		}
	}
	for name := range owned.keys {
		if _, ok := owned.upstreams[name]; !ok {
			return "", owned, nil, fmt.Errorf("the job gives a key for %q, which is not one of its upstreams", cutName(name))
		}
	}
	return program, owned, other, nil
}

// cutName is a name from a job file as an error may repeat it.
func cutName(name string) string {
	if !engineName.MatchString(name) {
		return "?"
	}
	return name
}

// args is the flags as arguments, in one fixed order.
func (f proxyFlags) args() []string {
	var out []string
	for _, pair := range [][2]string{{"--port", f.port}, {"--service-tier", f.tier}, {"--codex-command", f.binary}} {
		if pair[1] != "" {
			out = append(out, pair[0], pair[1])
		}
	}
	if f.anthropic {
		out = append(out, "--anthropic")
	}
	names := make([]string, 0, len(f.upstreams))
	for name := range f.upstreams {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		out = append(out, "--messages-upstream", name+"="+f.upstreams[name])
		if key, ok := f.keys[name]; ok {
			out = append(out, "--messages-upstream-key", name+"="+key)
		}
	}
	return out
}

// WantedArgs is what the service's job should run, given what it runs now and what the settings state. A
// setting the file leaves out keeps what is installed; one it states replaces it. Engines are stated together:
// once the file lists engines, the ones the job had are replaced by the ones listed. Arguments aimux does not own
// are kept, in their order.
func WantedArgs(service Service, current []string, settings PartSettings) ([]string, error) {
	program, flags, other, err := split(service, current)
	if err != nil {
		return nil, err
	}
	if service.ID == "model-proxy" {
		_, stated, _, _ := split(service, append(append([]string{program}, service.Serve...), settings.ProxyArgs()...))
		if stated.port != "" {
			flags.port = stated.port
		}
		if stated.tier != "" {
			flags.tier = stated.tier
		}
		if stated.binary != "" {
			flags.binary = stated.binary
		}
		if settings.Engines != nil {
			flags.anthropic, flags.upstreams, flags.keys = stated.anthropic, stated.upstreams, stated.keys
		}
	}
	return assemble(service, program, flags, other), nil
}

func assemble(service Service, program string, flags proxyFlags, other []string) []string {
	out := append([]string{program}, service.Serve...)
	out = append(out, other...)
	return append(out, flags.args()...)
}

// SameArgs reports whether two argument lists start the service the same way. The order aimux's own flags are
// written in, and --flag=value against --flag value, make no difference; a job is not rewritten for them.
func SameArgs(service Service, a, b []string) bool {
	canonical := func(args []string) ([]string, bool) {
		program, flags, other, err := split(service, args)
		if err != nil {
			return nil, false
		}
		return assemble(service, program, flags, other), true
	}
	first, okA := canonical(a)
	second, okB := canonical(b)
	if !okA || !okB {
		return slices.Equal(a, b)
	}
	return slices.Equal(first, second)
}
