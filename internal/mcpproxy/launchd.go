package mcpproxy

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Agent manages a launchd agent for the MCP proxy daemon.
type Agent struct {
	Label     string                                            // launchd label (e.g., "com.aimux.mcp-proxy")
	PlistPath string                                            // absolute path to plist file
	Binary    string                                            // absolute path to binary
	LogDir    string                                            // directory for stdout/stderr logs
	Args      []string                                          // arguments to binary (e.g., ["mcp","proxy","serve","--port","7381"])
	Run       func(name string, args ...string) ([]byte, error) // launchctl runner (injected)
	// Sleep waits between launchctl checks; default time.Sleep (tests pass a no-op).
	Sleep func(time.Duration)
	UID   int // user ID for gui/<uid>
	// Env is the agent's EnvironmentVariables. The plist is world-readable:
	// only non-secret values such as PATH and HOME belong here.
	Env map[string]string
}

// plistDict represents a macOS plist dictionary structure
type plistDict struct {
	XMLName xml.Name     `xml:"plist"`
	Version string       `xml:"version,attr"`
	Dict    dictContents `xml:"dict"`
}

type dictContents struct {
	Entries []interface{}
}

type plistKey struct {
	XMLName xml.Name `xml:"key"`
	Value   string   `xml:",chardata"`
}

type plistString struct {
	XMLName xml.Name `xml:"string"`
	Value   string   `xml:",chardata"`
}

type plistTrue struct {
	XMLName xml.Name `xml:"true"`
}

// plistEnvDict is a nested <dict> of string values, keys sorted.
type plistEnvDict map[string]string

// MarshalXML writes the map as alternating <key> and <string> elements.
func (d plistEnvDict) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	start := xml.StartElement{Name: xml.Name{Local: "dict"}}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := e.Encode(plistKey{Value: k}); err != nil {
			return err
		}
		if err := e.Encode(plistString{Value: d[k]}); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}

type plistArray struct {
	XMLName xml.Name      `xml:"array"`
	Strings []plistString `xml:"string"`
}

// MarshalXML implements custom XML marshaling for dictContents
func (d dictContents) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	for _, entry := range d.Entries {
		if err := e.Encode(entry); err != nil {
			return err
		}
	}
	return e.EncodeToken(xml.EndElement{Name: start.Name})
}

// Plist generates the launchd plist XML with RunAtLoad, KeepAlive, stdout/stderr paths, and ProgramArguments
func (a Agent) Plist() []byte {
	// Build ProgramArguments array: binary followed by args
	progArgs := plistArray{}
	progArgs.Strings = append(progArgs.Strings, plistString{Value: a.Binary})
	for _, arg := range a.Args {
		progArgs.Strings = append(progArgs.Strings, plistString{Value: arg})
	}

	// Build dict entries in order
	entries := []interface{}{
		plistKey{Value: "Label"},
		plistString{Value: a.Label},
		plistKey{Value: "ProgramArguments"},
		progArgs,
		plistKey{Value: "RunAtLoad"},
		plistTrue{},
		plistKey{Value: "KeepAlive"},
		plistTrue{},
		plistKey{Value: "StandardOutPath"},
		plistString{Value: filepath.Join(a.LogDir, "stdout.log")},
		plistKey{Value: "StandardErrorPath"},
		plistString{Value: filepath.Join(a.LogDir, "stderr.log")},
	}
	if len(a.Env) > 0 {
		entries = append(entries, plistKey{Value: "EnvironmentVariables"}, plistEnvDict(a.Env))
	}

	plist := plistDict{
		Version: "1.0",
		Dict:    dictContents{Entries: entries},
	}

	// Marshal to XML
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")

	encoder := xml.NewEncoder(&buf)
	encoder.Indent("", "  ")
	if err := encoder.Encode(plist); err != nil {
		// Should not happen with simple types
		panic(fmt.Sprintf("failed to encode plist: %v", err))
	}

	// encoding/xml writes empty elements as <true></true>; launchd's plist
	// parser rejects that form ("Bootstrap failed: 5: Input/output error")
	// although plutil -lint accepts it, so write the self-closing form.
	return bytes.ReplaceAll(buf.Bytes(), []byte("<true></true>"), []byte("<true/>"))
}

// errPlistNotWritten is an Install that failed before replacing the plist:
// neither the file nor the job changed.
var errPlistNotWritten = errors.New("the launchd plist was not written")

