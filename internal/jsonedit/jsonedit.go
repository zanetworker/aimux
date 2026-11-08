//go:build unix

// Package jsonedit edits one top-level key of a JSON config file that another
// program (Claude Code) also rewrites: atomically, verified by reading back,
// every other key byte-identical, and backs such files up with rotation. It
// imports only the standard library. Unix only (flock, stat owner and link
// count); aimux ships for darwin and linux.
package jsonedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var errNotJSON = errors.New("not valid JSON; nothing was changed")

// ErrConcurrentWrite means another writer kept changing the file while EditKey
// worked on it. The file holds that writer's last version: either none of our
// writes landed, or the last one was replaced.
var ErrConcurrentWrite = errors.New("config kept changing while editing; it holds the other writer's last version, without this edit")

// maxEditAttempts bounds how often EditKey starts over from a fresh read.
const maxEditAttempts = 5

// tornReadWait is the pause after a read that did not parse, most likely a
// file caught halfway through another writer's non-atomic write.
const tornReadWait = 50 * time.Millisecond

// lockfileStale is proper-lockfile's default: a <file>.lock directory older
// than this belongs to a process that died and may be taken.
const lockfileStale = 10 * time.Second

// Test hooks: called with the edited file's resolved path after the temp file is
// written but before the pre-rename check, and right after the rename.
var editHookBeforeRename, editHookAfterRename func(target string)

// editReadFile reads the edited file (a test hook for torn reads).
var editReadFile = os.ReadFile

// statOwner reports a file's uid and gid.
func statOwner(fi os.FileInfo) (int, int) {
	st := fi.Sys().(*syscall.Stat_t)
	return int(st.Uid), int(st.Gid)
}

// fileOwner reports the edited file's owner (a test hook; the temp file's
// owner always comes from statOwner).
var fileOwner = statOwner

// lockTimeout bounds the wait for each lock EditKey takes.
var lockTimeout = 10 * time.Second

// EditKey rewrites one top-level key of the JSON object at path with fn(old) (old is nil when absent),
// keeping every other key byte-identical, writing atomically (temp in same dir, original mode, rename),
// re-reading to verify, and retrying from a fresh read up to 5 times if the file changed in between.
//
// fn returning nil removes the key. fn may run once per attempt, each time on
// the value just read, and only the last call's result is written: it must be
// a pure function of old (it may record what it decided, last call wins), must
// do no I/O and must not call EditKey (the directory lock is not reentrant, so
// a nested call waits out the lock timeout and fails).
//
// The new value is reformatted to the file's top-level style (indented like
// the first entry, or compact). Everything outside the key's value is copied
// from the original bytes, so the other keys, their order and the whitespace
// between them are unchanged. Nothing is written when the value does not
// change. If path is a symlink its target is edited.
//
// Concurrency: aimux editors serialize on an advisory flock of the file's
// directory (waiting up to 10 s). Around the write window (the pre-rename check
// through the rename) EditKey also holds <file>.lock, a directory in
// proper-lockfile's format, in case the other writer (Claude Code bundles
// proper-lockfile) honours it; harmless if it does not. A change made by a
// writer that ignores both, between the read and the rename, is detected by
// re-reading just before the rename and starts the edit over. One window
// remains: a write landing between that re-read and the rename (one syscall)
// is replaced by ours. After the rename the file is read back; if our value is
// not in it the edit starts over. A read that does not parse (a torn read of a
// file being rewritten in place) waits 50 ms and starts over; the file is
// reported invalid only when two reads in a row return the same invalid bytes.
func EditKey(path, key string, fn func(old json.RawMessage) (json.RawMessage, error)) error {
	return editKey(path, key, fn, true)
}

// EditKeyVerbatim is like EditKey but does not reformat fn's returned value.
// Use when fn has already formatted the value to preserve internal structure
// (e.g., SetMember preserving sibling members' formatting). The value must
// still be valid JSON.
func EditKeyVerbatim(path, key string, fn func(old json.RawMessage) (json.RawMessage, error)) error {
	return editKey(path, key, fn, false)
}

