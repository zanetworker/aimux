package parts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Editing the config file. A command changes one thing in a file the owner wrote and reads: his comments, his
// order, his spacing and his line endings stay. Writing the file again from a parsed tree does not give that, so
// the edit is made on the text, at the place the parser says the value is, and the result is parsed again and
// compared with what was asked. An edit that cannot be made that way is refused, with the line, and nothing is
// written.

// Block is a set of settings to add, written one to a line. Flow is the same written on one line, as a key
// reference is. Their order is the order they are written in.
type (
	Block []Pair
	Flow  []Pair
	Pair  struct {
		Key   string
		Value any // true or false, a whole number, text, a Block or a Flow
	}
)

// Set changes one value in the file at path, or adds it, or with a nil value removes it, and leaves the rest of
// the file as it was. path is the keys from the top: {"parts", "inference-proxy", "enabled"}. A file that is not
// there is made, readable by its owner only.
func Set(file string, path []string, value any) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	// A link is followed: the file it points at is the one changed, and the link stays a link.
	if target, err := filepath.EvalSymlinks(file); err == nil {
		file = target
	} else if info, statErr := os.Lstat(file); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		// A link to a file that is not there yet: the file is made where the link points.
		target, linkErr := os.Readlink(file)
		if linkErr != nil {
			return linkErr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(file), target)
		}
		file = target
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return err
		}
	}
	// One edit at a time. Two that read the same text would each write their own change over the other's.
	lock, err := os.OpenFile(file+".lock", os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- beside the owner's own config file
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil { // #nosec G115 -- a file descriptor
		return err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }() // #nosec G115 -- a file descriptor

	mode := os.FileMode(0o600)
	data, err := os.ReadFile(file) // #nosec G304 -- the owner's own config file
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if info, err := os.Stat(file); err == nil {
			mode = info.Mode().Perm()
		}
	}
	edited, err := Edit(data, path, value)
	if err != nil || bytes.Equal(edited, data) {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(file), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(edited); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), mode); err != nil {
		return err
	}
	// An editor does not take the lock. If the file was saved since it was read, that save is not written over.
	if now, err := os.ReadFile(file); !bytes.Equal(now, data) && (err == nil || !errors.Is(err, os.ErrNotExist) || len(data) > 0) { // #nosec G304 -- the same file
		return errors.New("the config file was changed while this ran; nothing was written. Run it again")
	}
	return os.Rename(temp.Name(), file)
}

// Edit is Set on text. On an error the text is returned as it was given.
func Edit(data []byte, path []string, value any) ([]byte, error) {
	if len(path) == 0 {
		return data, errors.New("no setting named")
	}
	if strings.Contains(strings.ReplaceAll(string(data), "\r\n", ""), "\r") || strings.ContainsAny(string(data), "\u0085\u2028\u2029") {
		return data, errors.New("the config file has line endings this cannot follow (a carriage return on its own, or a Unicode line separator); change it by hand")
	}
	doc, err := oneDocument(data)
	if err != nil {
		return data, err
	}
	want, err := expected(data, path, value)
	if err != nil {
		return data, err
	}
	e := editor{lines: splitLines(data), eol: "\n"}
	if bytes.Contains(data, []byte("\r\n")) {
		e.eol = "\r\n"
	}
	if err := e.apply(root(doc), nil, path, value); err != nil {
		return data, err
	}
	out := []byte(strings.Join(e.lines, ""))
	// The check that makes the rest safe: the edited text has to say exactly what was asked, and nothing else
	// different from before.
	var got any
	if err := yaml.Unmarshal(out, &got); err != nil || !reflect.DeepEqual(tidy(got), tidy(want)) {
		return data, fmt.Errorf("that change could not be made safely to the file as it is written; change %s by hand", strings.Join(path, "."))
	}
	return out, nil
}

