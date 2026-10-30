package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/terminput"
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
	// ParseTail returns at least the last n turns, reading only the end of
	// the file; used instead of ParseTrace when set.
	ParseTail func(path string, n int) ([]trace.Turn, error)
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
	// Terminal, if set, gives the detail of a session open in a terminal its
	// terminal status (terminput.Service).
	Terminal TerminalStatusReader
	// Repo gives the web address and branch of the git repository in a
	// folder (gitremote.Lookup). Nil: details carry no repo.
	Repo func(dir string) (web, branch, root string, ok bool)

	mu   sync.Mutex
	last map[string]history.Session // by id, from the latest scan
}

// TerminalStatusReader reports a terminal session's status; registryStatus
// is the session's status in Claude's registry.
type TerminalStatusReader interface {
	Status(ctx context.Context, sessionID, registryStatus string) terminput.Status
}

func (x *LocalIndex) running(id string) bool { return x.Running != nil && x.Running(id) }

// openSessions reads Claude's registry once: the sessions a live process
// holds that is not aimux's own, by session id.
func (x *LocalIndex) openSessions() map[string]discovery.ClaudeRegistryEntry {
	open := map[string]discovery.ClaudeRegistryEntry{}
	for pid, e := range discovery.ReadClaudeRegistry(x.RegistryDir) {
		if discovery.IsLiveHolder(e) && (x.Owned == nil || !x.Owned(pid)) {
			open[e.SessionID] = e
		}
	}
	return open
}

// TerminalPIDs maps every session open in a terminal to the pid holding
// it, by the rule TerminalPID uses.
func (x *LocalIndex) TerminalPIDs() map[string]int {
	out := map[string]int{}
	for id, e := range x.openSessions() {
		out[id] = e.PID
	}
	return out
}

// discover scans the history and remembers the result for Lookup.
func (x *LocalIndex) discover() ([]history.Session, error) {
	all, err := x.Discover(history.DiscoverOpts{Provider: "claude"}, x.ProjectsDir)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]history.Session, len(all))
	for _, s := range all {
		byID[s.ID] = s
	}
	x.mu.Lock()
	x.last = byID
	x.mu.Unlock()
	return all, nil
}

// Lookup is a session's history record from the latest scan, without
// scanning again (the namer's Lookup).
func (x *LocalIndex) Lookup(id string) (history.Session, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	s, ok := x.last[id]
	return s, ok
}

// Record is a session's history record (its file and metadata), freshly
// scanned; acprun.ErrUnknownSession when the history has none.
func (x *LocalIndex) Record(_ context.Context, id string) (history.Session, error) {
	all, err := x.discover()
	if err != nil {
		return history.Session{}, fmt.Errorf("list sessions: %w", err)
	}
	for _, s := range all {
		if s.ID == id && s.FilePath != "" {
			return s, nil
		}
	}
	return history.Session{}, fmt.Errorf("%w: %s", acprun.ErrUnknownSession, id)
}

// TerminalPID is the pid of the terminal process holding the session, by
// the same rule that makes a session open_in_terminal (terminput.Sessions).
func (x *LocalIndex) TerminalPID(sessionID string) (int, bool) {
	e, ok := x.openSessions()[sessionID]
	return e.PID, ok
}

// RegistryStatus is the session's status in Claude's registry ("busy",
// "idle" or ""), "" when no terminal process holds it
// (terminput.RegistryStatuses).
func (x *LocalIndex) RegistryStatus(sessionID string) string {
	return x.openSessions()[sessionID].Status
}

func (x *LocalIndex) state(id string, open map[string]discovery.ClaudeRegistryEntry) string {
	_, inTerminal := open[id]
	switch {
	case x.Broker.Busy(id):
		return StateBusy
	case inTerminal:
		return StateOpenInTerminal
	default:
		return StateIdle
	}
}

// Sessions lists the most recently active sessions; automated ones only when
// includeAutomated is set.
func (x *LocalIndex) Sessions(ctx context.Context, limit int, includeAutomated bool) ([]Session, error) {
	// No limit here: filtering happens below, and the index scan is cached.
	all, err := x.discover()
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
	desc, _ := history.ShownDescription(s)
	return Session{ID: s.ID, Provider: s.Provider, Title: shownTitle(s), Project: s.Project, LastActive: s.LastActive, State: state,
		LastPrompt: shorten(cleanPrompt(s.LastPrompt), 160), Description: desc, Pinned: s.Starred}
}

// shownTitle is the title clients show: aimux's metadata title, else
// Claude Code's, else the first message.
func shownTitle(s history.Session) string {
	title := s.Title
	if title == "" {
		title = s.AITitle
	}
	if title == "" {
		title = shorten(cleanPrompt(s.FirstPrompt), 80)
	}
	return title
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
	return x.session(ctx, id, turns, true)
}

// SessionExists answers acprun.ErrUnknownSession for a session aimux does
// not know: neither a session file nor one created here. It reads no
// terminal, history or repository.
func (x *LocalIndex) SessionExists(_ context.Context, id string) error {
	if discovery.FindSessionFile(id, x.ProjectsDir) != "" {
		return nil
	}
	if _, ok := x.Created.Get(id); ok {
		return nil
	}
	return fmt.Errorf("%w: %s", acprun.ErrUnknownSession, id)
}

// Title returns a session's title without reading its terminal (no screen
// read, no Apple Event), for push notification titles.
func (x *LocalIndex) Title(ctx context.Context, id string) (string, error) {
	d, err := x.session(ctx, id, 1, false)
	return d.Title, err
}

