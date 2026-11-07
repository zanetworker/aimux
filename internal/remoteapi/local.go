package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/trace"
)

// maxTextLen caps each turn's user text and reply sent to clients.
const maxTextLen = 8 * 1024

// Store is a harness's sessions as the remote API serves them: the
// harness.SessionStore capability plus the list of its sessions and the
// latest turns of a transcript. provider.Claude implements it.
type Store interface {
	harness.SessionStore
	// Name is the harness's provider name, the Provider of its sessions.
	Name() string
	// Sessions lists the harness's sessions; records of another provider
	// are left out of the index.
	Sessions(opts history.DiscoverOpts) ([]history.Session, error)
	// ParseTail returns at least the last n turns of the transcript at
	// path, reading only the end of the file.
	ParseTail(path string, n int) ([]trace.Turn, error)
}

// Provider is one harness the index serves: its sessions and the processes
// holding them open.
type Provider struct {
	Store Store
	// Live is the sessions a process of this harness holds open (remote
	// serve shares one discovery.LiveCache with every other reader). Nil:
	// none.
	Live harness.LiveSessions
}

// LocalIndex answers the session list and detail from this machine's
// sessions of the registered providers; subagent and automated sessions are
// left out. Every provider's sessions are listed; the default, NewProvider,
// is the one that can be continued today, since the router (SessionLookup)
// and the chat manager's Fingerprint serve one provider (remote serve's
// remoteProviders).
type LocalIndex struct {
	// Providers are the harnesses listed, in order; a session belongs to
	// the first whose store has its file.
	Providers []Provider
	// NewProvider is the Provider of sessions created here (Created
	// records without a file yet): the harness acprun starts them with.
	// "": the first provider's name.
	NewProvider string
	Broker      *Broker
	// Owned reports aimux's own adapter processes, which hold a session in
	// a harness's registry without it being open in a terminal.
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
// is the session's status in its harness's registry.
type TerminalStatusReader interface {
	Status(ctx context.Context, sessionID, registryStatus string) terminput.Status
}

func (x *LocalIndex) running(id string) bool { return x.Running != nil && x.Running(id) }

// openSessions is the sessions a live process holds that is not aimux's
// own, by session id (harness.OpenSessions over every provider's Live). A
// registry is optional, so an error reads as no session open.
func (x *LocalIndex) openSessions() map[string]harness.Holder {
	var hs []harness.Holder
	for _, p := range x.Providers {
		if p.Live != nil {
			got, _ := p.Live.Holders(harness.List) // nothing open beats failing the list
			hs = append(hs, got...)
		}
	}
	return harness.OpenSessions(hs, x.Owned)
}

// newProvider is NewProvider, or the first provider's name when unset, so a
// session created here never carries provider ""; "" without providers.
func (x *LocalIndex) newProvider() string {
	if x.NewProvider == "" && len(x.Providers) > 0 {
		return x.Providers[0].Store.Name()
	}
	return x.NewProvider
}

// sessionFile is session id's transcript and the store that has it, the
// first in order (the first store wins when two providers report the same
// id); "" and nil when none has.
func (x *LocalIndex) sessionFile(id string) (string, Store) {
	for _, p := range x.Providers {
		if f := p.Store.SessionFile(id); f != "" {
			return f, p.Store
		}
	}
	return "", nil
}

// scan lists every provider's sessions with Provider set to the provider's
// name, leaving out records a store gives for another provider.
func (x *LocalIndex) scan() ([]history.Session, error) {
	var all []history.Session
	for _, p := range x.Providers {
		name := p.Store.Name()
		got, err := p.Store.Sessions(history.DiscoverOpts{})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, s := range got {
			if s.Provider != "" && s.Provider != name {
				continue
			}
			s.Provider = name
			all = append(all, s)
		}
	}
	return all, nil
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
	all, err := x.scan()
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

// RegistryStatus is the session's status in its harness's registry ("busy",
// "idle" or ""), "" when no terminal process holds it
// (terminput.RegistryStatuses).
func (x *LocalIndex) RegistryStatus(sessionID string) string {
	return x.openSessions()[sessionID].Status
}

func (x *LocalIndex) state(id string, open map[string]harness.Holder) string {
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
		if !x.listable(s, includeAutomated) {
			continue
		}
		sum := summary(s, x.state(s.ID, open))
		sum.Working = open[s.ID].Status == "busy"
		out = append(out, sum)
	}
	// Sessions created here that have no file yet (still starting, or failed
	// before the agent wrote one) are listed from their creation record.
	for id, c := range x.Created.All() {
		if !seen[id] && x.running(id) {
			out = append(out, Session{ID: id, Provider: x.newProvider(), Title: c.Prompt, Project: c.Cwd,
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
	return Session{ID: s.ID, Provider: s.Provider, Title: history.DisplayTitle(s), Project: s.Project, LastActive: s.LastActive, State: state,
		LastPrompt: history.Shorten(history.PromptLabel(s.LastPrompt), 160), Description: desc, Pinned: s.Starred}
}

// Session returns one session and its last turns.
func (x *LocalIndex) Session(ctx context.Context, id string, turns int) (SessionDetail, error) {
	return x.session(ctx, id, turns, true)
}

// SessionExists answers acprun.ErrUnknownSession for a session aimux does
// not know: neither a session file nor one created here. It reads no
// terminal, history or repository.
func (x *LocalIndex) SessionExists(_ context.Context, id string) error {
	if file, _ := x.sessionFile(id); file != "" {
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
	file, store := x.sessionFile(id)
	if file == "" {
		if c, ok := x.Created.Get(id); ok {
			// Created here, and the agent has not written its file yet. Without
			// a run either, its first prompt failed: nothing to continue.
			state := StateUnavailable
			if x.running(id) {
				state = x.state(id, x.openSessions())
			}
			return SessionDetail{Session: Session{ID: id, Provider: x.newProvider(), Title: c.Prompt, Project: c.Cwd,
				LastActive: c.Created, State: state}, Turns: []Turn{}}, nil
		}
		return SessionDetail{}, fmt.Errorf("%w: %s", acprun.ErrUnknownSession, id)
	}
	// store is the first with the file: when two providers report the same
	// id, the first store wins (sessionFile).
	parsed, err := store.ParseTail(file, turns)
	if err != nil {
		return SessionDetail{}, fmt.Errorf("read session %s: %w", id, err)
	}
	if len(parsed) > turns {
		parsed = parsed[len(parsed)-turns:]
	}
	open := x.openSessions()
	sum, indexed := x.lookupSummary(id, store.Name(), open)
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
		tools = append(tools, TurnTool{Name: a.Name, Snippet: history.Shorten(a.Snippet, 200), OK: a.Success, Detail: detailOf(a)})
	}
	return Turn{
		Number: t.Number,
		Time:   t.Timestamp,
		User:   history.Shorten(strings.Join(t.UserLines, "\n"), maxTextLen),
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
		Input: history.Shorten(input, maxDetailInput),
		Error: history.Shorten(a.ErrorMsg, maxDetailError),
		Old:   history.CutBytes(a.OldString, maxDetailDiff),
		New:   history.CutBytes(a.NewString, maxDetailDiff),
	}
	if d.New == "" {
		d.New = history.CutBytes(a.Content, maxDetailDiff)
	}
	if d == (ToolDetail{}) {
		return nil
	}
	return &d
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
	return history.Shorten(s, n)
}

// lookupSummary finds the session's title, project and last activity in the
// session index, and returns the index entry too (nil when unindexed); an
// unindexed session still gets its id, provider and state.
func (x *LocalIndex) lookupSummary(id, provider string, open map[string]harness.Holder) (Session, *history.Session) {
	state := x.state(id, open)
	all, err := x.discover()
	if err == nil {
		for _, s := range all {
			if s.ID == id {
				return summary(s, state), &s
			}
		}
	}
	return Session{ID: id, Provider: provider, State: state}, nil
}

// maxProjects is how many recent project folders a client may start a new
// session in.
const maxProjects = 12

// Project is a folder recent sessions ran in.
type Project struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	LastActive time.Time `json:"lastActive"`
}

// Projects lists the folders of recent interactive sessions, most recent
// first: the only places a client may start a new session.
func (x *LocalIndex) Projects(ctx context.Context) ([]Project, error) {
	all, err := x.scan()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].LastActive.After(all[j].LastActive) })
	seen := map[string]bool{}
	out := []Project{}
	for _, s := range all {
		// The recorded working directory: Project may be rebuilt from a
		// folder name (Claude's), which different paths can share.
		dir := s.Cwd
		if dir == "" {
			dir = s.Project
		}
		if !x.listable(s, false) || dir == "" || seen[dir] {
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

// listable is history.Visible with the remote API's options: never a
// subagent, an automated session only when asked for or created here.
func (x *LocalIndex) listable(s history.Session, includeAutomated bool) bool {
	_, created := x.Created.Get(s.ID)
	return history.Visible(s, history.RemoteOpts(includeAutomated, created))
}

// listableIDs are the sessions the list shows: interactive sessions
// with a file, and sessions created here that have a file or a live run.
func (x *LocalIndex) listableIDs() []string {
	ids := []string{}
	all, err := x.discover()
	if err == nil {
		for _, s := range all {
			if x.listable(s, false) {
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

// searchable reports a search result the user can open and continue, and
// its provider: it matched some text, it is not automated (unless aimux
// created it), and it still has a session file or, created here, a live run.
func (x *LocalIndex) searchable(r search.Result) (string, bool) {
	if r.Snippet == "" {
		return "", false
	}
	_, created := x.Created.Get(r.SessionID)
	// The search scope is listableIDs (history.RemoteOpts); this clause only
	// guards against the index and history disagreeing about automated.
	if r.Automated && !created {
		return "", false
	}
	if file, store := x.sessionFile(r.SessionID); file != "" {
		return store.Name(), true
	}
	return x.newProvider(), created && x.running(r.SessionID)
}

// SearchHit is a session matching a search, with the matching text.
type SearchHit struct {
	Session
	Snippet string `json:"snippet"`
}

// Search ranks this machine's sessions for query. Like the list, it
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
		provider, ok := x.searchable(r)
		if !ok {
			continue
		}
		c, _ := x.Created.Get(r.SessionID)
		// Fallbacks for a session the latest scan does not know (created here
		// and not yet on disk, or written after the scan): the index's title,
		// then the prompt it was created with. A known session takes the
		// list's title below.
		title := history.Shorten(history.PromptLabel(r.Title), 80)
		if title == "" {
			title = c.Prompt
		}
		hit := SearchHit{Session: Session{ID: r.SessionID, Provider: provider, Title: title, Project: r.CWD,
			LastActive: r.ModTime, State: x.state(r.SessionID, open)}, Snippet: r.Snippet}
		if s, ok := x.Lookup(r.SessionID); ok {
			hit.Title = history.DisplayTitle(s) // the list's title, not the index's
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
