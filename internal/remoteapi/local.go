package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
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
	// Created is the sessions aimux started; they are listed although the
	// agent SDK marks them automated. Nil: none.
	Created *CreatedStore
	// Running reports whether aimux has a run for a session: a created
	// session without a file is usable only while it does.
	Running func(id string) bool
	// SearchIndex ranks sessions for a query (aimux's session search),
	// among the given sessions only.
	SearchIndex func(ctx context.Context, query string, limit int, ids []string) ([]search.Result, error)
}

func (x *LocalIndex) running(id string) bool { return x.Running != nil && x.Running(id) }

func (x *LocalIndex) openSessions() map[string]bool {
	open := map[string]bool{}
	for pid, e := range discovery.ReadClaudeRegistry(x.RegistryDir) {
		if discovery.IsLiveHolder(e) && (x.Owned == nil || !x.Owned(pid)) {
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
	// No limit here: filtering happens below, and the index scan is cached.
	all, err := x.Discover(history.DiscoverOpts{Provider: "claude"}, x.ProjectsDir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	open := x.openSessions()
	out := make([]Session, 0, limit)
	seen := map[string]bool{}
	for _, s := range all {
		seen[s.ID] = true
		if s.Provider != "claude" || s.IsSubagent {
			continue
		}
		if _, created := x.Created.Get(s.ID); s.Automated && !includeAutomated && !created {
			continue
		}
		out = append(out, summary(s, x.state(s.ID, open)))
	}
	// Sessions created here that have no file yet (still starting, or failed
	// before the agent wrote one) are listed from their creation record.
	for id, c := range x.Created.All() {
		if !seen[id] && x.running(id) {
			out = append(out, Session{ID: id, Provider: "claude", Title: c.Prompt, Project: c.Cwd,
				LastActive: c.Created, State: x.state(id, open)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func summary(s history.Session, state string) Session {
	title := s.Title
	if title == "" {
		title = s.AITitle
	}
	if title == "" {
		title = shorten(cleanPrompt(s.FirstPrompt), 80)
	}
	return Session{ID: s.ID, Provider: s.Provider, Title: title, Project: s.Project, LastActive: s.LastActive, State: state,
		LastPrompt: shorten(cleanPrompt(s.LastPrompt), 160)}
}

var imageTag = regexp.MustCompile(`\[Image(?: #\d+|: [^\]]*)\]`)

// cleanPrompt turns a stored prompt into what a person would call it:
// without image placeholders, and with the messages Claude Code itself puts
// in the user's place (hook feedback, notifications) named, not quoted.
func cleanPrompt(p string) string {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return ""
	case strings.HasPrefix(p, "The user sent a new message while you were working:"):
		p = strings.TrimPrefix(p, "The user sent a new message while you were working:")
	case strings.HasPrefix(p, "Stop hook feedback:"):
		return "(hook feedback)"
	case strings.HasPrefix(p, "Another Claude session sent a message"):
		return "(message from another session)"
	case strings.HasPrefix(p, "<"):
		return "(notification)"
	}
	hadImage := imageTag.MatchString(p)
	p = strings.Join(strings.Fields(imageTag.ReplaceAllString(p, " ")), " ")
	if p == "" && hadImage {
		return "(image)"
	}
	return p
}

// Session returns one session and its last turns.
func (x *LocalIndex) Session(ctx context.Context, id string, turns int) (SessionDetail, error) {
	file := discovery.FindSessionFile(id, x.ProjectsDir)
	if file == "" {
		if c, ok := x.Created.Get(id); ok {
			// Created here, and the agent has not written its file yet. Without
			// a run either, its first prompt failed: nothing to continue.
			state := StateUnavailable
			if x.running(id) {
				state = x.state(id, x.openSessions())
			}
			return SessionDetail{Session: Session{ID: id, Provider: "claude", Title: c.Prompt, Project: c.Cwd,
				LastActive: c.Created, State: state}, Turns: []json.RawMessage{}}, nil
		}
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

// maxProjects is how many recent project folders a client may start a new
// session in.
const maxProjects = 12

// Project is a folder recent Claude sessions ran in.
type Project struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	LastActive time.Time `json:"lastActive"`
}

// Projects lists the folders of recent interactive Claude sessions, most
// recent first: the only places a client may start a new session.
func (x *LocalIndex) Projects(ctx context.Context) ([]Project, error) {
	all, err := x.Discover(history.DiscoverOpts{Provider: "claude"}, x.ProjectsDir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].LastActive.After(all[j].LastActive) })
	seen := map[string]bool{}
	out := []Project{}
	for _, s := range all {
		// The recorded working directory: Project is rebuilt from Claude's
		// folder name, which different paths can share.
		dir := s.Cwd
		if dir == "" {
			dir = s.Project
		}
		_, created := x.Created.Get(s.ID)
		if s.Provider != "claude" || s.IsSubagent || (s.Automated && !created) || dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, Project{Path: dir, Name: filepath.Base(dir), LastActive: s.LastActive})
		if len(out) == maxProjects {
			break
		}
	}
	return out, nil
}

// listableIDs are the sessions the list shows: interactive Claude sessions
// with a file, and sessions created here that have a file or a live run.
func (x *LocalIndex) listableIDs() []string {
	ids := []string{}
	all, err := x.Discover(history.DiscoverOpts{Provider: "claude"}, x.ProjectsDir)
	if err == nil {
		for _, s := range all {
			if _, created := x.Created.Get(s.ID); s.Provider == "claude" && !s.IsSubagent && (!s.Automated || created) {
				ids = append(ids, s.ID)
			}
		}
	}
	for id := range x.Created.All() {
		if x.running(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// searchable reports a search result the user can open and continue: it
// matched some text, it is not automated (unless aimux created it), and it
// still has a session file or, created here, a live run.
func (x *LocalIndex) searchable(r search.Result) bool {
	if r.Snippet == "" {
		return false
	}
	_, created := x.Created.Get(r.SessionID)
	if r.Automated && !created {
		return false
	}
	return discovery.FindSessionFile(r.SessionID, x.ProjectsDir) != "" || (created && x.running(r.SessionID))
}

// SearchHit is a session matching a search, with the matching text.
type SearchHit struct {
	Session
	Snippet string `json:"snippet"`
}

// Search ranks this machine's Claude sessions for query. Like the list, it
// leaves out automated sessions (unless aimux created them) and sessions
// that are only in aimux's archive, which cannot be continued.
func (x *LocalIndex) Search(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	if x.SearchIndex == nil {
		return nil, errors.New("session search is not available")
	}
	// Search only the sessions the list would show: filtering after ranking
	// can lose them behind the index's caps.
	results, err := x.SearchIndex(ctx, query, limit, x.listableIDs())
	if err != nil {
		return nil, fmt.Errorf("search sessions: %w", err)
	}
	open := x.openSessions()
	hits := []SearchHit{}
	for _, r := range results {
		if !x.searchable(r) {
			continue
		}
		c, _ := x.Created.Get(r.SessionID)
		title := shorten(cleanPrompt(r.Title), 80)
		if title == "" {
			title = c.Prompt
		}
		hits = append(hits, SearchHit{Session: Session{ID: r.SessionID, Provider: "claude", Title: title, Project: r.CWD,
			LastActive: r.ModTime, State: x.state(r.SessionID, open)}, Snippet: r.Snippet})
		if len(hits) == limit {
			break
		}
	}
	return hits, nil
}
