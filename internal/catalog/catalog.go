// Package catalog is this machine's session catalog: every registered
// provider's sessions joined with their shown title, visibility, provider
// and live state. The remote API (internal/remoteapi) and the MCP server
// (internal/sessionmcp) read sessions through it; it knows no HTTP or
// contract types.
package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/trace"
)

// A session's live state; the remote API sends these values as they are.
const (
	StateIdle           = "idle"             // can be continued from here
	StateOpenInTerminal = "open_in_terminal" // follow only
	StateBusy           = "busy"             // aimux is running a turn
	// StateUnavailable: created here, but its first prompt failed before the
	// agent wrote a file; nothing to continue.
	StateUnavailable = "unavailable"
)

// Store is a harness's sessions as the catalog reads them: the
// harness.SessionStore capability plus the list of its sessions and the
// latest turns of a transcript. provider.Claude implements it.
type Store interface {
	harness.SessionStore
	// Name is the harness's provider name, the Provider of its sessions.
	Name() string
	// Sessions lists the harness's sessions; records of another provider
	// are left out of the catalog.
	Sessions(opts history.DiscoverOpts) ([]history.Session, error)
	// ParseTail returns at least the last n turns of the transcript at
	// path, reading only the end of the file.
	ParseTail(path string, n int) ([]trace.Turn, error)
}

// Provider is one harness the catalog serves: its sessions and the
// processes holding them open.
type Provider struct {
	Store Store
	// Live is the sessions a process of this harness holds open. Nil: none.
	Live harness.LiveSessions
}

// LiveSource is a provider that reports which of its sessions a process
// holds open (provider.LiveSource).
type LiveSource interface {
	LiveSessions(ttl time.Duration) harness.LiveSessions
}

// ProvidersOf is every provider of ps, in order, that implements Store and
// LiveSource, its Live read with ttl. Callers pass the registry's providers
// (provider.Registry Local), so a new harness implements those two and
// needs no change here. Today that is Claude only; the first is the
// default (DefaultProvider). The catalog takes them as values so that it
// does not import the provider registry and everything it builds.
func ProvidersOf[P any](ps []P, ttl time.Duration) []Provider {
	var out []Provider
	for _, p := range ps {
		store, isStore := any(p).(Store)
		live, isLive := any(p).(LiveSource)
		if isStore && isLive {
			out = append(out, Provider{Store: store, Live: live.LiveSessions(ttl)})
		}
	}
	return out
}

// BusyReader reports whether aimux is running a turn of a session (the
// remote API's Broker).
type BusyReader interface {
	Busy(id string) bool
}

// Cache holds the latest scan, by session id, for Lookup. The zero value
// is ready to use.
type Cache struct {
	mu   sync.Mutex
	last map[string]history.Session
}

// Catalog lists and describes the sessions of the registered providers.
// Every session listed belongs to the first provider whose store has its
// file; records a store gives for another provider are dropped.
type Catalog struct {
	// Providers are the harnesses listed, in order.
	Providers []Provider
	// NewProvider is the Provider of sessions created here (Created
	// records without a file yet). "": the first provider's name.
	NewProvider string
	// Busy reports aimux's own running turns. Nil: none.
	Busy BusyReader
	// Owned reports aimux's own adapter processes, which hold a session in
	// a harness's registry without it being open in a terminal.
	Owned func(pid int) bool
	// Created is the sessions aimux started; they are listed although the
	// agent SDK marks them automated. Nil: none.
	Created *CreatedStore
	// Running reports whether aimux has a run for a session: a created
	// session without a file is usable only while it does. Nil: none.
	Running func(id string) bool
	// Cache keeps the latest scan for Lookup; nil: the Catalog's own.
	Cache *Cache

	own Cache
}

// Entry is one session as the catalog lists it.
type Entry struct {
	ID       string
	Provider string
	// Title is the shown title (history.DisplayTitle), or the first
	// message of a session created here that has no file yet.
	Title      string
	Project    string
	Cwd        string // the recorded working directory, "" when unknown
	LastActive time.Time
	State      string // one of the State constants
	// Working: a terminal process holds the session and its registry
	// says busy.
	Working bool
	// Record is the history record; nil for a session created here that
	// has no file yet.
	Record *history.Session
}

// Open is the sessions a live process that is not aimux's own holds, by
// session id: one read of every provider's Live.
type Open map[string]harness.Holder

// Project is a folder recent sessions ran in.
type Project struct {
	Path       string
	Name       string
	LastActive time.Time
}