func (x *LocalIndex) session(ctx context.Context, id string, turns int, withTerminal bool) (SessionDetail, error) {
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
				LastActive: c.Created, State: state}, Turns: []Turn{}}, nil
		}
		return SessionDetail{}, fmt.Errorf("%w: %s", acprun.ErrUnknownSession, id)
	}
	var parsed []trace.Turn
	var err error
	if x.ParseTail != nil {
		parsed, err = x.ParseTail(file, turns)
	} else {
		parsed, err = x.ParseTrace(file)
	}
	if err != nil {
		return SessionDetail{}, fmt.Errorf("read session %s: %w", id, err)
	}
	if len(parsed) > turns {
		parsed = parsed[len(parsed)-turns:]
	}
	open := x.openSessions()
	sum, indexed := x.lookupSummary(id, open)
	d := SessionDetail{Session: sum, Turns: make([]Turn, 0, len(parsed))}
	if withTerminal && d.State == StateOpenInTerminal && x.Terminal != nil {
		d.Terminal = terminalStatusOf(x.Terminal.Status(ctx, id, open[id].Status))
	}
	if dir := cwdOf(indexed); x.Repo != nil && dir != "" {
		if web, branch, root, ok := x.Repo(dir); ok {
			d.Repo = &Repo{Web: web, Branch: branch, Root: root}
		}
	}
	if indexed != nil {
		d.Stats = &SessionStats{CostUSD: indexed.CostUSD, TokensIn: indexed.TokensIn, TokensOut: indexed.TokensOut,
			CacheReadTokens: indexed.CacheReadTokens, CacheWriteTokens: indexed.CacheWriteTokens, Turns: indexed.TurnCount,
			StartTime: timeOrNil(indexed.StartTime), DurationMin: indexed.DurationMin, Model: indexed.Model, GitBranch: indexed.GitBranch}
	}
	for _, t := range parsed {
		d.Turns = append(d.Turns, turnOf(t))
	}
	return d, nil
}

// cwdOf is the working directory the session recorded, or "" when it has
// none: Project is rebuilt from Claude's folder name, which different paths
// can share, so looking a repo up there could name another folder's repo.
func cwdOf(s *history.Session) string {
	if s == nil {
		return ""
	}
	return s.Cwd
}

// timeOrNil is nil for the zero time, which means the index has no value.
func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func turnOf(t trace.Turn) Turn {
	tools := make([]TurnTool, 0, len(t.Actions))
	for _, a := range t.Actions {
		tools = append(tools, TurnTool{Name: a.Name, Snippet: shorten(a.Snippet, 200), OK: a.Success, Detail: detailOf(a)})
	}
	return Turn{
		Number: t.Number,
		Time:   t.Timestamp,
		User:   shorten(strings.Join(t.UserLines, "\n"), maxTextLen),
		Reply:  shortenLines(replyOf(t), maxTextLen),
		Tools:  tools,
	}
}

// Caps, in bytes, on the step detail a client can ask for.
const (
	maxDetailInput = 4096
	maxDetailError = 2048
	maxDetailDiff  = 4096 // each of old and new
)

// detailOf maps a tool span to its detail; nil when every field is empty.
// Input and error are trimmed; old and new keep their whitespace. A Write's
// content is its "new" text. An input the snippet already shows (a path, a
// query, or a Grep pattern in slashes) is dropped, so the step does not open
// just to repeat its line.
func detailOf(a trace.ToolSpan) *ToolDetail {
	input := strings.TrimSpace(a.Input)
	if snip := strings.TrimSpace(a.Snippet); input == snip || "/"+input+"/" == snip {
		input = ""
	}
	d := ToolDetail{
		Input: shorten(input, maxDetailInput),
		Error: shorten(a.ErrorMsg, maxDetailError),
		Old:   capText(a.OldString, maxDetailDiff),
		New:   capText(a.NewString, maxDetailDiff),
	}
	if d.New == "" {
		d.New = capText(a.Content, maxDetailDiff)
	}
	if d == (ToolDetail{}) {
		return nil
	}
	return &d
}

// capText cuts s to at most n bytes on a UTF-8 boundary and appends "…"; it
// does not trim.
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
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

// replyOf is the reply as written, or the joined lines for a provider that
// does not record it.
func replyOf(t trace.Turn) string {
	if t.OutputText != "" {
		return t.OutputText
	}
	return strings.Join(t.OutputLines, "\n")
}

// shortenLines is shorten for Markdown text: over n bytes it cuts at the last
// line break in the second half of the cap and appends "…" as a paragraph of
// its own, so the cut cannot leave the ellipsis as a table row; a text with no
// such line break is cut like shorten.
func shortenLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	if nl := strings.LastIndexByte(s[:n+1], '\n'); nl > n/2 {
		return strings.TrimRight(s[:nl], " \t\r\n") + "\n\n…"
	}
	return shorten(s, n)
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// lookupSummary finds the session's title, project and last activity in the
// session index, and returns the index entry too (nil when unindexed); an
// unindexed session still gets its id, provider and state.
func (x *LocalIndex) lookupSummary(id string, open map[string]discovery.ClaudeRegistryEntry) (Session, *history.Session) {
	state := x.state(id, open)
	all, err := x.discover()
	if err == nil {
		for _, s := range all {
			if s.ID == id {
				return summary(s, state), &s
			}
		}
	}
	return Session{ID: id, Provider: "claude", State: state}, nil
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
	all, err := x.discover()
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
		hit := SearchHit{Session: Session{ID: r.SessionID, Provider: "claude", Title: title, Project: r.CWD,
			LastActive: r.ModTime, State: x.state(r.SessionID, open)}, Snippet: r.Snippet}
		if s, ok := x.Lookup(r.SessionID); ok {
			hit.Description, _ = history.ShownDescription(s)
			hit.Pinned = s.Starred
		}
		hits = append(hits, hit)
		if len(hits) == limit {
			break
		}
	}
	return hits, nil
}
