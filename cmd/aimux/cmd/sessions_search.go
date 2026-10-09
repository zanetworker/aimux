package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/jump"
	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/sessions"
)

// sessionsIndexQuery is what `aimux sessions <query>` asks the index for.
type sessionsIndexQuery struct {
	Mode             string
	Limit            int
	IncludeAutomated bool
}

// sessionsSearchDeps are optional; with Index unset, `sessions <query>` keeps
// the older title-then-ripgrep search.
type sessionsSearchDeps struct {
	Index     func(query string, q sessionsIndexQuery) (rs []search.Result, semantic bool, err error)
	FocusLive func(sessionID string) (where string, ok bool) // focus the session's live pane; ok=false if not live
	LiveIDs   func() []string                                // sessions running in a terminal now (picker ● marker)
}

// sessionsSearch is set by RegisterAll.
var sessionsSearch sessionsSearchDeps

func validMode(m string) error {
	for _, v := range search.Modes {
		if m == v {
			return nil
		}
	}
	return fmt.Errorf("invalid --mode %q: valid values are %s", m, strings.Join(search.Modes, ", "))
}

func claudeProjectsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// envEmbedder returns the OpenAI embedder, or a nil interface when no key is
// set (a nil *OpenAIEmbedder must not leak into a non-nil interface).
func envEmbedder() search.Embedder {
	if e := search.NewOpenAIEmbedderFromEnv(); e != nil {
		return e
	}
	return nil
}

// runIndexedQuery prints index results, or opens the picked one.
func runIndexedQuery(cmd *cobra.Command, query string, q sessionsIndexQuery, dir string, listMode, danger bool,
	picker sessionsPickerFn, resume sessionsResumeFn) error {
	rs, semantic, err := sessionsSearch.Index(query, q)
	if err != nil {
		return err
	}
	if dir != "" {
		var scoped []search.Result
		for _, r := range rs {
			if r.CWD == dir || strings.HasPrefix(r.CWD, strings.TrimRight(dir, "/")+"/") {
				scoped = append(scoped, r)
			}
		}
		rs = scoped
	}
	if len(rs) == 0 {
		return fmt.Errorf("no sessions matching %q", query)
	}

	if listMode || !IsInteractive() || picker == nil {
		return printSearchResults(cmd.OutOrStdout(), rs, semantic)
	}
	selected, err := picker(resultsAsSessions(rs))
	if err != nil {
		return err
	}
	if msg := openSessionGuarded(selected.ID, selected.Title, selected.LastActive, danger, sessionsSearch.FocusLive, resume); msg != "" {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), msg)
	}
	return nil
}

// activeWindow: a session written to more recently than this is probably open
// in a terminal that live discovery could not identify.
const activeWindow = 2 * time.Minute

// openSessionGuarded is openSession, except that it will not resume a session
// that looks open elsewhere: resuming it would put two processes on one
// session file. `aimux resume <id>` remains the explicit override.
func openSessionGuarded(id, title string, modTime time.Time, danger bool, focus func(string) (string, bool), resume sessionsResumeFn) string {
	if focus != nil {
		if where, ok := focus(id); ok {
			return jumpedMsg(id, title, where)
		}
	}
	if age := time.Since(modTime); !modTime.IsZero() && age < activeWindow {
		if title == "" {
			title = id
		}
		return fmt.Sprintf("%s was active %ds ago, probably in a terminal I can't focus; not resuming a second copy.\nIf it really is closed: aimux resume %s",
			title, int(age.Seconds()), id)
	}
	return openSession(id, title, danger, nil, resume)
}

func jumpedMsg(id, title, where string) string {
	if title == "" {
		title = id
	}
	msg := "→ Jumped to " + title
	if where != "" {
		msg += " (" + where + ")"
	}
	return msg
}

// openSession focuses the session's live pane if it has one, else resumes it.
func openSession(id, title string, danger bool, focus func(string) (string, bool), resume sessionsResumeFn) string {
	if focus != nil {
		if where, ok := focus(id); ok {
			return jumpedMsg(id, title, where)
		}
	}
	if resume != nil {
		resume(id, danger)
	}
	return ""
}

type searchResultJSON struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Project    string `json:"project"`
	FilePath   string `json:"file_path"`
	LastActive string `json:"last_active"`
	Match      string `json:"match"`
	Automated  bool   `json:"automated,omitempty"`
}