// oneDocument parses data, which has to be one YAML document or nothing.
func oneDocument(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("the config file is not valid YAML: %w", err)
	}
	var more yaml.Node
	if err := decoder.Decode(&more); !errors.Is(err, io.EOF) {
		return nil, errors.New("the config file holds more than one YAML document; it has to be one")
	}
	if top := root(&doc); doc.Kind != 0 && top == nil && len(doc.Content) > 0 && doc.Content[0].Tag != "!!null" {
		return nil, fmt.Errorf("line %d: the config file is not a list of settings", doc.Content[0].Line)
	}
	return &doc, nil
}

// expected is what the file should say after the change, as plain values.
func expected(data []byte, path []string, value any) (any, error) {
	var before any
	if err := yaml.Unmarshal(data, &before); err != nil {
		return nil, fmt.Errorf("the config file is not valid YAML: %w", err)
	}
	top, _ := before.(map[string]any)
	if top == nil {
		top = map[string]any{}
	}
	at := top
	for _, key := range path[:len(path)-1] {
		next, ok := at[key].(map[string]any)
		if !ok {
			if value == nil {
				return top, nil
			}
			next = map[string]any{}
			at[key] = next
		}
		at = next
	}
	if value == nil {
		delete(at, path[len(path)-1])
	} else {
		at[path[len(path)-1]] = plainValue(value)
	}
	return top, nil
}

func plainValue(value any) any {
	var pairs []Pair
	switch v := value.(type) {
	case Block:
		pairs = v
	case Flow:
		pairs = v
	default:
		return value
	}
	out := map[string]any{}
	for _, pair := range pairs {
		out[pair.Key] = plainValue(pair.Value)
	}
	return out
}

// tidy makes "nothing under a key" compare equal however it is written: absent settings, {} and null.
func tidy(value any) any {
	m, ok := value.(map[string]any)
	if !ok {
		return value
	}
	if len(m) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, v := range m {
		out[key] = tidy(v)
	}
	return out
}

type editor struct {
	lines []string // each with its line ending
	eol   string
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // text that ends with a line ending has no line after it
	}
	return lines
}

// apply finds path under mapping (whose own key is holder, nil at the top) and makes the change.
func (e *editor) apply(mapping, holder *yaml.Node, path []string, value any) error {
	for depth, name := range path {
		last := depth == len(path)-1
		if mapping == nil {
			// Nothing here yet: under a key with no value, or in an empty file.
			return e.add(nil, holder, path[depth:], value)
		}
		if mapping.Anchor != "" {
			return refuse(mapping, "is used in more than one place in the file (an anchor)")
		}
		key, node := lookup(mapping, name)
		if _, merged := lookup(mapping, "<<"); merged != nil {
			return refuse(merged, "is merged in from another place in the file")
		}
		if key == nil {
			return e.add(mapping, holder, path[depth:], value)
		}
		if node.Kind == yaml.AliasNode || node.Anchor != "" {
			return refuse(node, "is used in more than one place in the file (an anchor)")
		}
		if last {
			return e.change(mapping, key, node, value)
		}
		switch {
		case node.Kind == yaml.MappingNode:
			mapping, holder = node, key
		case node.Tag == "!!null":
			mapping, holder = nil, key
		default:
			return refuse(node, "is not a list of settings")
		}
	}
	return nil
}

func lookup(mapping *yaml.Node, name string) (key, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == name {
			return mapping.Content[i], mapping.Content[i+1]
		}
	}
	return nil, nil
}

func refuse(node *yaml.Node, why string) error {
	return fmt.Errorf("line %d: what is written here %s; change it by hand", node.Line, why)
}

func flow(node *yaml.Node) bool { return node != nil && node.Style&yaml.FlowStyle != 0 }

