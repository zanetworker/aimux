package search

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Ranking modes for Service.Query.
const (
	ModeHybrid   = "hybrid"   // keyword + semantic (falls back to keyword without an embedder)
	ModeKeyword  = "keyword"  // BM25 only
	ModeSemantic = "semantic" // embeddings only
)

// Modes lists the valid ranking modes.
var Modes = []string{ModeHybrid, ModeKeyword, ModeSemantic}

// embedInlineMax is the most texts a query embeds on the spot; larger
// backlogs are left to an explicit index run so a search stays fast.
const embedInlineMax = 64

// embedInlineSessions is the most changed sessions a query embeds inline.
const embedInlineSessions = 20

// Service is the one entry point every frontend (CLI, TUI, web) uses to
// search sessions: it refreshes the index, then ranks.
type Service struct {
	DBPath      string
	ProjectsDir string
	ArchiveDirs []string  // also indexed; sessions here outlive Claude Code's cleanup
	Embedder    Embedder  // nil: keyword only
	Notes       io.Writer // explanations of degraded modes; nil discards
	// EmbedInBackground queues changed sessions for embedding instead of
	// waiting on the embedding API inside a query. For long-lived frontends
	// (TUI, web, MCP); one-shot CLI runs exit before background work ends.
	EmbedInBackground bool

	mu sync.Mutex // one call at a time: concurrent refreshes lock SQLite
	bg struct {
		mu      sync.Mutex
		pending map[string]bool // session IDs waiting for embedding
		running bool
		wg      sync.WaitGroup
	}
}

// QueryOpts narrows a Service query.
type QueryOpts struct {
	Mode             string // "" means hybrid
	Limit            int
	IncludeAutomated bool
	Dir              string   // only sessions under this working directory
	IDs              []string // only these sessions; nil = no restriction, empty = none
	NoRefresh        bool     // skip the file scan; the caller keeps the index fresh
}

// DefaultService searches ~/.aimux/search.db over ~/.claude/projects and
// ~/.aimux/archive, with OpenAI embeddings when OPENAI_API_KEY is set.
func DefaultService(notes io.Writer) *Service {
	home, _ := os.UserHomeDir()
	s := &Service{DBPath: DefaultPath(), ProjectsDir: filepath.Join(home, ".claude", "projects"),
		ArchiveDirs: []string{DefaultArchiveDir()}, Notes: notes}
	if e := NewOpenAIEmbedderFromEnv(); e != nil { // keep a nil *OpenAIEmbedder out of the interface
		s.Embedder = e
	}
	return s
}

// Query refreshes the index (only changed files are re-read) and returns
// sessions best first, and whether semantic ranking contributed.
func (s *Service) Query(ctx context.Context, query string, o QueryOpts) ([]Result, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode := o.Mode
	if mode == "" {
		mode = ModeHybrid
	}
	if mode != ModeHybrid && mode != ModeKeyword && mode != ModeSemantic {
		return nil, false, fmt.Errorf("invalid mode %q: valid values are %s", mode, strings.Join(Modes, ", "))
	}
	if strings.TrimSpace(query) == "" {
		return nil, false, nil
	}
	ix, err := Open(s.DBPath)
	if err != nil {
		return nil, false, fmt.Errorf("open search index: %w", err)
	}
	defer func() { _ = ix.Close() }()
	if !o.NoRefresh {
		if err := s.refresh(ctx, ix); err != nil {
			return nil, false, err
		}
	}
	opts := SearchOpts{Limit: o.Limit, IncludeAutomated: o.IncludeAutomated, Dir: o.Dir, IDs: o.IDs}

	if mode == ModeKeyword {
		rs, err := ix.Search(query, opts)
		return rs, false, err
	}
	if s.Embedder == nil {
		if mode == ModeSemantic {
			return nil, false, fmt.Errorf("semantic search needs OPENAI_API_KEY; use keyword mode")
		}
		s.note("note: keyword ranking only (set OPENAI_API_KEY for semantic ranking)")
		rs, err := ix.Search(query, opts)
		return rs, false, err
	}
	e := ix.CachedEmbedder(s.Embedder)
	if mode == ModeSemantic {
		rs, err := ix.Semantic(ctx, query, opts, e)
		return rs, err == nil, err
	}
	return ix.Hybrid(ctx, query, opts, e)
}