func editKey(path, key string, fn func(old json.RawMessage) (json.RawMessage, error), reformat bool) error {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("edit %s: %w", path, err)
	}
	unlock, err := lockDir(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("edit %s: %w", path, err)
	}
	defer unlock()
	removeStaleTemps(target)

	var lastBad []byte
	for i := 0; i < maxEditAttempts; i++ {
		a, err := editOnce(target, key, fn, reformat)
		if err != nil {
			return fmt.Errorf("edit %s key %q: %w", path, key, err)
		}
		switch a.result {
		case attemptDone:
			return nil
		case attemptTorn:
			if lastBad != nil && bytes.Equal(a.bad, lastBad) {
				return fmt.Errorf("edit %s key %q: %w", path, key, a.badErr)
			}
			lastBad = a.bad
			time.Sleep(tornReadWait)
		default:
			lastBad = nil
		}
	}
	return fmt.Errorf("edit %s key %q: %w", path, key, ErrConcurrentWrite)
}

type attemptResult int

const (
	attemptDone     attemptResult = iota
	attemptConflict               // another writer changed the file; start over
	attemptTorn                   // a read did not parse; wait, then start over
)

// attempt is the outcome of one editOnce; bad and badErr describe a torn read.
type attempt struct {
	result attemptResult
	bad    []byte
	badErr error
}

var (
	done     = attempt{result: attemptDone}
	conflict = attempt{result: attemptConflict}
)

func torn(b []byte, err error) attempt { return attempt{attemptTorn, b, err} }

// editOnce makes one attempt. Errors are final; a conflict or torn read means
// the caller should start over.
func editOnce(target, key string, fn func(json.RawMessage) (json.RawMessage, error), reformat bool) (attempt, error) {
	base, err := editReadFile(target)
	if err != nil {
		return attempt{}, err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return attempt{}, err
	}
	if n := fi.Sys().(*syscall.Stat_t).Nlink; n > 1 {
		return attempt{}, fmt.Errorf("it has %d hard links and replacing it would split them; refusing to write", n)
	}
	doc, err := parseTopObject(base)
	if err != nil {
		return torn(base, err), nil
	}
	var old json.RawMessage
	if e := doc.find(key); e != nil {
		old = json.RawMessage(base[e.valStart:e.valEnd])
	}
	val, err := fn(old)
	if err != nil {
		return attempt{}, err
	}
	if val != nil {
		if reformat {
			if val, err = doc.format(val); err != nil {
				return attempt{}, fmt.Errorf("new value is not valid JSON: %w", err)
			}
		} else if !json.Valid(val) {
			return attempt{}, errors.New("new value is not valid JSON")
		}
	}
	if (val == nil && old == nil) || (val != nil && old != nil && jsonSame(val, old)) {
		return done, nil
	}
	out, err := doc.with(key, val)
	if err != nil {
		return attempt{}, err
	}
	if err := checkEdit(base, out, key, val); err != nil {
		return attempt{}, fmt.Errorf("internal splice check failed, nothing written: %w", err)
	}

	tmp, err := writeTemp(target, out, fi)
	if err != nil {
		return attempt{}, err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmp) // best effort; the temp file is ours alone
		}
	}()
	release, err := takeLockfile(target)
	if err != nil {
		return attempt{}, err
	}
	if editHookBeforeRename != nil {
		editHookBeforeRename(target)
	}
	now, err := editReadFile(target)
	if err != nil {
		release()
		return attempt{}, err
	}
	if !bytes.Equal(now, base) {
		release()
		if !json.Valid(now) {
			return torn(now, errNotJSON), nil
		}
		return conflict, nil
	}
	err = os.Rename(tmp, target)
	release()
	if err != nil {
		return attempt{}, err
	}
	renamed = true
	syncDir(filepath.Dir(target))
	if editHookAfterRename != nil {
		editHookAfterRename(target)
	}

	after, err := editReadFile(target)
	if err != nil {
		return attempt{}, err
	}
	if bytes.Equal(after, out) {
		return done, nil
	}
	// Someone wrote after our rename. If our value survived, they built on our
	// file and the edit stands; otherwise they replaced it and we start over.
	got, err := parseTopObject(after)
	if err != nil {
		return torn(after, err), nil
	}
	e := got.find(key)
	switch {
	case val == nil && e == nil:
		return done, nil
	case val != nil && e != nil && jsonSame(after[e.valStart:e.valEnd], val):
		return done, nil
	default:
		return conflict, nil
	}
}

