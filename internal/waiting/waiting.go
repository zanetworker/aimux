// Package waiting records which Claude Code sessions are waiting for the
// owner's permission. Claude Code's own hooks (aimux remote hooks notify)
// write one marker file per waiting session and remove it once Claude goes
// on; aimux remote serve reads the markers for the session list and pushes
// one notification per new marker. Markers live in ~/.aimux/waiting
// (directory 0700, files 0600) and never hold more than Claude Code's short
// notification line.
package waiting

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// KindPermission: Claude Code shows a permission prompt.
const KindPermission = "permission"

// MaxMessage is the longest message a marker keeps, in runes.
const MaxMessage = 200

// Waiting is what a session waits for. It is the session list's `waiting`.
type Waiting struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

// marker is the file's content. TranscriptPath (Claude Code's
// transcript_path) lets the server see the session move on; AgentID is set
// when a subagent asked.
type marker struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	Waiting
}

// Dir is where the markers live under a home directory.
func Dir(home string) string { return filepath.Join(home, ".aimux", "waiting") }

// Claude Code session ids are UUIDs; this admits any plain file name part.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func path(dir, sessionID string) (string, error) {
	if !safeID.MatchString(sessionID) {
		return "", errors.New("not a session id")
	}
	return filepath.Join(dir, sessionID+".json"), nil
}

// Write records that sessionID waits for permission since now, replacing
// any earlier marker. The write is atomic: a reader sees the old marker or
// the new one.
func Write(dir, sessionID, message string, now time.Time) error {
	return writeMarker(dir, marker{SessionID: sessionID, Waiting: Waiting{Kind: KindPermission, Message: message, Since: now}})
}

func writeMarker(dir string, m marker) error {
	p, err := path(dir, m.SessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs x; 0700 is owner only
		return err
	}
	m.Message, m.Since = tidy(m.Message), m.Since.UTC()
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*.json") // CreateTemp makes it 0600
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save the marker: %w", err)
	}
	return nil
}

// tidy keeps one line of at most MaxMessage runes.
func tidy(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > MaxMessage {
		s = string(r[:MaxMessage-1]) + "…"
	}
	return s
}

// Remove deletes sessionID's marker; a missing one is not an error.
func Remove(dir, sessionID string) error {
	p, err := path(dir, sessionID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Read returns sessionID's marker, if there is a readable one.
func Read(dir, sessionID string) (Waiting, bool) {
	m, ok := readMarker(dir, sessionID)
	return m.Waiting, ok
}

func readMarker(dir, sessionID string) (marker, bool) {
	p, err := path(dir, sessionID)
	if err != nil {
		return marker{}, false
	}
	return readFile(p, sessionID)
}

func readFile(p, sessionID string) (marker, bool) {
	data, err := os.ReadFile(p) //nolint:gosec // p is dir plus a checked session id
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(data, &m) != nil || m.SessionID != sessionID || m.Kind == "" {
		return marker{}, false
	}
	return m, true
}

// List returns every readable marker by session id. A missing directory
// lists nothing.
func List(dir string) map[string]Waiting {
	out := map[string]Waiting{}
	for id, m := range listMarkers(dir) {
		out[id] = m.Waiting
	}
	return out
}

func listMarkers(dir string) map[string]marker {
	out := map[string]marker{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || !safeID.MatchString(id) {
			continue
		}
		if m, ok := readFile(filepath.Join(dir, e.Name()), id); ok {
			out[id] = m
		}
	}
	return out
}