// Install writes the plist atomically (0644), then restarts the job from it
// (Restart: bootout, ignoring "not loaded", then bootstrap).
func (a Agent) Install() error {
	// Generate plist content
	plistContent := a.Plist()

	// Write plist atomically
	dir := filepath.Dir(a.PlistPath)
	tmpFile, err := os.CreateTemp(dir, ".plist-tmp-*")
	if err != nil {
		return fmt.Errorf("%w: failed to create temp file: %w", errPlistNotWritten, err)
	}
	tmpPath := tmpFile.Name()

	// Clean up temp file on error
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(plistContent); err != nil {
		return fmt.Errorf("%w: failed to write plist: %w", errPlistNotWritten, err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("%w: failed to close temp file: %w", errPlistNotWritten, err)
	}

	// Set permissions to 0644 (launchd plist files must be readable by system)
	//nolint:gosec // G302: plist files need 0644 to be readable by launchd
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return fmt.Errorf("%w: failed to set permissions: %w", errPlistNotWritten, err)
	}

	// Rename atomically
	if err := os.Rename(tmpPath, a.PlistPath); err != nil {
		return fmt.Errorf("%w: failed to write plist atomically: %w", errPlistNotWritten, err)
	}

	// Successfully written, don't delete in defer
	tmpFile = nil

	// Reload it from the new plist; Restart waits for launchd to unload a
	// running job, so a bootstrap never lands in the unload window.
	return a.Restart()
}

// Restart reloads the service from the plist on disk as it is (bootout,
// ignoring "not loaded", then bootstrap); it does not rewrite the plist.
func (a Agent) Restart() error {
	out, err := a.Run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", a.UID, a.Label))
	if err != nil && !isServiceNotFoundError(out, err) {
		return fmt.Errorf("failed to bootout: %w", err)
	}
	// bootout returns before launchd has unloaded the job; a bootstrap in
	// that window fails with exit status 5 and leaves the daemon down.
	for i := 0; i < unloadChecks; i++ {
		if loaded, err := a.Loaded(); err == nil && !loaded {
			break
		}
		a.sleep(launchdPoll)
	}
	return a.bootstrap()
}

// Kickstart restarts the daemon in place (launchctl kickstart -k): the job
// stays loaded, so no failed bootstrap can leave it down. An agent that is
// not loaded is bootstrapped. The plist is not re-read; use Restart for that.
func (a Agent) Kickstart() error {
	loaded, err := a.Loaded()
	if err != nil {
		return err
	}
	if !loaded {
		return a.bootstrap()
	}
	if out, err := a.Run("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", a.UID, a.Label)); err != nil {
		return fmt.Errorf("failed to kickstart: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// bootstrap loads the agent, retrying while launchd still finishes an
// unload (exit status 5).
func (a Agent) bootstrap() error {
	var err error
	for i := 0; i < bootstrapTries; i++ {
		var out []byte
		if out, err = a.Run("launchctl", "bootstrap", fmt.Sprintf("gui/%d", a.UID), a.PlistPath); err == nil {
			return nil
		}
		if text := string(out) + " " + err.Error(); !strings.Contains(text, "exit status 5") && !strings.Contains(text, "Input/output error") {
			break // not the unload race: retrying will not help
		}
		a.sleep(launchdPoll)
	}
	return fmt.Errorf("failed to bootstrap: %w", err)
}

func (a Agent) sleep(d time.Duration) {
	if a.Sleep != nil {
		a.Sleep(d)
		return
	}
	time.Sleep(d)
}

const (
	launchdPoll    = 100 * time.Millisecond
	unloadChecks   = 50 // up to 5 s for launchd to unload a job
	bootstrapTries = 20 // up to 2 s more of exit status 5
)

// Uninstall bootouts the service and removes the plist (both idempotent)
func (a Agent) Uninstall() error {
	// Bootout the service (ignore "not loaded" errors)
	domain := fmt.Sprintf("gui/%d/%s", a.UID, a.Label)
	out, err := a.Run("launchctl", "bootout", domain)
	if err != nil {
		// Ignore "service not loaded" or "could not find service" errors
		if !isServiceNotFoundError(out, err) {
			return fmt.Errorf("failed to bootout: %w", err)
		}
		// Service not loaded is fine during uninstall
	}

	// Remove plist file (idempotent - ignore if not exists)
	if err := os.Remove(a.PlistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove plist: %w", err)
	}

	return nil
}

// PID is the pid launchd reports for the job's running process (launchctl
// print's "pid = N"); 0 when it is not loaded or not running.
func (a Agent) PID() (int, error) {
	out, err := a.Run("launchctl", "print", fmt.Sprintf("gui/%d/%s", a.UID, a.Label))
	if err != nil {
		if isServiceNotFoundError(out, err) {
			return 0, nil
		}
		return 0, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "pid = "); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return 0, fmt.Errorf("launchctl print: pid %q: %w", v, err)
			}
			return pid, nil
		}
	}
	return 0, nil
}

