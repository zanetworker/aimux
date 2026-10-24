package remoteapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"syscall"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/trace"
)

// maxTextLen caps each turn's user text and reply sent to clients.
const maxTextLen = 8 * 1024

// LocalIndex answers the session list and detail from this machine's Claude
// Code sessions. Only Claude sessions are listed, since only they can be
// continued; subagent and automated sessions are left out.
type LocalIndex struct {
	ProjectsDir string // ~/.claude/projects
	RegistryDir string // ~/.claude/sessions
	Broker      *Broker
	Discover    func(opts history.DiscoverOpts, projectsDir string) ([]history.Session, error)
	ParseTrace  func(path string) ([]trace.Turn, error)
	// Owned reports aimux's own adapter processes, which hold a session in
	// Claude's registry without it being open in a terminal.
	Owned func(pid int) bool
}

func (x *LocalIndex) openSessions() map[string]bool {
	open := map[string]bool{}
	for pid, e := range discovery.ReadClaudeRegistry(x.RegistryDir) {
		if syscall.Kill(pid, 0) == nil && (x.Owned == nil || !x.Owned(pid)) {
			open[e.SessionID] = true
		}
	}
	return open
}

func (x *LocalIndex) state(id string, open map[string]bool) string {
	switch {
	case x.Broker.Busy(id):
		return StateBusy
	case open[id]:
		return StateOpenInTerminal
	default:
		return StateIdle
	}
}

// Sessions lists the most recently active sessions; automated ones only when
// includeAutomated is set.
func (x *LocalIndex) Sessions(ctx context.Context, limit int, includeAutomated bool) ([]Session, error) {
	all, err := x.Discover(history.DiscoverOpts{Provider: "claude", Limit: limit * 2}, x.ProjectsDir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	open := x.openSessions()
	out := make([]Session, 0, limit)
	for _, s := range all {
		if s.Provider != "claude" || s.IsSubagent || (s.Automated && !includeAutomated) {
			continue
		}
		out = append(out, summary(s, x.state(s.ID, open)))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func summary(s history.Session, state string) Session {
	title := s.Title
	if title == "" {
		title = shorten(s.FirstPrompt, 80)
	}
	return Session{ID: s.ID, Provider: s.Provider, Title: title, Project: s.Project, LastActive: s.LastActive, State: state}
}

// Session returns one session and its last turns.
func (x *LocalIndex) Session(ctx context.Context, id string, turns int) (SessionDetail, error) {
	file := discovery.FindSessionFile(id, x.ProjectsDir)
	if file == "" {
		return SessionDetail{}, fmt.Errorf("%w: %s", acprun.ErrUnknownSession, id)
	}
	parsed, err := x.ParseTrace(file)
	if err != nil {
		return SessionDetail{}, fmt.Errorf("read session %s: %w", id, err)
	}
	if len(parsed) > turns {
		parsed = parsed[len(parsed)-turns:]
	}
	d := SessionDetail{
		Session: x.lookupSummary(id),
		Turns:   make([]json.RawMessage, 0, len(parsed)),
	}
	for _, t := range parsed {
		d.Turns = append(d.Turns, turnJSON(t))
	}
	return d, nil
}

func turnJSON(t trace.Turn) json.RawMessage {
	type tool struct {
		Name    string `json:"name"`
		Snippet string `json:"snippet,omitempty"`
		OK      bool   `json:"ok"`
	}
	tools := make([]tool, 0, len(t.Actions))
	for _, a := range t.Actions {
		tools = append(tools, tool{Name: a.Name, Snippet: shorten(a.Snippet, 200), OK: a.Success})
	}
	data, _ := json.Marshal(map[string]any{
		"number": t.Number,
		"time":   t.Timestamp,
		"user":   shorten(strings.Join(t.UserLines, "\n"), maxTextLen),
		"reply":  shorten(strings.Join(t.OutputLines, "\n"), maxTextLen),
		"tools":  tools,
	})
	return data
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut-- // do not split a UTF-8 sequence
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// lookupSummary finds the session's title, project and last activity in the
// session index; an unindexed session still gets its id, provider and state.
func (x *LocalIndex) lookupSummary(id string) Session {
	state := x.state(id, x.openSessions())
	all, err := x.Discover(history.DiscoverOpts{Provider: "claude"}, x.ProjectsDir)
	if err == nil {
		for _, s := range all {
			if s.ID == id {
				return summary(s, state)
			}
		}
	}
	return Session{ID: id, Provider: "claude", State: state}
}
