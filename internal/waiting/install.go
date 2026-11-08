package waiting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/jsonedit"
)

// commandSuffix ends every hook command aimux installs, optionally followed
// by neverFail; it is how aimux's entries are told apart from the owner's.
const commandSuffix = " remote hooks notify"

// neverFail keeps Claude Code from showing a hook error when the binary is
// gone (a moved or deleted build); the hook then does nothing.
const neverFail = " || true"

// hookTimeout (seconds) bounds one hook call; the helper takes milliseconds.
const hookTimeout = 10

// HookCommand is the hook command for the aimux binary at binary, quoted
// for the shell when it needs to be.
func HookCommand(binary string) string {
	return shellQuote(binary) + commandSuffix + neverFail
}

// isOurs: command is one aimux installed, in either form (with or without
// neverFail).
func isOurs(command string) bool {
	return strings.HasSuffix(strings.TrimSuffix(command, neverFail), commandSuffix)
}

// CommandBinary is the binary an aimux hook command runs, unquoted; false
// for any other command.
func CommandBinary(command string) (string, bool) {
	rest, ok := strings.CutSuffix(strings.TrimSuffix(command, neverFail), commandSuffix)
	if !ok || rest == "" {
		return "", false
	}
	if len(rest) >= 2 && rest[0] == '\'' && rest[len(rest)-1] == '\'' {
		rest = strings.ReplaceAll(rest[1:len(rest)-1], `'\''`, "'")
	}
	return rest, true
}

var shellPlain = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if shellPlain.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Events are the hook events aimux installs, in the order it adds them.
func Events() []string { return append([]string{SetEvent}, ClearEvents...) }

// Change is what Install or Uninstall did, by event. Backup is the copy of
// the settings made before writing; empty when nothing was written or there
// was no file.
type Change struct {
	Added, Updated, Removed []string
	Backup                  string
}

// State is what Status found: the events with aimux's hook, the events
// without, and the distinct commands those hooks run.
type State struct {
	Installed, Missing, Commands []string
}

// field is one member of a JSON object, kept in file order.
type field struct {
	Key   string
	Value json.RawMessage
}

type object []field

func (o object) get(k string) (json.RawMessage, bool) {
	for _, f := range o {
		if f.Key == k {
			return f.Value, true
		}
	}
	return nil, false
}

func (o *object) set(k string, v json.RawMessage) {
	for i, f := range *o {
		if f.Key == k {
			(*o)[i].Value = v
			return
		}
	}
	*o = append(*o, field{k, v})
}

func (o *object) del(k string) {
	for i, f := range *o {
		if f.Key == k {
			*o = append((*o)[:i], (*o)[i+1:]...)
			return
		}
	}
}