func printSearchResults(w io.Writer, rs []search.Result, semantic bool) error {
	if jsonOutput {
		out := make([]searchResultJSON, len(rs))
		for i, r := range rs {
			out[i] = searchResultJSON{ID: r.SessionID, Title: r.Title, Project: r.CWD, FilePath: r.Path,
				LastActive: r.ModTime.Format("2006-01-02T15:04:05Z07:00"), Match: r.Snippet, Automated: r.Automated}
		}
		b, err := json.MarshalIndent(map[string]any{"results": out, "count": len(out), "semantic": semantic}, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	_, _ = fmt.Fprintln(w, "ID\tPROJECT\tAGE\tTITLE\tMATCH")
	for _, r := range rs {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.SessionID, shortProject(r.CWD), shortAgeFmt(r.ModTime),
			clip(r.Title, 40), clip(r.Snippet, 90))
	}
	return nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// resultsAsSessions adapts index hits for the session picker; the match
// snippet is shown where the first prompt usually is.
func resultsAsSessions(rs []search.Result) []history.Session {
	out := make([]history.Session, len(rs))
	for i, r := range rs {
		out[i] = history.Session{ID: r.SessionID, Provider: "claude", Project: r.CWD, FilePath: r.Path,
			LastActive: r.ModTime, Title: r.Title, FirstPrompt: r.Snippet, Resumable: true}
	}
	return out
}

// DefaultIndexSearch runs a query through the shared search service.
func DefaultIndexSearch(query string, q sessionsIndexQuery) ([]search.Result, bool, error) {
	svc := search.DefaultService(os.Stderr)
	svc.DBPath = searchDBPath()
	return svc.Query(context.Background(), query, search.QueryOpts{Mode: q.Mode, Limit: q.Limit, IncludeAutomated: q.IncludeAutomated})
}

// LiveIDsVia lists session ids of agents running in a terminal right now.
func LiveIDsVia(discover func() ([]agent.Agent, error)) func() []string {
	return func() []string {
		agents, err := discover()
		if err != nil {
			return nil
		}
		var ids []string
		for _, a := range agents {
			if a.SessionID != "" {
				ids = append(ids, a.SessionID)
			}
		}
		return ids
	}
}

// FocusLiveVia returns a FocusLive func: it finds the live agent running the
// session and brings its iTerm2 pane to the front.
func FocusLiveVia(discover func() ([]agent.Agent, error)) func(string) (string, bool) {
	return func(sessionID string) (string, bool) {
		agents, err := discover()
		if err != nil {
			return "", false
		}
		for _, a := range agents {
			if a.SessionID != sessionID {
				continue
			}
			if tty := jump.TTYForPID(a.PID); tty != "" {
				where, ok, _ := jump.ITerm2FocusLocate(tty)
				return where, ok
			}
		}
		return "", false
	}
}

func newSessionsIndexCmd() *cobra.Command {
	var noEmbed bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build or refresh the session search index",
		Long:  "Re-reads new or changed session files into ~/.aimux/search.db and, when OPENAI_API_KEY is set, embeds new exchanges for semantic ranking.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ix, err := search.Open(searchDBPath())
			if err != nil {
				return err
			}
			defer func() { _ = ix.Close() }()
			st, err := ix.Update(claudeProjectsDir(), search.DefaultExtractOpts())
			if err != nil {
				return err
			}
			embedded, pending := 0, 0
			e := envEmbedder()
			if e != nil && !noEmbed {
				if embedded, err = ix.EmbedMissing(context.Background(), e, 64); err != nil {
					return fmt.Errorf("embedded %d chunks before failing: %w", embedded, err)
				}
			}
			if e != nil {
				pending, _ = ix.PendingEmbeddings(e.Model())
			}
			res := map[string]any{"indexed": st.Indexed, "removed": st.Removed, "total": st.Total,
				"embedded": embedded, "pending_embeddings": pending, "semantic": e != nil, "db": searchDBPath()}
			if jsonOutput {
				b, _ := json.MarshalIndent(res, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Indexed %d changed sessions (%d removed, %d total). Embedded %d exchanges.\n",
				st.Indexed, st.Removed, st.Total, embedded)
			if e == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Semantic ranking off: set OPENAI_API_KEY to enable it.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noEmbed, "no-embed", false, "Refresh the full-text index only")
	return cmd
}

// searchDBPath is swapped in tests.
var searchDBPath = search.DefaultPath

// runSearchPicker opens the split-view picker (fzf), then focuses or resumes
// the chosen session. It refreshes the index once up front; keystrokes then
// query the index without re-scanning session files.
func runSearchPicker(cmd *cobra.Command, query string, danger bool, resume sessionsResumeFn) error {
	dir, err := os.MkdirTemp("", "aimux-picker-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	st := sessions.PickerState{Dir: dir}
	if envEmbedder() != nil {
		// semantic+keyword ranks best when embeddings are available;
		// ^s switches to plain keyword
		_ = st.SetMode("hybrid")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// Open on the cached index at once; refresh and live discovery (~3s) run
	// in the background and reload the open picker when done.
	background := func() {
		if ix, err := search.Open(searchDBPath()); err == nil {
			_, _ = ix.Update(claudeProjectsDir(), search.DefaultExtractOpts())
			_ = ix.Close()
		}
		if sessionsSearch.LiveIDs != nil {
			_ = st.SetLive(sessionsSearch.LiveIDs())
		}
	}
	id, err := sessions.SearchPick(self, query, st, background)
	if err != nil {
		return quietCancel(err)
	}
	title, modTime := "", time.Time{}
	if ix, err := search.Open(searchDBPath()); err == nil {
		if d, err := ix.Detail(id, 0); err == nil {
			title, modTime = d.Title, d.ModTime
		}
		_ = ix.Close()
	}
	// the index may be a few seconds stale; the file's own mtime is current
	if d, err := os.Stat(sessionFilePath(id)); err == nil {
		modTime = d.ModTime()
	}
	if msg := openSessionGuarded(id, title, modTime, danger, sessionsSearch.FocusLive, resume); msg != "" {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), msg)
	}
	return nil
}

// quietCancel treats leaving the picker without a choice as success.
func quietCancel(err error) error {
	if errors.Is(err, sessions.ErrCancelled) {
		return nil
	}
	return err
}

// RunPickerHelper runs the commands fzf calls on every keystroke
// (sessions rows/preview/picker-toggle) without aimux's full startup.
// It reports false for anything else so main continues normally.
func RunPickerHelper(args []string, out io.Writer) (bool, error) {
	if len(args) < 2 || args[0] != "sessions" {
		return false, nil
	}
	var c *cobra.Command
	switch args[1] {
	case "rows":
		c = newSessionsRowsCmd()
	case "preview":
		c = newSessionsPreviewCmd()
	case "picker-toggle":
		c = newSessionsPickerToggleCmd()
	default:
		return false, nil
	}
	c.SetOut(out)
	c.SetErr(io.Discard)
	c.SetArgs(args[2:])
	c.SilenceUsage, c.SilenceErrors = true, true
	return true, c.Execute()
}

// sessionFilePath finds <projects>/<any project>/<id>.jsonl, or "".
func sessionFilePath(id string) string {
	m, _ := filepath.Glob(filepath.Join(claudeProjectsDir(), "*", id+".jsonl"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func pickerArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.TrimSpace(args[0])
}

// newSessionsRowsCmd prints picker rows: recent sessions for an empty query,
// ranked matches otherwise. Called by fzf on every keystroke.
func newSessionsRowsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "rows [query]", Hidden: true, Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := sessions.PickerStateFromEnv()
			ix, err := search.Open(searchDBPath())
			if err != nil {
				return err
			}
			defer func() { _ = ix.Close() }()
			opts := search.SearchOpts{Limit: 200, IncludeAutomated: st.IncludeAutomated()}
			q := pickerArg(args)
			var rs []search.Result
			switch {
			case q == "":
				rs, err = ix.Recent(opts)
			case st.Mode() == "hybrid":
				var e search.Embedder
				if inner := envEmbedder(); inner != nil {
					e = ix.CachedEmbedder(inner)
				}
				rs, _, err = ix.Hybrid(context.Background(), q, search.SearchOpts{Limit: 50, IncludeAutomated: opts.IncludeAutomated}, e)
			default:
				opts.Limit = 100
				rs, err = ix.Search(q, opts)
			}
			if err != nil {
				return err
			}
			live := st.Live()
			for _, r := range rs {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), sessions.FormatRow(r, live[r.SessionID]))
			}
			return nil
		},
	}
}