// Loaded returns true if the service is currently loaded in launchd
func (a Agent) Loaded() (bool, error) {
	domain := fmt.Sprintf("gui/%d/%s", a.UID, a.Label)
	output, err := a.Run("launchctl", "print", domain)

	if err != nil {
		// Check if this is a "service not found" error
		if isServiceNotFoundError(output, err) {
			return false, nil
		}
		// Some other error
		return false, err
	}

	// If no error, service is loaded (even if not running)
	// launchctl print succeeds for any loaded service
	return len(output) > 0, nil
}

// isServiceNotFoundError reports whether a failed launchctl call only means
// the service is not loaded. launchctl writes the reason to its output; the
// error carries the exit code: bootout exits 3 ("No such process") and print
// exits 113 ("Could not find service") for a service that is not loaded.
func isServiceNotFoundError(output []byte, err error) bool {
	if err == nil {
		return false
	}
	text := string(output) + " " + err.Error()
	for _, s := range []string{"No such process", "Could not find", "could not find", "service not loaded", "exit status 113"} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// plistNode is any plist element, for reading a plist back.
type plistNode struct {
	XMLName xml.Name
	Text    string      `xml:",chardata"`
	Nodes   []plistNode `xml:",any"`
}

// ReadPlistEnv reads the EnvironmentVariables of the launchd plist at
// path: the environment the agent's daemon runs with. found is false when
// there is no plist (no error); a plist without the key has an empty map.
func ReadPlistEnv(path string) (env map[string]string, found bool, err error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the agent's own plist
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var root plistNode
	if err := xml.Unmarshal(b, &root); err != nil {
		return nil, false, fmt.Errorf("read the launchd plist %s: %w", path, err)
	}
	env = map[string]string{}
	if len(root.Nodes) == 0 {
		return env, true, nil
	}
	top := root.Nodes[0].Nodes
	for i := 0; i+1 < len(top); i++ {
		if top[i].XMLName.Local != "key" || top[i].Text != "EnvironmentVariables" || top[i+1].XMLName.Local != "dict" {
			continue
		}
		vars := top[i+1].Nodes
		for j := 0; j+1 < len(vars); j += 2 {
			if vars[j].XMLName.Local == "key" && vars[j+1].XMLName.Local == "string" {
				env[vars[j].Text] = vars[j+1].Text
			}
		}
	}
	return env, true, nil
}

// ReadPlistBinary is the program the launchd plist at path runs (the first
// ProgramArguments string), or "" when there is no readable plist.
func ReadPlistBinary(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- the agent's own plist
	if err != nil {
		return ""
	}
	var root plistNode
	if xml.Unmarshal(b, &root) != nil || len(root.Nodes) == 0 {
		return ""
	}
	top := root.Nodes[0].Nodes
	for i := 0; i+1 < len(top); i++ {
		if top[i].XMLName.Local == "key" && top[i].Text == "ProgramArguments" && top[i+1].XMLName.Local == "array" {
			for _, n := range top[i+1].Nodes {
				if n.XMLName.Local == "string" {
					return n.Text
				}
			}
		}
	}
	return ""
}

// SwitchBinary points the installed plist at a.Binary when it runs another
// program: it rewrites only the first ProgramArguments string, then restarts
// the job with bootout and bootstrap so launchd reads it (kickstart -k would
// keep the old program). It returns the program the plist ran before (""
// when it did not read) and switched, true when it rewrote the plist;
// nothing is done when there is no plist, it already runs a.Binary, or
// a.Binary is empty.
func (a Agent) SwitchBinary() (was string, switched bool, err error) {
	if a.Binary == "" {
		return "", false, nil
	}
	b, err := os.ReadFile(a.PlistPath) // #nosec G304 -- the agent's own plist
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	old := ReadPlistBinary(a.PlistPath)
	if old == a.Binary {
		return "", false, nil
	}
	nb, err := replacePlistProgram(b, a.Binary)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", a.PlistPath, err)
	}
	if err := writePlist(a.PlistPath, nb); err != nil {
		return "", false, err
	}
	if err := a.Restart(); err != nil {
		// Put the old program back and load it again, so the old daemon
		// serves and the next install sees the mismatch and retries.
		if rerr := writePlist(a.PlistPath, b); rerr != nil {
			return "", false, fmt.Errorf("%w; putting the plist back also failed: %v", err, rerr)
		}
		if rerr := a.Restart(); rerr != nil {
			return "", false, fmt.Errorf("%w; loading the old plist (%s) again also failed: %v", err, old, rerr)
		}
		return "", false, fmt.Errorf("%w; the agent runs %s again", err, old)
	}
	return old, true, nil
}