// checkEdit re-parses the spliced output and confirms it differs from base only
// in key: every other key present with the same raw bytes in the same order.
func checkEdit(base, out []byte, key string, val json.RawMessage) error {
	b, err := parseTopObject(base)
	if err != nil {
		return err
	}
	o, err := parseTopObject(out)
	if err != nil {
		return err
	}
	var bi, oi int
	for bi < len(b.entries) || oi < len(o.entries) {
		if bi < len(b.entries) && b.entries[bi].key == key {
			bi++
			continue
		}
		if oi < len(o.entries) && o.entries[oi].key == key {
			if val == nil || !bytes.Equal(out[o.entries[oi].valStart:o.entries[oi].valEnd], val) {
				return fmt.Errorf("key %q has the wrong value", key)
			}
			oi++
			continue
		}
		if bi >= len(b.entries) || oi >= len(o.entries) {
			return errors.New("key count differs")
		}
		be, oe := b.entries[bi], o.entries[oi]
		if be.key != oe.key || !bytes.Equal(base[be.keyStart:be.valEnd], out[oe.keyStart:oe.valEnd]) {
			return fmt.Errorf("key %q changed", be.key)
		}
		bi, oi = bi+1, oi+1
	}
	if val != nil && o.find(key) == nil {
		return fmt.Errorf("key %q missing", key)
	}
	return nil
}

// lockDir takes an exclusive advisory lock on dir so aimux processes editing
// files in it take turns, waiting up to lockTimeout. It creates no file; the
// kernel drops the lock if the process dies.
func lockDir(dir string) (func(), error) {
	f, err := os.Open(dir) //nolint:gosec // the edited file's directory
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) // #nosec G115 -- file descriptors are small
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, fmt.Errorf("another aimux is editing files in %s; gave up after %v", dir, lockTimeout)
			}
			return nil, fmt.Errorf("lock %s: %w", dir, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // #nosec G115 -- file descriptors are small
		_ = f.Close()
	}, nil
}

// takeLockfile creates target+".lock" as proper-lockfile does (mkdir), waiting
// up to lockTimeout while another process holds a fresh one and taking one
// older than lockfileStale. Two takers of the same stale lock can race as they
// can in proper-lockfile itself; the window is one stat and one rmdir.
func takeLockfile(target string) (func(), error) {
	lock := target + ".lock"
	deadline := time.Now().Add(lockTimeout)
	for {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			return func() { _ = os.Remove(lock) }, nil // best effort; a leftover goes stale in 10 s
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock %s: %w", lock, err)
		}
		fi, err := os.Stat(lock)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue // released between our mkdir and stat
		case err == nil && time.Since(fi.ModTime()) > lockfileStale && os.Remove(lock) == nil:
			continue // stale: its holder died
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is held by another process; gave up after %v", lock, lockTimeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// tempPrefix starts the name of every temp file EditKey writes beside target.
func tempPrefix(target string) string { return "." + filepath.Base(target) + ".aimux-" }

// staleTempAge is when a leftover temp file counts as a crash's debris rather
// than a live edit's (edits hold their temp file for milliseconds).
const staleTempAge = time.Minute

// removeStaleTemps deletes temp files a crashed EditKey left beside target.
// Called under the directory lock; failures are ignored (debris only).
func removeStaleTemps(target string) {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(target), escapeGlob(tempPrefix(target))+"*"))
	for _, m := range matches {
		if fi, err := os.Lstat(m); err == nil && fi.Mode().IsRegular() && time.Since(fi.ModTime()) > staleTempAge {
			_ = os.Remove(m)
		}
	}
}

