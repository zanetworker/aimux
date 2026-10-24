package acprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/jsonl"
)

// Router errors.
var (
	ErrUnknownSession = errors.New("no such session")
	// ErrOpenInTerminal: replying to a session open in a terminal is left
	// out on purpose; typing into one can approve a permission prompt the
	// user never saw. Close it there to continue it here.
	ErrOpenInTerminal = errors.New("the session is open in a terminal on this machine; close it there to continue it here")
)

// SessionInfo is what the router needs to know about a session.
type SessionInfo struct {
	Provider       string
	Cwd            string
	OpenInTerminal bool  // some live process holds the session
	Holders        []int // those processes
}

// Lookup finds a session by ID.
type Lookup func(sessionID string) (SessionInfo, error)

// Router sends a message to a session the right way for its state.
type Router struct {
	Lookup  Lookup
	Manager *Manager
}

// Send continues sessionID with text through the manager, unless the session
// is unknown or open in a terminal that aimux does not drive.
func (r *Router) Send(ctx context.Context, sessionID, text string, onEvent func(Event)) (string, error) {
	info, err := r.route(sessionID)
	if err != nil {
		return "", err
	}
	return r.Manager.Send(ctx, sessionID, info.Cwd, text, onEvent)
}

// Check reports whether Send would accept sessionID, without sending.
func (r *Router) Check(sessionID string) error {
	_, err := r.route(sessionID)
	return err
}

func (r *Router) route(sessionID string) (SessionInfo, error) {
	info, err := r.Lookup(sessionID)
	if err != nil {
		return SessionInfo{}, err
	}
	// A session aimux runs shows up in Claude's registry through the
	// adapter's own process; any other holder is a user's terminal, even
	// while aimux keeps an idle run for the session.
	for _, pid := range info.Holders {
		if !r.Manager.Owns(pid) {
			return SessionInfo{}, fmt.Errorf("%w (session %s)", ErrOpenInTerminal, sessionID)
		}
	}
	return info, nil
}

// ClaudeLookup finds Claude Code sessions: the session file under
// projectsDir gives the working directory, and a live process in Claude's
// registry (registryDir) holding the session means it is open in a terminal.
func ClaudeLookup(projectsDir, registryDir string) Lookup {
	return func(sessionID string) (SessionInfo, error) {
		file := discovery.FindSessionFile(sessionID, projectsDir)
		if file == "" {
			return SessionInfo{}, fmt.Errorf("%w: %s", ErrUnknownSession, sessionID)
		}
		cwd, err := sessionCwd(file)
		if err != nil {
			return SessionInfo{}, err
		}
		info := SessionInfo{Provider: "claude", Cwd: cwd}
		for pid, e := range discovery.ReadClaudeRegistry(registryDir) {
			if e.SessionID == sessionID && syscall.Kill(pid, 0) == nil {
				info.OpenInTerminal = true
				info.Holders = append(info.Holders, pid)
			}
		}
		return info, nil
	}
}

// sessionCwd returns the first working directory recorded in a Claude
// session file.
func sessionCwd(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- path found under the Claude projects dir
	if err != nil {
		return "", fmt.Errorf("open session file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	s := jsonl.NewScanner(f)
	for s.Scan() {
		var line struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(s.Bytes(), &line) == nil && line.Cwd != "" {
			return line.Cwd, nil
		}
	}
	if err := s.Err(); err != nil {
		return "", fmt.Errorf("read session file %s: %w", path, err)
	}
	return "", fmt.Errorf("session file %s records no working directory", path)
}