func parseObject(data []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var o object
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o = append(o, field{k, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("text after the JSON object")
	}
	return o, nil
}

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(f.Value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// hookGroup is one matcher group as aimux writes it.
type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// handlerCommand returns a handler's command when it is one of aimux's.
func handlerCommand(raw json.RawMessage) (string, bool) {
	var h hookHandler
	if json.Unmarshal(raw, &h) != nil || h.Type != "command" || !isOurs(h.Command) {
		return "", false
	}
	return h.Command, true
}

// group is a matcher group split into its members (order kept) and its
// handlers.
type group struct {
	obj      object
	handlers []json.RawMessage
}

func parseGroup(raw json.RawMessage) (group, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return group{}, false
	}
	hs, _ := obj.get("hooks")
	var handlers []json.RawMessage
	if json.Unmarshal(hs, &handlers) != nil {
		return group{}, false
	}
	return group{obj, handlers}, true
}

// ours returns the index of aimux's handler in a group, or -1.
func (g group) ours() int {
	for i, h := range g.handlers {
		if _, ok := handlerCommand(h); ok {
			return i
		}
	}
	return -1
}

func (g group) marshal() json.RawMessage {
	hs, _ := marshal(g.handlers)
	g.obj.set("hooks", hs)
	b, _ := g.obj.MarshalJSON()
	return b
}

func matcherFor(event string) string {
	if event == SetEvent {
		return SetMatcher
	}
	return ""
}

// settings is a settings file read for changing.
type settings struct {
	exists bool
	raw    []byte
	hooks  object
	groups map[string][]json.RawMessage
}

func load(path string) (*settings, error) {
	s := &settings{groups: map[string][]json.RawMessage{}}
	data, err := os.ReadFile(path) //nolint:gosec // the owner's own Claude Code settings path
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	s.exists, s.raw = true, data
	if len(bytes.TrimSpace(data)) == 0 {
		return s, nil
	}
	top, err := parseObject(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not a JSON object (%v); fix it first, nothing was changed", path, err)
	}
	raw, _ := top.get("hooks")
	if err := s.readHooks(raw); err != nil {
		return nil, fmt.Errorf("%s: %v; nothing was changed", path, err)
	}
	return s, nil
}

// readHooks sets s.hooks and s.groups from the raw "hooks" value (nil: none).
func (s *settings) readHooks(raw json.RawMessage) error {
	s.hooks, s.groups = nil, map[string][]json.RawMessage{}
	if raw == nil {
		return nil
	}
	hooks, err := parseObject(raw)
	if err != nil {
		return fmt.Errorf("\"hooks\" is not an object (%v)", err)
	}
	for _, f := range hooks {
		var list []json.RawMessage
		if err := json.Unmarshal(f.Value, &list); err != nil {
			return fmt.Errorf("hooks.%s is not a list (%v)", f.Key, err)
		}
		s.groups[f.Key] = list
	}
	s.hooks = hooks
	return nil
}

// hooksValue is the "hooks" value for s, nil when no event is left.
func (s *settings) hooksValue() (json.RawMessage, error) {
	for _, f := range s.hooks {
		list, err := marshal(s.groups[f.Key])
		if err != nil {
			return nil, err
		}
		s.hooks.set(f.Key, list)
	}
	if len(s.hooks) == 0 {
		return nil, nil
	}
	return s.hooks.MarshalJSON()
}

// edit applies change to the settings at path. A dry run on the file as it is
// decides whether anything changes (and refuses broken settings); only then is
// the file backed up (or created, 0600 in a 0700 dir, when missing) and the
// "hooks" key rewritten through jsonedit.EditKey: atomic, verified, every other
// top-level key byte-identical, retried from a fresh read if Claude Code writes
// meanwhile. change runs again on each fresh read and must be pure; the Change
// returned is the one that was written.
func edit(path string, now time.Time, change func(*settings) Change) (Change, error) {
	s, err := load(path)
	if err != nil {
		return Change{}, err
	}
	if ch := change(s); len(ch.Added)+len(ch.Updated)+len(ch.Removed) == 0 {
		return ch, nil
	}
	backup := ""
	if s.exists && len(bytes.TrimSpace(s.raw)) > 0 {
		backup = path + ".aimux-bak-" + now.UTC().Format("20060102T150405.000000000Z")
		if err := os.WriteFile(backup, s.raw, 0o600); err != nil { // #nosec G703 -- next to the owner's settings.json (HooksSettings), not external input
			return Change{}, fmt.Errorf("back up %s: %w", path, err)
		}
	} else if err := startObject(path); err != nil {
		return Change{}, err
	}
	var ch Change
	err = jsonedit.EditKey(path, "hooks", func(old json.RawMessage) (json.RawMessage, error) {
		cur := &settings{}
		if err := cur.readHooks(old); err != nil {
			return nil, err
		}
		ch = change(cur)
		return cur.hooksValue()
	})
	ch.Backup = backup
	return ch, err
}

// startObject makes a missing or blank settings file an empty JSON object for
// EditKey to fill: created 0600 (dir 0700) when missing, appended to when it
// holds only whitespace. A file someone else created meanwhile is left alone.
func startObject(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the owner's own Claude Code settings path
	if errors.Is(err, fs.ErrExist) {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0) //nolint:gosec // same path
		if err != nil {
			return err
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // same path
		if rerr != nil || len(bytes.TrimSpace(data)) > 0 {
			_ = f.Close()
			return rerr // filled meanwhile: EditKey takes it from here
		}
	} else if err != nil {
		return err
	}
	_, werr := f.WriteString("{}\n")
	return errors.Join(werr, f.Close())
}

// Install adds aimux's hooks (command) to the Claude Code settings at path,
// creating the file if missing and keeping every other key and hook. An
// aimux hook with another command (the binary moved) is updated in place.
// Nothing is written when nothing changes.
func Install(path, command string, now time.Time) (Change, error) {
	return edit(path, now, func(s *settings) Change { return installChange(s, command) })
}

// installChange adds or updates aimux's hook in s for every event.
func installChange(s *settings, command string) Change {
	var ch Change
	for _, ev := range Events() {
		handler := hookHandler{Type: "command", Command: command, Timeout: hookTimeout}
		want, _ := marshal(hookGroup{Matcher: matcherFor(ev), Hooks: []hookHandler{handler}})
		wantHandler, _ := marshal(handler)
		list, found := s.groups[ev], false
		for i, raw := range list {
			g, ok := parseGroup(raw)
			j := -1
			if ok {
				j = g.ours()
			}
			if j < 0 {
				continue
			}
			found = true
			switch {
			case len(g.handlers) == 1 && !jsonEqual(raw, want):
				list[i] = want
				ch.Updated = append(ch.Updated, ev)
			case len(g.handlers) > 1 && !jsonEqual(g.handlers[j], wantHandler):
				g.handlers[j] = wantHandler // in a group of the owner's: only our handler changes
				list[i] = g.marshal()
				ch.Updated = append(ch.Updated, ev)
			}
			break
		}
		if !found {
			s.groups[ev] = append(list, want)
			if _, ok := s.hooks.get(ev); !ok {
				s.hooks.set(ev, nil)
			}
			ch.Added = append(ch.Added, ev)
		}
	}
	return ch
}

// Uninstall removes aimux's hooks from the settings at path, and an event
// list or the hooks object it leaves empty; nothing else.
func Uninstall(path string, now time.Time) (Change, error) {
	return edit(path, now, uninstallChange)
}

// uninstallChange removes aimux's handlers from s, dropping emptied events.
func uninstallChange(s *settings) Change {
	var ch Change
	for _, f := range append(object(nil), s.hooks...) {
		var kept []json.RawMessage
		removed := false
		for _, raw := range s.groups[f.Key] {
			g, ok := parseGroup(raw)
			if !ok || g.ours() < 0 {
				kept = append(kept, raw)
				continue
			}
			removed = true
			var others []json.RawMessage
			for _, h := range g.handlers {
				if _, ours := handlerCommand(h); !ours {
					others = append(others, h)
				}
			}
			if len(others) > 0 { // the owner's handlers stay in their group
				g.handlers = others
				kept = append(kept, g.marshal())
			}
		}
		if !removed {
			continue
		}
		ch.Removed = append(ch.Removed, f.Key)
		if len(kept) == 0 {
			s.hooks.del(f.Key)
			delete(s.groups, f.Key)
		} else {
			s.groups[f.Key] = kept
		}
	}
	return ch
}

// Status reports which of aimux's hooks the settings at path hold.
func Status(path string) (State, error) {
	s, err := load(path)
	if err != nil {
		return State{}, err
	}
	var st State
	seen := map[string]bool{}
	for _, ev := range Events() {
		found := false
		for _, raw := range s.groups[ev] {
			g, ok := parseGroup(raw)
			if !ok {
				continue
			}
			for _, h := range g.handlers {
				if c, ok := handlerCommand(h); ok {
					found = true
					if !seen[c] {
						seen[c] = true
						st.Commands = append(st.Commands, c)
					}
				}
			}
		}
		if found {
			st.Installed = append(st.Installed, ev)
		} else {
			st.Missing = append(st.Missing, ev)
		}
	}
	return st, nil
}

// marshal is json.Marshal without HTML escaping, so the owner's shell
// commands (&&, >) stay readable in their settings.
func marshal(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b.Bytes()), nil
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}