// change sets, replaces or removes a key that exists.
func (e *editor) change(mapping, key, node *yaml.Node, value any) error {
	_, complex := value.(Block)
	if _, f := value.(Flow); f {
		complex = true
	}
	if value != nil && !complex && node.Kind == yaml.ScalarNode {
		return e.replaceScalar(node, flow(mapping), scalar(value))
	}
	if flow(mapping) {
		return refuse(key, "is written on one line with its neighbours")
	}
	e.remove(key, mapping.Content[0] == key && len(mapping.Content) > 2)
	if value == nil {
		return nil
	}
	// Replacing settings with other settings: out, then in again, through the text as it now is.
	return errors.New("that setting already has settings under it; remove it first")
}

var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_./:@+-]+$`)

// scalar is a value as it is written in the file.
func scalar(value any) string {
	switch v := value.(type) {
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case string:
		var probe any
		// Plain only when it reads back as the same text: not a number, a switch, a date or an empty value.
		if plainScalar.MatchString(v) && !strings.HasPrefix(v, "-") && !strings.HasSuffix(v, ":") && yaml.Unmarshal([]byte(v), &probe) == nil && probe == any(v) {
			return v
		}
		quoted, _ := json.Marshal(v)
		return string(quoted)
	default:
		quoted, _ := json.Marshal(fmt.Sprint(v))
		return string(quoted)
	}
}

// replaceScalar swaps the text of one value for another, on its line.
func (e *editor) replaceScalar(node *yaml.Node, inFlow bool, text string) error {
	if node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || strings.Contains(node.Value, "\n") || node.Line < 1 || node.Line > len(e.lines) {
		return refuse(node, "runs over more than one line")
	}
	line := e.lines[node.Line-1]
	start := byteOffset(line, node.Column)
	if start < 0 || start >= len(line) {
		return refuse(node, "could not be found on its line")
	}
	rest := line[start:]
	end := 0
	switch rest[0] {
	case '"':
		end = closingQuote(rest, '"', true)
	case '\'':
		end = closingQuote(rest, '\'', false)
	default:
		end = len(strings.TrimRight(rest, "\r\n"))
		if i := commentStart(rest[:end]); i >= 0 {
			end = i
		}
		if inFlow {
			if i := strings.IndexAny(rest[:end], ",}]"); i >= 0 {
				end = i
			}
		}
		end = len(strings.TrimRight(rest[:end], " \t"))
	}
	if end <= 0 {
		return refuse(node, "could not be read on its line")
	}
	e.lines[node.Line-1] = line[:start] + text + rest[end:]
	return nil
}

// commentStart is where a comment's leading space begins in the rest of a line, or -1: a # after a space or a
// tab.
func commentStart(rest string) int {
	for i := 1; i < len(rest); i++ {
		if rest[i] == '#' && (rest[i-1] == ' ' || rest[i-1] == '\t') {
			return len(strings.TrimRight(rest[:i], " \t"))
		}
	}
	return -1
}

// byteOffset is where the 1-based column of a line starts, in bytes.
func byteOffset(line string, column int) int {
	seen := 1
	for i := range line {
		if seen == column {
			return i
		}
		seen++
	}
	return -1
}

// closingQuote is the length of the quoted text that starts s, its quotes included, or 0.
func closingQuote(s string, quote byte, backslash bool) int {
	for i := 1; i < len(s); i++ {
		switch {
		case backslash && s[i] == '\\':
			i++
		case s[i] == quote && !backslash && i+1 < len(s) && s[i+1] == quote:
			i++ // '' inside single quotes is one quote
		case s[i] == quote:
			return i + 1
		}
	}
	return 0
}

// indentOf is how far a line is indented, and whether it holds anything but space or a comment.
func indentOf(line string) (indent int, content bool) {
	trimmed := strings.TrimLeft(line, " ")
	body := strings.TrimRight(trimmed, "\r\n\t ")
	return len(line) - len(trimmed), body != "" && !strings.HasPrefix(body, "#")
}

// blockEnd is the last line (1-based) that belongs to the key on line keyLine: the last one below it that is
// indented further and holds something. With nothing under the key it is keyLine.
func (e *editor) blockEnd(keyLine, keyIndent int) int {
	last := keyLine
	for n := keyLine + 1; n <= len(e.lines); n++ {
		indent, content := indentOf(e.lines[n-1])
		if !content {
			continue
		}
		if indent <= keyIndent {
			break
		}
		last = n
	}
	return last
}

// remove takes out a key, everything under it, and the comment lines written directly above it. keepHeading
// leaves those comments: above the first of several keys they may be about all of them.
func (e *editor) remove(key *yaml.Node, keepHeading bool) {
	indent := key.Column - 1
	first, last := key.Line, e.blockEnd(key.Line, indent)
	for first > 1 && !keepHeading {
		above := e.lines[first-2]
		aboveIndent, content := indentOf(above)
		if content || strings.TrimSpace(above) == "" || aboveIndent != indent {
			break
		}
		first--
	}
	e.lines = append(e.lines[:first-1], e.lines[last:]...)
}

// add writes path, which is not in the file from its first key on, with value at its end. mapping is where the
// first key goes, when that is a list of settings that exists; holder is the key above it, nil at the top.
func (e *editor) add(mapping, holder *yaml.Node, path []string, value any) error {
	if value == nil {
		return nil // removing what is not there
	}
	for i := len(path) - 1; i > 0; i-- {
		value = Block{{path[i], value}}
	}
	indent, after := 0, len(e.lines)
	switch {
	case holder == nil:
		// At the top: after everything, so nothing the owner wrote moves.
	case flow(mapping) && len(mapping.Content) > 0:
		return refuse(holder, "is written on one line; put its settings on lines of their own first")
	case mapping == nil || len(mapping.Content) == 0:
		// The key is there with nothing under it, written as nothing or as {}.
		indent, after = holder.Column+1, holder.Line
		// Whatever stands for "nothing" after the colon (null, ~, {}) goes; a comment beside it stays.
		line := e.lines[holder.Line-1]
		start := byteOffset(line, holder.Column)
		colon := strings.Index(line[max(start, 0):], ":")
		if start < 0 || colon < 0 {
			return refuse(holder, "is written in a way that cannot be added to")
		}
		head, rest := line[:start+colon+1], line[start+colon+1:]
		ending := rest[len(strings.TrimRight(rest, "\r\n")):]
		rest = strings.TrimRight(rest, "\r\n")
		comment := ""
		if strings.HasPrefix(strings.TrimLeft(rest, " \t"), "#") {
			comment = rest
		} else if i := commentStart(rest); i >= 0 {
			comment = rest[i:]
		}
		e.lines[holder.Line-1] = head + comment + ending
	default:
		indent, after = mapping.Content[0].Column-1, e.blockEnd(holder.Line, holder.Column-1)
	}
	if after > 0 && !strings.HasSuffix(e.lines[after-1], "\n") {
		e.lines[after-1] += e.eol // the last line of the file had no ending
	}
	var added []string
	for _, line := range render(path[0], value, indent) {
		added = append(added, line+e.eol)
	}
	e.lines = append(e.lines[:after], append(added, e.lines[after:]...)...)
	return nil
}

// render is key and its value as lines, indented.
func render(key string, value any, indent int) []string {
	pad := strings.Repeat(" ", indent)
	switch v := value.(type) {
	case Block:
		lines := []string{pad + key + ":"}
		for _, pair := range v {
			lines = append(lines, render(pair.Key, pair.Value, indent+2)...)
		}
		return lines
	case Flow:
		return []string{pad + key + ": " + renderFlow(v)}
	default:
		return []string{pad + key + ": " + scalar(value)}
	}
}

func renderFlow(pairs Flow) string {
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		text := ""
		switch v := pair.Value.(type) {
		case Flow:
			text = renderFlow(v)
		case Block:
			text = renderFlow(Flow(v))
		default:
			text = scalar(v)
		}
		parts = append(parts, pair.Key+": "+text)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
