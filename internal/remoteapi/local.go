package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/catalog"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/trace"
)

// maxTextLen caps each turn's user text and reply sent to clients.
const maxTextLen = 8 * 1024

// Store is a harness's sessions as the remote API serves them
// (catalog.Store).
type Store = catalog.Store

// Provider is one harness the index serves (catalog.Provider).
type Provider = catalog.Provider

// CreatedSession is a session started through the remote API
// (catalog.CreatedSession).
type CreatedSession = catalog.CreatedSession

// CreatedStore remembers the sessions aimux started (catalog.CreatedStore).
type CreatedStore = catalog.CreatedStore

// LocalIndex answers the session list and detail from this machine's
// sessions of the registered providers: the HTTP- and contract-shaped
// layer over internal/catalog, which owns the listing, visibility, state
// and lookup cache. Every provider's sessions are listed; the default,
// NewProvider, is the one that can be continued today, since the router
// (SessionLookup) and the chat manager's Fingerprint serve one provider
// (catalog.Providers, which remote serve lists).
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

	cache catalog.Cache // the latest scan, for Lookup
}

// TerminalStatusReader reports a terminal session's status; registryStatus
// is the session's status in its harness's registry.
type TerminalStatusReader interface {
	Status(ctx context.Context, sessionID, registryStatus string) terminput.Status
}

// Catalog is the session catalog over the index's providers and live
// state, sharing the index's scan cache (Lookup). It reads the fields as
// they are at the call.
func (x *LocalIndex) Catalog() *catalog.Catalog {
	c := &catalog.Catalog{Providers: x.Providers, NewProvider: x.NewProvider, Owned: x.Owned,
		Created: x.Created, Running: x.Running, Cache: &x.cache}
	if x.Broker != nil {
		c.Busy = x.Broker
	}
	return c
}

func (x *LocalIndex) running(id string) bool { return x.Running != nil && x.Running(id) }

// openSessions is the sessions a live process holds that is not aimux's
// own, by session id (catalog.Open).
func (x *LocalIndex) openSessions() catalog.Open { return x.Catalog().Open() }

// newProvider is the Provider of sessions created here
// (catalog.DefaultProvider).
func (x *LocalIndex) newProvider() string { return x.Catalog().DefaultProvider() }

// TerminalPIDs maps every session open in a terminal to the pid holding
// it, by the rule TerminalPID uses.
func (x *LocalIndex) TerminalPIDs() map[string]int { return x.Catalog().TerminalPIDs() }

// Lookup is a session's history record from the latest scan, without
// scanning again (the namer's Lookup).
func (x *LocalIndex) Lookup(id string) (history.Session, bool) { return x.Catalog().Lookup(id) }

// Record is a session's history record (its file and metadata), freshly
// scanned; acprun.ErrUnknownSession when the history has none.
func (x *LocalIndex) Record(_ context.Context, id string) (history.Session, error) {
	all, err := x.Catalog().Scan()
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
	return x.Catalog().TerminalPID(sessionID)
}

// RegistryStatus is the session's status in its harness's registry ("busy",
// "idle" or ""), "" when no terminal process holds it
// (terminput.RegistryStatuses).
func (x *LocalIndex) RegistryStatus(sessionID string) string {
	return x.Catalog().RegistryStatus(sessionID)
}

// Sessions lists the most recently active sessions; automated ones only when
// includeAutomated is set.
func (x *LocalIndex) Sessions(ctx context.Context, limit int, includeAutomated bool) ([]Session, error) {
	entries, err := x.Catalog().List(history.RemoteOpts(includeAutomated, false))
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]Session, 0, limit)
	for _, e := range entries {
		out = append(out, sessionOf(e))
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// sessionOf is a catalog entry as the list sends it.
func sessionOf(e catalog.Entry) Session {
	if e.Record == nil { // created here, no file yet
		return Session{ID: e.ID, Provider: e.Provider, Title: e.Title, Project: e.Project, LastActive: e.LastActive, State: e.State}
	}
	sum := summary(*e.Record, e.State)
	sum.Working = e.Working
	return sum
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
	if x.Catalog().Exists(id) {
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
	cat := x.Catalog()
	file, store := cat.File(id)
	if file == "" {
		if c, ok := x.Created.Get(id); ok {
			// Created here, and the agent has not written its file yet. Without
			// a run either, its first prompt failed: nothing to continue.
			state := StateUnavailable
			if x.running(id) {
				state = cat.State(id)
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
	open := cat.Open()
	sum, indexed := lookupSummary(cat, id, store.Name(), open)
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

// lookupSummary finds the session's title, project and last activity in a
// fresh scan, and returns the record too (nil when unindexed); an
// unindexed session still gets its id, provider and state.
func lookupSummary(cat *catalog.Catalog, id, provider string, open catalog.Open) (Session, *history.Session) {
	state := cat.StateOf(id, open)
	all, err := cat.Scan()
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
	ps, err := x.Catalog().Projects(history.RemoteOpts(false, false), maxProjects)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]Project, 0, len(ps))
	for _, p := range ps {
		out = append(out, Project{Path: p.Path, Name: p.Name, LastActive: p.LastActive})
	}
	return out, nil
}

// listableIDs are the sessions the list shows: interactive sessions
// with a file, and sessions created here that have a file or a live run.
func (x *LocalIndex) listableIDs() []string {
	return x.Catalog().ListableIDs(history.RemoteOpts(false, false))
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
	cat := x.Catalog()
	results, err := x.SearchIndex(ctx, query, limit, cat.ListableIDs(history.RemoteOpts(false, false)))
	if err != nil {
		return nil, fmt.Errorf("search sessions: %w", err)
	}
	open := cat.Open()
	hits := []SearchHit{}
	for _, r := range results {
		// A hit must have matched some text; the search scope is the
		// listable IDs, and Searchable only guards against the index and
		// history disagreeing about automated.
		if r.Snippet == "" {
			continue
		}
		provider, ok := cat.Searchable(r.SessionID, r.Automated)
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
			LastActive: r.ModTime, State: cat.StateOf(r.SessionID, open)}, Snippet: r.Snippet}
		if s, ok := cat.Lookup(r.SessionID); ok {
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