// newSessionsPreviewCmd renders the picker's preview pane for one session.
func newSessionsPreviewCmd() *cobra.Command {
	return &cobra.Command{
		Use: "preview <id> [query]", Hidden: true, Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ix, err := search.Open(searchDBPath())
			if err != nil {
				return err
			}
			defer func() { _ = ix.Close() }()
			d, err := ix.Detail(args[0], 4)
			if err != nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), err.Error())
				return nil
			}
			snippet := ""
			if q := pickerArg(args[1:]); q != "" {
				if rs, err := ix.Search(q, search.SearchOpts{Limit: 300, IncludeAutomated: true}); err == nil {
					for _, r := range rs {
						if r.SessionID == d.SessionID {
							snippet = r.Snippet
							break
						}
					}
				}
			}
			_, _ = fmt.Fprint(cmd.OutOrStdout(), sessions.FormatDetail(d, snippet, sessions.PickerStateFromEnv().Live()[d.SessionID]))
			return nil
		},
	}
}

// newSessionsPickerToggleCmd flips a picker toggle and prints the new header.
func newSessionsPickerToggleCmd() *cobra.Command {
	return &cobra.Command{
		Use: "picker-toggle <automated|semantic>", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st := sessions.PickerStateFromEnv()
			if err := st.Toggle(args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), st.Header())
			return nil
		},
	}
}