func escapeGlob(s string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`).Replace(s)
}

// writeTemp writes data to a new temp file beside target with the original's
// mode and owner, and syncs it. If the owner cannot be kept (aimux running as
// another user, e.g. under sudo) it refuses rather than hand the file over.
func writeTemp(target string, data []byte, orig os.FileInfo) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), tempPrefix(target)+"*")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(f.Name()) // best effort; nothing was renamed
		return "", err
	}
	tfi, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	uid, gid := fileOwner(orig)
	if tuid, tgid := statOwner(tfi); tuid != uid || tgid != gid {
		if err := f.Chown(uid, gid); err != nil {
			return fail(fmt.Errorf("it is owned by uid %d gid %d and the new file cannot be given that owner (%v); refusing to write", uid, gid, err))
		}
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr, os.Chmod(f.Name(), orig.Mode().Perm())); err != nil {
		_ = os.Remove(f.Name()) // best effort; the write already failed
		return "", err
	}
	return f.Name(), nil
}

// syncDir flushes a rename to disk. Failure only weakens durability across a
// power loss, not correctness, so it is ignored.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil { //nolint:gosec // the edited file's directory
		_ = d.Sync()
		_ = d.Close()
	}
}

// jsonSame reports whether a and b hold the same JSON value, ignoring
// whitespace. Numbers compare by their text, so a change float64 would round
// away still counts (and 1e5 vs 100000 counts as a change, which is harmless).
func jsonSame(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	x, xerr := decodeNumbers(a)
	y, yerr := decodeNumbers(b)
	return xerr == nil && yerr == nil && reflect.DeepEqual(x, y)
}

func decodeNumbers(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// topEntry locates one member of the top-level object in the source bytes.
type topEntry struct {
	key                                string
	keyStart, keyEnd, valStart, valEnd int
}

// topObject is the top level of a JSON object with byte offsets into src.
type topObject struct {
	src         []byte
	open, close int // offsets of '{' and '}'
	entries     []topEntry
}

// parseTopObject finds the top-level members of a JSON object without
// re-encoding anything. It refuses invalid JSON, a non-object, text after the
// object and duplicate top-level keys (which value wins would be ambiguous).
func parseTopObject(src []byte) (*topObject, error) {
	if !json.Valid(src) {
		return nil, errNotJSON
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("top level is not a JSON object; nothing was changed")
	}
	doc := &topObject{src: src, open: int(dec.InputOffset()) - 1}
	seen := map[string]bool{}
	for dec.More() {
		prevEnd := int(dec.InputOffset())
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := tok.(string)
		keyEnd := int(dec.InputOffset())
		q := bytes.IndexByte(src[prevEnd:keyEnd], '"')
		if q < 0 {
			return nil, errors.New("cannot locate a top-level key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		valEnd := int(dec.InputOffset())
		valStart := valEnd - len(raw)
		if valStart < keyEnd || !bytes.Equal(src[valStart:valEnd], raw) {
			return nil, fmt.Errorf("cannot locate the value of top-level key %q", k)
		}
		if seen[k] {
			return nil, fmt.Errorf("duplicate top-level key %q; nothing was changed", k)
		}
		seen[k] = true
		doc.entries = append(doc.entries, topEntry{k, prevEnd + q, keyEnd, valStart, valEnd})
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, err
	}
	doc.close = int(dec.InputOffset()) - 1
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("text after the JSON object; nothing was changed")
	}
	return doc, nil
}

func (d *topObject) find(key string) *topEntry {
	for i := range d.entries {
		if d.entries[i].key == key {
			return &d.entries[i]
		}
	}
	return nil
}

// style returns the whitespace before the first key (e.g. "\n  "), the text
// between a key and its value (e.g. ": ") and the indent unit ("" when compact).
func (d *topObject) style() (lead, colon, indent string) {
	if len(d.entries) == 0 {
		return "\n  ", ": ", "  "
	}
	first := d.entries[0]
	lead = string(d.src[d.open+1 : first.keyStart])
	colon = string(d.src[first.keyEnd:first.valStart])
	if i := strings.LastIndexByte(lead, '\n'); i >= 0 {
		indent = lead[i+1:]
	}
	return lead, colon, indent
}

// format rewrites v in the file's style: indented one level deep, or compact.
func (d *topObject) format(v json.RawMessage) (json.RawMessage, error) {
	var b bytes.Buffer
	_, _, indent := d.style()
	var err error
	if indent == "" {
		err = json.Compact(&b, v)
	} else {
		err = json.Indent(&b, bytes.TrimSpace(v), indent, indent)
	}
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// formatMemberValue reformats v to match the object's indentation style and position.
// For inline objects (indent=""), it compacts v. For indented objects, it formats v
// with proper indentation for a member value, determining the indentation unit from
// the object's structure.
func (d *topObject) formatMemberValue(v json.RawMessage) (json.RawMessage, error) {
	lead, colon, _ := d.style()

	// Extract base indent and unit from lead
	baseIndent := ""
	indentUnit := "  " // default 2 spaces
	if i := strings.LastIndexByte(lead, '\n'); i >= 0 {
		baseIndent = lead[i+1:]
		// Indentation unit is typically half the base (2 spaces if base is 4)
		// or the same if base is already 2
		if len(baseIndent) >= 4 {
			indentUnit = baseIndent[:len(baseIndent)/2]
		} else if len(baseIndent) > 0 {
			indentUnit = baseIndent
		}
	}

	if baseIndent == "" {
		// Inline/compact style - detect if it uses spaces
		var b bytes.Buffer
		if err := json.Compact(&b, v); err != nil {
			return nil, err
		}
		compact := b.Bytes()

		// If colon has a space after it (": "), add spaces after commas too
		if strings.HasSuffix(colon, " ") {
			compact = spaceStructural(compact)
		}
		return compact, nil
	}

	// Format with no prefix, using the indentation unit
	var b bytes.Buffer
	if err := json.Indent(&b, bytes.TrimSpace(v), "", indentUnit); err != nil {
		return nil, err
	}
	formatted := b.Bytes()

	// Add base indentation to all lines except the first
	lines := bytes.Split(formatted, []byte("\n"))
	if len(lines) <= 1 {
		return formatted, nil
	}

	for i := 1; i < len(lines); i++ {
		if len(lines[i]) > 0 {
			lines[i] = append([]byte(baseIndent), lines[i]...)
		}
	}
	return bytes.Join(lines, []byte("\n")), nil
}

// with returns the source with key set to val (removed when val is nil).
func (d *topObject) with(key string, val json.RawMessage) ([]byte, error) {
	src := d.src
	splice := func(from, to int, mid []byte) []byte {
		out := make([]byte, 0, len(src)-(to-from)+len(mid))
		out = append(out, src[:from]...)
		out = append(out, mid...)
		return append(out, src[to:]...)
	}
	for i, e := range d.entries {
		if e.key != key {
			continue
		}
		switch {
		case val != nil:
			return splice(e.valStart, e.valEnd, val), nil
		case len(d.entries) == 1:
			return splice(d.open+1, d.close, nil), nil
		case i < len(d.entries)-1:
			return splice(e.keyStart, d.entries[i+1].keyStart, nil), nil
		default:
			return splice(d.entries[i-1].valEnd, e.valEnd, nil), nil
		}
	}
	if val == nil {
		return src, nil
	}
	k, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	lead, colon, indent := d.style()
	member := append(append(k, colon...), val...)
	if len(d.entries) == 0 {
		mid := lead + string(member) + "\n"
		if indent == "" {
			mid = string(member)
		}
		return splice(d.open+1, d.close, []byte(mid)), nil
	}
	last := d.entries[len(d.entries)-1]
	return splice(last.valEnd, last.valEnd, []byte(","+lead+string(member))), nil
}

// SetMember returns obj with one member set to value (nil value removes the member).
// Every other member's bytes, order and whitespace are preserved. The new value
// is formatted to match obj's style (indented if obj is multi-line, compact if inline).
// obj must be a JSON object; arrays and other types are refused. Duplicate members
// are refused.
func SetMember(obj json.RawMessage, key string, value json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(obj) {
		return nil, errors.New("not valid JSON")
	}
	doc, err := parseTopObject(obj)
	if err != nil {
		return nil, err
	}
	if value != nil {
		if value, err = doc.formatMemberValue(value); err != nil {
			return nil, fmt.Errorf("value is not valid JSON: %w", err)
		}
	}
	return doc.with(key, value)
}

// ReplaceMemberVerbatim returns obj with the existing member key's value
// replaced by value's exact bytes (not reformatted), so a value saved
// earlier can be put back byte for byte. An absent member is an error.
func ReplaceMemberVerbatim(obj json.RawMessage, key string, value json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(obj) {
		return nil, errors.New("not valid JSON")
	}
	if !json.Valid(value) {
		return nil, errors.New("value is not valid JSON")
	}
	doc, err := parseTopObject(obj)
	if err != nil {
		return nil, err
	}
	for _, e := range doc.entries {
		if e.key == key {
			return doc.with(key, value)
		}
	}
	return nil, fmt.Errorf("no member %q", key)
}

// backupStamp is the UTC timestamp in backup names.
const backupStamp = "20060102T150405Z"

// backupNow is the clock for backup names (a test hook).
var backupNow = time.Now

// Backup copies path to dir/<base>.<UTC 20060102T150405Z>, 0600, keeping the newest keep copies; returns the copy's path.
//
// The stamp is the later of now and the newest existing backup's, so a clock
// that went back cannot make the new copy sort oldest. A second backup with the same stamp gets "-1", "-2", ... appended (one
// past the highest already there, so a name freed by rotation is never reused
// and order stays newest last). dir is created 0700 if missing. Rotation only
// removes files named like this file's backups; anything else in dir stays.
func Backup(path, dir string, keep int) (string, error) {
	if keep < 1 {
		return "", fmt.Errorf("back up %s: keep must be at least 1, got %d", path, keep)
	}
	data, err := os.ReadFile(path) //nolint:gosec // a config path the caller chose
	if err != nil {
		return "", fmt.Errorf("back up %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("back up %s: %w", path, err)
	}
	base := filepath.Base(path)
	stamp := backupNow().UTC().Format(backupStamp)
	existing, err := listBackups(dir, base)
	if err != nil {
		return "", fmt.Errorf("back up %s: %w", path, err)
	}
	if n := len(existing); n > 0 && existing[n-1].stamp > stamp {
		stamp = existing[n-1].stamp // the clock went back: still sort newest
	}
	seq := 0
	for _, b := range existing {
		if b.stamp == stamp && b.seq >= seq {
			seq = b.seq + 1
		}
	}
	var f *os.File
	for ; ; seq++ { // O_EXCL: a concurrent backup taking the same name moves us on
		f, err = os.OpenFile(filepath.Join(dir, backupName(base, stamp, seq)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the backup dir
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("back up %s: %w", path, err)
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr, os.Chmod(f.Name(), 0o600)); err != nil {
		_ = os.Remove(f.Name()) // a partial backup is worse than none
		return "", fmt.Errorf("back up %s: %w", path, err)
	}
	if err := pruneBackups(dir, base, keep); err != nil {
		return f.Name(), fmt.Errorf("back up %s: rotate: %w", path, err)
	}
	return f.Name(), nil
}

type backupFile struct {
	name  string
	stamp string
	seq   int
}

func backupName(base, stamp string, seq int) string {
	if seq == 0 {
		return base + "." + stamp
	}
	return base + "." + stamp + "-" + strconv.Itoa(seq)
}

// listBackups returns base's backups in dir, oldest first.
func listBackups(dir, base string) ([]backupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var list []backupFile
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), base+".")
		if !ok || e.IsDir() || len(rest) < len(backupStamp) {
			continue
		}
		stamp, suffix := rest[:len(backupStamp)], rest[len(backupStamp):]
		if _, err := time.Parse(backupStamp, stamp); err != nil {
			continue
		}
		seq := 0
		if suffix != "" {
			n, ok := strings.CutPrefix(suffix, "-")
			if seq, err = strconv.Atoi(n); !ok || err != nil || seq < 1 || backupName(base, stamp, seq) != e.Name() {
				continue
			}
		}
		list = append(list, backupFile{e.Name(), stamp, seq})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].stamp != list[j].stamp {
			return list[i].stamp < list[j].stamp
		}
		return list[i].seq < list[j].seq
	})
	return list, nil
}

// pruneBackups removes all but the newest keep backups of base in dir.
func pruneBackups(dir, base string, keep int) error {
	list, err := listBackups(dir, base)
	if err != nil {
		return err
	}
	var errs []error
	for i := 0; i < len(list)-keep; i++ {
		errs = append(errs, os.Remove(filepath.Join(dir, list[i].name)))
	}
	return errors.Join(errs...)
}

// spaceStructural adds a space after each comma and colon of compact JSON
// that is structure, not string content: a permission rule such as
// "Bash(echo a,b)" keeps its exact bytes.
func spaceStructural(compact []byte) []byte {
	out := make([]byte, 0, len(compact)+len(compact)/8)
	inString, escaped := false, false
	for _, c := range compact {
		out = append(out, c)
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && (c == ',' || c == ':'):
			out = append(out, ' ')
		}
	}
	return out
}