// Readiness is what a check of a restarted daemon found.
type Readiness int

const (
	// NotReady: no daemon of the job answered in the time given.
	NotReady Readiness = iota
	// Ready: the job's own daemon answered.
	Ready
	// ReadyUnknown: it could not be told (no time left to ask, the wait was
	// interrupted, or a daemon answered that cannot be identified).
	ReadyUnknown
)

// InstallChecked is Install followed by ready, which reports whether a
// daemon started after since answers. When the install fails after the
// plist was written (a bootstrap launchd refuses) or ready says no, it is
// undone: a plist that was there before is written back and loaded again,
// and ready is asked again so the error says whether the old daemon runs
// (or that it was not verified);
// a first install is removed. It returns the program the plist ran before
// ("" for a first install).
func (a Agent) InstallChecked(ready func(since time.Time) Readiness) (was string, err error) {
	old, readErr := os.ReadFile(a.PlistPath) // #nosec G304 -- the agent's own plist
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("read the installed plist %s: %w", a.PlistPath, readErr)
	}
	had := readErr == nil
	was = ReadPlistBinary(a.PlistPath)
	since := time.Now().Add(-time.Second) // slack for a daemon started as Install returned
	cause := a.Install()
	if cause == nil {
		if ready(since) == Ready {
			return was, nil
		}
		cause = fmt.Errorf("the daemon did not answer after the restart with %s (logs in %s)", a.Binary, a.LogDir)
	} else if errors.Is(cause, errPlistNotWritten) {
		return was, cause // the plist was never replaced: launchd was not touched
	}
	if !had { // a first install: take the new job away again
		// The plist goes even when launchd cannot unload the job, so it
		// does not load at a later login; each failure is reported.
		out, berr := a.Run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", a.UID, a.Label))
		if berr != nil && isServiceNotFoundError(out, berr) {
			berr = nil
		}
		rerr := os.Remove(a.PlistPath)
		if errors.Is(rerr, os.ErrNotExist) {
			rerr = nil
		}
		switch {
		case rerr != nil:
			return was, fmt.Errorf("%w; removing the new plist %s also failed: %v", cause, a.PlistPath, rerr)
		case berr != nil:
			return was, fmt.Errorf("%w; the new plist was removed, but unloading the job failed: %v", cause, berr)
		}
		return was, fmt.Errorf("%w; the new launchd job was removed", cause)
	}
	if err := writePlist(a.PlistPath, old); err != nil {
		return was, fmt.Errorf("%w; putting the old plist back also failed: %v", cause, err)
	}
	restarted := time.Now().Add(-time.Second)
	if err := a.Restart(); err != nil {
		return was, fmt.Errorf("%w; loading the old plist (%s) again also failed: %v", cause, was, err)
	}
	switch ready(restarted) {
	case NotReady:
		return was, fmt.Errorf("%w; the previous plist (%s) is back, but its daemon does not answer either", cause, was)
	case ReadyUnknown:
		return was, fmt.Errorf("%w; the previous plist (%s) is back and loaded; that its daemon answers was not verified", cause, was)
	}
	return was, fmt.Errorf("%w; it runs %s again", cause, was)
}

// replacePlistProgram swaps the first string of ProgramArguments in plist
// for program, leaving every other byte as it was.
func replacePlistProgram(plist []byte, program string) ([]byte, error) {
	const key = "<key>ProgramArguments</key>"
	k := bytes.Index(plist, []byte(key))
	if k < 0 {
		return nil, errors.New("no ProgramArguments in the launchd plist")
	}
	rest := plist[k+len(key):]
	open := bytes.Index(rest, []byte("<string>"))
	if open < 0 {
		return nil, errors.New("ProgramArguments in the launchd plist is empty")
	}
	start := k + len(key) + open + len("<string>")
	end := bytes.Index(plist[start:], []byte("</string>"))
	if end < 0 {
		return nil, errors.New("ProgramArguments in the launchd plist is not closed")
	}
	var esc bytes.Buffer
	if err := xml.EscapeText(&esc, []byte(program)); err != nil {
		return nil, err
	}
	out := append([]byte{}, plist[:start]...)
	out = append(out, esc.Bytes()...)
	return append(out, plist[start+end:]...), nil
}

// writePlist replaces the plist at path atomically, readable by launchd.
func writePlist(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plist-tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	//nolint:gosec // G302: plist files need 0644 to be readable by launchd
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
