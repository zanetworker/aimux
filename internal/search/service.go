package search

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	Embedder    Embedder  // nil: keyword only
	Notes       io.Writer // explanations of degraded modes; nil discards
}

// QueryOpts narrows a Service query.
type QueryOpts struct {
	Mode             string // "" means hybrid
	Limit            int
	IncludeAutomated bool
}

// DefaultService searches ~/.aimux/search.db over ~/.claude/projects, with
// OpenAI embeddings when OPENAI_API_KEY is set.
func DefaultService(notes io.Writer) *Service {
	home, _ := os.UserHomeDir()
	s := &Service{DBPath: DefaultPath(), ProjectsDir: filepath.Join(home, ".claude", "projects"), Notes: notes}
	if e := NewOpenAIEmbedderFromEnv(); e != nil { // keep a nil *OpenAIEmbedder out of the interface
		s.Embedder = e
	}
	return s
}

// Query refreshes the index (only changed files are re-read) and returns
// sessions best first, and whether semantic ranking contributed.
func (s *Service) Query(ctx context.Context, query string, o QueryOpts) ([]Result, bool, error) {
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
	st, err := ix.Update(s.ProjectsDir, DefaultExtractOpts())
	if err != nil {
		return nil, false, fmt.Errorf("update search index: %w", err)
	}
	opts := SearchOpts{Limit: o.Limit, IncludeAutomated: o.IncludeAutomated}

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
	// Embed only what this refresh changed; checking the whole index for
	// missing vectors costs seconds per query. Big backlogs (a first run) are
	// left to `aimux sessions index`.
	if len(st.Changed) > 0 && len(st.Changed) <= embedInlineSessions {
		_, _ = ix.EmbedMissingFor(ctx, s.Embedder, st.Changed, embedInlineMax)
	} else if len(st.Changed) > embedInlineSessions {
		s.note(fmt.Sprintf("note: %d sessions changed; run `aimux sessions index` to embed them for semantic ranking", len(st.Changed)))
	}
	e := ix.CachedEmbedder(s.Embedder)
	if mode == ModeSemantic {
		rs, err := ix.Semantic(ctx, query, opts, e)
		return rs, err == nil, err
	}
	return ix.Hybrid(ctx, query, opts, e)
}

func (s *Service) note(msg string) {
	if s.Notes != nil {
		_, _ = fmt.Fprintln(s.Notes, msg)
	}
}
