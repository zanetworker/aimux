package mcpproxy

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// Install writes the plist atomically (0644), bootouts (ignoring "not loaded"), then bootstraps
func (a Agent) Install() error {
	// Generate plist content
	plistContent := a.Plist()

	// Write plist atomically
	dir := filepath.Dir(a.PlistPath)
	tmpFile, err := os.CreateTemp(dir, ".plist-tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
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
		return fmt.Errorf("failed to write plist: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Set permissions to 0644 (launchd plist files must be readable by system)
	//nolint:gosec // G302: plist files need 0644 to be readable by launchd
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	// Rename atomically
	if err := os.Rename(tmpPath, a.PlistPath); err != nil {
		return fmt.Errorf("failed to write plist atomically: %w", err)
	}

	// Successfully written, don't delete in defer
	tmpFile = nil

	// Bootout existing service (ignore "not loaded" errors)
	domain := fmt.Sprintf("gui/%d/%s", a.UID, a.Label)
	out, err := a.Run("launchctl", "bootout", domain)
	if err != nil {
		// Ignore "service not loaded" or "could not find service" errors
		if !isServiceNotFoundError(out, err) {
			return fmt.Errorf("failed to bootout: %w", err)
		}
		// Service not loaded is expected on first install, continue
	}

	// Bootstrap the service
	bootstrapDomain := fmt.Sprintf("gui/%d", a.UID)
	_, err = a.Run("launchctl", "bootstrap", bootstrapDomain, a.PlistPath)
	if err != nil {
		return fmt.Errorf("failed to bootstrap: %w", err)
	}

	return nil
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