func (c *Catalog) cache() *Cache {
	if c.Cache != nil {
		return c.Cache
	}
	return &c.own
}

func (c *Catalog) running(id string) bool { return c.Running != nil && c.Running(id) }

// Open reads which sessions are open (harness.OpenSessions over every
// provider's Live). A registry is optional, so an error reads as no
// session open.
func (c *Catalog) Open() Open {
	var hs []harness.Holder
	for _, p := range c.Providers {
		if p.Live != nil {
			got, _ := p.Live.Holders(harness.List) // nothing open beats failing the list
			hs = append(hs, got...)
		}
	}
	return harness.OpenSessions(hs, c.Owned)
}

// StateOf is a session's state given open: busy while aimux runs a turn,
// open_in_terminal while a terminal holds it, idle otherwise.
func (c *Catalog) StateOf(id string, open Open) string {
	_, inTerminal := open[id]
	switch {
	case c.Busy != nil && c.Busy.Busy(id):
		return StateBusy
	case inTerminal:
		return StateOpenInTerminal
	default:
		return StateIdle
	}
}

// State is StateOf with a fresh read of the open sessions.
func (c *Catalog) State(id string) string { return c.StateOf(id, c.Open()) }

// TerminalPIDs maps every session open in a terminal to the pid holding it.
func (c *Catalog) TerminalPIDs() map[string]int {
	out := map[string]int{}
	for id, e := range c.Open() {
		out[id] = e.PID
	}
	return out
}

// TerminalPID is the pid of the terminal process holding the session, by
// the same rule that makes a session open_in_terminal.
func (c *Catalog) TerminalPID(id string) (int, bool) {
	e, ok := c.Open()[id]
	return e.PID, ok
}

// RegistryStatus is the session's status in its harness's registry ("busy",
// "idle" or ""), "" when no terminal process holds it.
func (c *Catalog) RegistryStatus(id string) string { return c.Open()[id].Status }

// DefaultProvider is NewProvider, or the first provider's name when unset,
// so a session created here never carries provider ""; "" without
// providers.
func (c *Catalog) DefaultProvider() string {
	if c.NewProvider == "" && len(c.Providers) > 0 {
		return c.Providers[0].Store.Name()
	}
	return c.NewProvider
}

// File is a session's transcript and the store that has it, the first in
// order (the first store wins when two providers report the same id); ""
// and nil when none has.
func (c *Catalog) File(id string) (string, Store) {
	for _, p := range c.Providers {
		if f := p.Store.SessionFile(id); f != "" {
			return f, p.Store
		}
	}
	return "", nil
}

// Exists reports a session aimux knows: one with a file, or one created
// here. It reads no history scan.
func (c *Catalog) Exists(id string) bool {
	if f, _ := c.File(id); f != "" {
		return true
	}
	_, ok := c.Created.Get(id)
	return ok
}

// scan lists every provider's sessions with Provider set to the provider's
// name, leaving out records a store gives for another provider.
func (c *Catalog) scan() ([]history.Session, error) {
	var all []history.Session
	for _, p := range c.Providers {
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

// Scan lists every provider's sessions and remembers the result for Lookup.
func (c *Catalog) Scan() ([]history.Session, error) {
	all, err := c.scan()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]history.Session, len(all))
	for _, s := range all {
		byID[s.ID] = s
	}
	k := c.cache()
	k.mu.Lock()
	k.last = byID
	k.mu.Unlock()
	return all, nil
}

// Lookup is a session's history record from the latest scan, without
// scanning again.
func (c *Catalog) Lookup(id string) (history.Session, bool) {
	k := c.cache()
	k.mu.Lock()
	defer k.mu.Unlock()
	s, ok := k.last[id]
	return s, ok
}

// visible is history.Visible with o, plus the catalog's exception: a
// session aimux created shows although the agent SDK marks it automated.
func (c *Catalog) visible(s history.Session, o history.VisibleOpts) bool {
	if _, created := c.Created.Get(s.ID); created {
		o.Reveal |= history.KindAutomated
	}
	return history.Visible(s, o)
}

func entryOf(s history.Session, open Open, state string) Entry {
	rec := s
	return Entry{ID: s.ID, Provider: s.Provider, Title: history.DisplayTitle(s), Project: s.Project, Cwd: s.Cwd,
		LastActive: s.LastActive, State: state, Working: open[s.ID].Status == "busy", Record: &rec}
}