// Refresh brings the index up to date and embeds what changed, without
// querying. The picker runs it in the background so hybrid ranking sees new
// sessions too.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := Open(s.DBPath)
	if err != nil {
		return fmt.Errorf("open search index: %w", err)
	}
	defer func() { _ = ix.Close() }()
	return s.refresh(ctx, ix)
}

// Transcript is one session's preview data plus every exchange in order.
type Transcript struct {
	Detail    Detail
	Exchanges []Exchange
}

// Session refreshes the index, resolves an ID or unique prefix, and returns
// the whole session. Resolve errors come back unwrapped, so callers can
// errors.As them into *AmbiguousError.
func (s *Service) Session(ctx context.Context, idOrPrefix string) (Transcript, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := Open(s.DBPath)
	if err != nil {
		return Transcript{}, fmt.Errorf("open search index: %w", err)
	}
	defer func() { _ = ix.Close() }()
	if err := s.refresh(ctx, ix); err != nil {
		return Transcript{}, err
	}
	id, err := ix.Resolve(idOrPrefix)
	if err != nil {
		return Transcript{}, err
	}
	d, err := ix.Detail(id, 0)
	if err != nil {
		return Transcript{}, err
	}
	ex, err := ix.Exchanges(id)
	if err != nil {
		return Transcript{}, err
	}
	return Transcript{Detail: d, Exchanges: ex}, nil
}

// Recent refreshes the index and lists sessions by last activity.
func (s *Service) Recent(ctx context.Context, o SearchOpts) ([]Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := Open(s.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open search index: %w", err)
	}
	defer func() { _ = ix.Close() }()
	if err := s.refresh(ctx, ix); err != nil {
		return nil, err
	}
	return ix.Recent(o)
}

// refresh re-reads changed session files and, with an embedder, embeds only
// those sessions: checking the whole index for missing vectors costs seconds.
// Big backlogs (a first run) are left to `aimux sessions index`.
func (s *Service) refresh(ctx context.Context, ix *Index) error {
	st, err := ix.Update(s.ProjectsDir, DefaultExtractOpts(), s.ArchiveDirs...)
	if err != nil {
		return fmt.Errorf("update search index: %w", err)
	}
	if s.Embedder == nil || len(st.Changed) == 0 {
		return nil
	}
	if len(st.Changed) > embedInlineSessions {
		s.note(fmt.Sprintf("note: %d sessions changed; run `aimux sessions index` to embed them for semantic ranking", len(st.Changed)))
		return nil
	}
	if s.EmbedInBackground {
		s.queueEmbed(st.Changed)
		return nil
	}
	_, _ = ix.EmbedMissingFor(ctx, s.Embedder, st.Changed, embedInlineMax)
	return nil
}

// queueEmbed hands changed sessions to a single background worker; sessions
// queued while it runs are picked up in its next round.
func (s *Service) queueEmbed(ids []string) {
	s.bg.mu.Lock()
	defer s.bg.mu.Unlock()
	if s.bg.pending == nil {
		s.bg.pending = map[string]bool{}
	}
	for _, id := range ids {
		s.bg.pending[id] = true
	}
	if s.bg.running {
		return
	}
	s.bg.running = true
	s.bg.wg.Add(1)
	go s.embedWorker()
}

func (s *Service) embedWorker() {
	defer s.bg.wg.Done()
	for {
		s.bg.mu.Lock()
		if len(s.bg.pending) == 0 {
			s.bg.running = false
			s.bg.mu.Unlock()
			return
		}
		ids := make([]string, 0, len(s.bg.pending))
		for id := range s.bg.pending {
			ids = append(ids, id)
		}
		s.bg.pending = map[string]bool{}
		s.bg.mu.Unlock()

		// its own connection, outside s.mu, so queries never wait on the API;
		// SQLite's busy timeout serializes the short write transactions
		ix, err := Open(s.DBPath)
		if err != nil {
			s.note(fmt.Sprintf("note: background embedding: %v", err))
			continue
		}
		if _, err := ix.EmbedMissingFor(context.Background(), s.Embedder, ids, embedInlineMax); err != nil {
			s.note(fmt.Sprintf("note: background embedding: %v", err))
		}
		_ = ix.Close()
	}
}

// Wait blocks until background embedding has finished.
func (s *Service) Wait() { s.bg.wg.Wait() }

func (s *Service) note(msg string) {
	if s.Notes != nil {
		_, _ = fmt.Fprintln(s.Notes, msg)
	}
}
