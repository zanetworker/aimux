package acprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	// ErrOwnershipUnknown: without Claude's session registry aimux cannot
	// tell whether a terminal holds the session, so it does not risk a
	// second writer.
	ErrOwnershipUnknown = errors.New("cannot tell whether the session is open in a terminal: Claude's session registry is unavailable")
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
	// Another aimux process first: its adapter would otherwise look like a
	// terminal below. (The manager takes the real reservation.)
	if err := r.Manager.ProbeReserved(sessionID); err != nil {
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
		registry, err := discovery.ReadClaudeRegistryStrict(registryDir)
		if err != nil {
			// A registry aimux cannot fully read may hide the terminal that
			// holds this session.
			return SessionInfo{}, fmt.Errorf("%w: %v", ErrOwnershipUnknown, err)
		}
		info := SessionInfo{Provider: "claude", Cwd: cwd}
		seen := map[int]bool{}
		for pid, e := range registry {
			if e.SessionID == sessionID && discovery.IsLiveHolder(e) {
				seen[pid] = true
			}
		}
		// The registry can miss a session (an older Claude Code, a
		// half-written entry); a Claude process with the session id on its
		// command line holds it too.
		if procs, err := listProcesses(); err == nil {
			for _, p := range procs {
				if commandLineHolds(p.Args, sessionID) && syscall.Kill(p.PID, 0) == nil {
					seen[p.PID] = true
				}
			}
		}
		for pid := range seen {
			info.Holders = append(info.Holders, pid)
		}
		info.OpenInTerminal = len(info.Holders) > 0
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

// ClaudeFingerprint fingerprints a Claude session by its file's size and
// modification time, for ManagerOptions.Fingerprint.
func ClaudeFingerprint(projectsDir string) func(sessionID string) string {
	return func(sessionID string) string {
		file := discovery.FindSessionFile(sessionID, projectsDir)
		if file == "" {
			return ""
		}
		info, err := os.Stat(file)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	}
}

type processArgs struct {
	PID  int
	Args string
}

// listProcesses lists processes with their command lines. A variable so
// tests can replace it.
var listProcesses = func() ([]processArgs, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,args=").Output()
	if err != nil {
		return nil, err
	}
	var procs []processArgs
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		procs = append(procs, processArgs{PID: pid, Args: strings.Join(fields[1:], " ")})
	}
	return procs, nil
}

// commandLineHolds reports whether a process command line is Claude Code
// working on sessionID (for example `claude --resume <id>`).
func commandLineHolds(args, sessionID string) bool {
	fields := strings.Fields(args)
	if len(fields) == 0 || filepath.Base(fields[0]) != "claude" {
		return false
	}
	for _, f := range fields[1:] {
		if f == sessionID || strings.HasSuffix(f, "="+sessionID) {
			return true
		}
	}
	return false
}