func (c *Catalog) createdEntry(id string, cr CreatedSession, state string) Entry {
	return Entry{ID: id, Provider: c.DefaultProvider(), Title: cr.Prompt, Project: cr.Cwd, Cwd: cr.Cwd,
		LastActive: cr.Created, State: state}
}

// List is the sessions a list with options o shows, most recently active
// first: the scanned sessions Visible admits, then the sessions created
// here that have no file yet but a live run.
func (c *Catalog) List(o history.VisibleOpts) ([]Entry, error) { return c.ListWith(o, c.Open()) }

// ListWith is List with the open sessions given (an Open read the caller
// shares, e.g. with the session state service), so State and Working agree
// with what the caller derives from the same read.
func (c *Catalog) ListWith(o history.VisibleOpts, open Open) ([]Entry, error) {
	all, err := c.Scan()
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	seen := map[string]bool{}
	for _, s := range all {
		seen[s.ID] = true
		if c.visible(s, o) {
			out = append(out, entryOf(s, open, c.StateOf(s.ID, open)))
		}
	}
	for id, cr := range c.Created.All() {
		if !seen[id] && c.running(id) {
			out = append(out, c.createdEntry(id, cr, c.StateOf(id, open)))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	return out, nil
}

// Get is one session, freshly scanned, whatever its visibility: its record,
// or a session created here that has no file yet (unavailable unless it
// has a live run). false when the catalog has neither.
func (c *Catalog) Get(id string) (Entry, bool) {
	open := c.Open()
	if all, err := c.Scan(); err == nil {
		for _, s := range all {
			if s.ID == id {
				return entryOf(s, open, c.StateOf(id, open)), true
			}
		}
	}
	if cr, ok := c.Created.Get(id); ok {
		state := StateUnavailable
		if c.running(id) {
			state = c.StateOf(id, open)
		}
		return c.createdEntry(id, cr, state), true
	}
	return Entry{}, false
}

// Rereader is a Store that can read one session's record again, as its
// Sessions would give it now (provider.Claude: history.Reread).
type Rereader interface {
	Reread(s history.Session) (history.Session, error)
}

// Refresh is Get for one session without a full scan: the record from the
// last scan, read again by its store (a /rename or a .meta.json title
// change shows) and put back in the cache. A session the last scan does
// not have, a store without Reread, or a failed reread fall back to Get.
func (c *Catalog) Refresh(id string) (Entry, bool) {
	if rec, ok := c.Lookup(id); ok {
		if rr, ok := c.store(rec.Provider).(Rereader); ok {
			if fresh, err := rr.Reread(rec); err == nil {
				fresh.Provider = rec.Provider
				k := c.cache()
				k.mu.Lock()
				k.last[id] = fresh
				k.mu.Unlock()
				open := c.Open()
				return entryOf(fresh, open, c.StateOf(id, open)), true
			}
		}
	}
	return c.Get(id)
}

// store is the provider store named name; nil when none is.
func (c *Catalog) store(name string) Store {
	for _, p := range c.Providers {
		if p.Store.Name() == name {
			return p.Store
		}
	}
	return nil
}

// Projects lists the folders of recent sessions a list with options o
// shows, most recent first, at most max: the recorded working directory
// (else Project), existing folders only.
func (c *Catalog) Projects(o history.VisibleOpts, max int) ([]Project, error) {
	all, err := c.scan()
	if err != nil {
		return nil, err
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
		if !c.visible(s, o) || dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, Project{Path: dir, Name: filepath.Base(dir), LastActive: s.LastActive})
		if len(out) == max {
			break
		}
	}
	return out, nil
}

// ListableIDs are the sessions a list with options o shows: the visible
// scanned sessions, and sessions created here that have a live run. A
// failed scan leaves only the latter.
func (c *Catalog) ListableIDs(o history.VisibleOpts) []string {
	ids := []string{}
	if all, err := c.Scan(); err == nil {
		for _, s := range all {
			if c.visible(s, o) {
				ids = append(ids, s.ID)
			}
		}
	}
	for id := range c.Created.All() {
		if c.running(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// Searchable reports a search hit the user can open and continue, and its
// provider: not automated (unless aimux created it), and it still has a
// session file or, created here, a live run.
func (c *Catalog) Searchable(id string, automated bool) (string, bool) {
	_, created := c.Created.Get(id)
	if automated && !created {
		return "", false
	}
	if file, store := c.File(id); file != "" {
		return store.Name(), true
	}
	return c.DefaultProvider(), created && c.running(id)
}
