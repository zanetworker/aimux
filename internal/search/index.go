package search

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // pure-Go SQLite with FTS5
)

// Index is a persistent full-text index over session transcripts.
type Index struct {
	db *sql.DB
}

// Result is one matching session, best-scoring chunk first.
type Result struct {
	SessionID string
	Path      string
	CWD       string
	Title     string
	Automated bool
	ModTime   time.Time
	Snippet   string
	Score     float64 // lower is better (BM25)

	matchText string // the matching chunk's text, so a snippet is built only if shown
}

// SearchOpts narrows a search.
type SearchOpts struct {
	Limit            int
	IncludeAutomated bool
	Dir              string // only sessions whose cwd is Dir or below it
}

// dirClause restricts a query joined to sessions as "s" to opts.Dir.
// It returns "" and no args when Dir is empty.
func dirClause(dir string) (string, []any) {
	dir = strings.TrimRight(dir, "/")
	if dir == "" {
		return "", nil
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(dir)
	return ` AND (s.cwd = ? OR s.cwd LIKE ? ESCAPE '\')`, []any{dir, esc + "/%"}
}

// UpdateStats reports what an Update changed.
type UpdateStats struct {
	Indexed int      // sessions (re)read because they were new or changed
	Changed []string // ids of those sessions
	Removed int      // sessions dropped because their file is gone
	Total   int      // sessions in the index afterwards
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY, path TEXT, cwd TEXT, title TEXT, first_prompt TEXT,
	automated INTEGER, mtime INTEGER, size INTEGER
);
CREATE VIRTUAL TABLE IF NOT EXISTS chunks USING fts5(
	session_id UNINDEXED, seq UNINDEXED, prompt UNINDEXED, prose UNINDEXED, title, text, more,
	tokenize = 'unicode61'
);`

// Open opens (creating if needed) the index database at path.
func Open(path string) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { // #nosec G703 -- index path is the user's own config/default
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema + embeddingsSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := rereadIfExtractorChanged(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}

// schemaVersion is bumped whenever the table layout or tokenizer changes; an index with an
// older version is dropped and rebuilt from the session files on next Update.
const schemaVersion = 6

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v == schemaVersion {
		return nil
	}
	// embeddings and query vectors are matched by text hash, so they stay valid
	// across layout changes: keeping them spares a full re-embed.
	for _, t := range []string{"chunks", "sessions", "meta"} {
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return fmt.Errorf("reset index: %w", err)
		}
	}
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// extractVersion is bumped when ExtractFile's output changes without the
// table layout changing. Sessions are then re-read on the next Update while
// embeddings stay: they are matched by text hash, so unchanged prose is not
// re-embedded.
const extractVersion = 2

func rereadIfExtractorChanged(db *sql.DB) error {
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = 'extract_version'`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	want := strconv.Itoa(extractVersion)
	if v == want {
		return nil
	}
	if _, err := db.Exec(`UPDATE sessions SET mtime = 0, size = -1`); err != nil {
		return fmt.Errorf("mark sessions for re-read: %w", err)
	}
	_, err = db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('extract_version', ?)`, want)
	return err
}

// Close releases the database.
func (ix *Index) Close() error { return ix.db.Close() }

// DefaultPath is ~/.aimux/search.db.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aimux", "search.db")
}

type fileState struct {
	mtime, size int64
}

// Update brings the index in line with the session files under projectsDir
// (layout: <projectsDir>/<project>/<session-id>.jsonl). Only new or changed
// files are re-read; sessions whose file is gone are removed.
func (ix *Index) Update(projectsDir string, opts ExtractOpts) (UpdateStats, error) {
	var st UpdateStats
	files, err := filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
	if err != nil {
		return st, err
	}

	known := map[string]fileState{}
	rows, err := ix.db.Query(`SELECT id, mtime, size FROM sessions`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var id string
		var fs fileState
		if err := rows.Scan(&id, &fs.mtime, &fs.size); err != nil {
			_ = rows.Close()
			return st, err
		}
		known[id] = fs
	}
	_ = rows.Close()

	seen := map[string]bool{}
	var changed []string
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		seen[id] = true
		if fs, ok := known[id]; ok && fs.mtime == info.ModTime().UnixNano() && fs.size == info.Size() {
			continue
		}
		changed = append(changed, f)
	}

	docs := extractAll(changed, opts)

	tx, err := ix.db.Begin()
	if err != nil {
		return st, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, d := range docs {
		if err := writeDoc(tx, d); err != nil {
			return st, err
		}
		st.Indexed++
		st.Changed = append(st.Changed, d.SessionID)
	}
	for id := range known {
		if seen[id] {
			continue
		}
		if err := deleteSession(tx, id); err != nil {
			return st, err
		}
		st.Removed++
	}
	if err := tx.Commit(); err != nil {
		return st, err
	}
	err = ix.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&st.Total)
	return st, err
}

// extractAll parses files in parallel; unreadable files are skipped.
func extractAll(files []string, opts ExtractOpts) []Doc {
	out := make([]Doc, len(files))
	ok := make([]bool, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f string) {
			defer func() { <-sem; wg.Done() }()
			if d, err := ExtractFile(f, opts); err == nil {
				out[i], ok[i] = d, true
			}
		}(i, f)
	}
	wg.Wait()
	var docs []Doc
	for i := range out {
		if ok[i] {
			docs = append(docs, out[i])
		}
	}
	return docs
}

func writeDoc(tx *sql.Tx, d Doc) error {
	info, err := os.Stat(d.Path)
	if err != nil {
		return nil // vanished between extract and write; next Update removes it
	}
	// keep the session's vectors: they are matched to chunk text by hash, so a
	// growing session only re-embeds what changed. Drop vectors for chunks
	// that no longer exist.
	if _, err := tx.Exec(`DELETE FROM chunks WHERE session_id = ?`, d.SessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM embeddings WHERE session_id = ? AND seq >= ?`, d.SessionID, len(d.Chunks)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO sessions (id, path, cwd, title, first_prompt, automated, mtime, size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.SessionID, d.Path, d.CWD, d.Title, d.FirstPrompt, boolInt(d.Automated), info.ModTime().UnixNano(), info.Size()); err != nil {
		return err
	}
	for _, c := range d.Chunks {
		if _, err := tx.Exec(`INSERT INTO chunks (session_id, seq, prompt, prose, title, text, more) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			d.SessionID, c.Seq, c.Prompt, c.Prose, d.Title, c.Text, c.More); err != nil {
			return err
		}
	}
	return nil
}

// deleteSession drops a session's row, chunks and vectors (its file is gone).
func deleteSession(tx *sql.Tx, id string) error {
	for _, q := range []string{
		`DELETE FROM chunks WHERE session_id = ?`,
		`DELETE FROM embeddings WHERE session_id = ?`,
		`DELETE FROM sessions WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

// overflowWeight is the BM25 weight of a long exchange's text past its first
// MaxChunkChars (title 10, main text 1). Measured: indexing overflow at full
// weight let long, tool-heavy exchanges outrank precise matches.
var overflowWeight = 0.1

func overflowWeightSQL() string { return strconv.FormatFloat(overflowWeight, 'f', 2, 64) }

// AnyTermCap bounds the any-term fallback: once nothing covers enough of the
// query, a loose match is a guess, and a long list of guesses buries the point.
const AnyTermCap = 20

// candidateChunks is how many BM25-ranked chunks are examined per query.
// Reading candidates dominates query time; measured on real sessions, 200
// gave the same ranking quality as 600 at half the latency (90ms vs 179ms).
var candidateChunks = 200

// minCoverage is how many query terms a chunk must contain: all of them for
// one- or two-word queries, about two thirds for longer, natural-language
// ones, where people rarely remember their exact wording.
func minCoverage(n int) int {
	if n <= 2 {
		return n
	}
	return (2*n + 2) / 3
}

// Search returns sessions matching query, best first, ranked by the BM25
// score of their best chunk. Text in double quotes must appear as an exact
// phrase (an unclosed quote is a phrase up to the end, its last word a
// prefix, so it works while typing). Free words: chunks covering fewer than
// minCoverage of them are dropped; if no session qualifies, up to AnyTermCap
// sessions matching any word are returned. Titles naming the query are lifted
// to the top (tierByTitle).
func (ix *Index) Search(query string, opts SearchOpts) ([]Result, error) {
	phrases, rest := parsePhrases(query)
	terms := queryTerms(rest)
	if len(terms) == 0 && len(phrases) == 0 {
		return nil, nil
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	fts := strings.Join(terms, " OR ")
	if len(phrases) > 0 {
		fts = strings.Join(phrases, " AND ")
		if len(terms) > 0 {
			fts += " AND (" + strings.Join(terms, " OR ") + ")"
		}
	}
	prefixes := termPrefixes(terms)
	rs, err := ix.coverageMatch(fts, prefixes, minCoverage(len(terms)), opts)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 && len(terms) > 1 {
		if rs, err = ix.coverageMatch(fts, prefixes, 1, opts); err != nil {
			return nil, err
		}
		opts.Limit = min(opts.Limit, AnyTermCap)
	}
	rs = tierByTitle(rs, query, append(terms, queryTerms(strings.Join(phraseWords(phrases), " "))...))
	if len(rs) > opts.Limit {
		rs = rs[:opts.Limit]
	}
	// Highlighting is costly in FTS5 (most of a query's time when done for
	// every candidate), so only the results returned get snippets, built from
	// text the query already read.
	marks := termPrefixes(append(terms, queryTerms(strings.Join(phraseWords(phrases), " "))...))
	for i := range rs {
		rs[i].Snippet = oneLine(makeSnippet(rs[i].matchText, marks, 14), 240)
		rs[i].matchText = ""
	}
	return rs, nil
}

var quoted = regexp.MustCompile(`"([^"]*)("|$)`)

// parsePhrases pulls "quoted text" out of q as FTS5 phrase expressions and
// returns them with the remaining free text.
func parsePhrases(q string) (phrases []string, rest string) {
	rest = quoted.ReplaceAllStringFunc(q, func(m string) string {
		sub := quoted.FindStringSubmatch(m)
		words := normalizeWords(sub[1])
		if words == "" {
			return " "
		}
		p := `"` + words + `"`
		if sub[2] == "" { // unclosed: still typing, last word is a prefix
			p += "*"
		}
		phrases = append(phrases, p)
		return " "
	})
	return phrases, rest
}

// HasPhrase reports whether q asks for an exact phrase.
func HasPhrase(q string) bool { return strings.Contains(q, `"`) }

func phraseWords(phrases []string) []string {
	out := make([]string, len(phrases))
	for i, p := range phrases {
		out[i] = strings.Trim(p, `"*`)
	}
	return out
}

type scored struct {
	Result
	coverage int
}

func (ix *Index) coverageMatch(fts string, plain []string, need int, opts SearchOpts) ([]Result, error) {
	dirSQL, dirArgs := dirClause(opts.Dir)
	// #nosec G202 -- dirSQL is a fixed clause; values are bound
	rows, err := ix.db.Query(`
		SELECT c.session_id, bm25(chunks, 0, 0, 0, 0, 10.0, 1.0, `+overflowWeightSQL()+`) AS score,
		       c.title, c.text, c.more,
		       s.path, s.cwd, s.automated, s.mtime
		FROM chunks c JOIN sessions s ON s.id = c.session_id
		WHERE chunks MATCH ? AND (s.automated = 0 OR ?)`+dirSQL+`
		ORDER BY score LIMIT ?`, append(append([]any{fts, boolInt(opts.IncludeAutomated)}, dirArgs...), candidateChunks)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	best := map[string]*scored{}
	var order []string
	for rows.Next() {
		var r Result
		var text, more string
		var auto int
		var mtime int64
		if err := rows.Scan(&r.SessionID, &r.Score, &r.Title, &text, &more, &r.Path, &r.CWD, &auto, &mtime); err != nil {
			return nil, err
		}
		// The main text must cover enough of the query. Overflow (the rest
		// of a long exchange) only qualifies a chunk when it completes every
		// term: deep text stays findable without letting loose matches in.
		cov := coverage(plain, r.Title+" "+text)
		r.matchText = text
		if cov < need {
			if more == "" || coverage(plain, r.Title+" "+text+" "+more) < len(plain) {
				continue
			}
			r.matchText = more // qualified by its overflow: show that part
		}
		cur, seen := best[r.SessionID]
		if seen && r.Score >= cur.Score {
			continue
		}
		r.Automated = auto == 1
		r.ModTime = time.Unix(0, mtime)
		if !seen {
			order = append(order, r.SessionID)
		}
		best[r.SessionID] = &scored{Result: r, coverage: cov}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]scored, 0, len(order))
	for _, id := range order {
		out = append(out, *best[id])
	}
	// coverage only filters; BM25 ranks, since it already weighs rare terms
	// above common ones ("swimming" over "old")
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score < out[j].Score })
	rs := make([]Result, len(out))
	for i := range out {
		rs[i] = out[i].Result
	}
	return rs, nil
}

// termPrefixes strips the FTS5 quoting from queryTerms output.
func termPrefixes(terms []string) []string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(t, `"`), `"*`))
	}
	return out
}

// coverage counts the terms that prefix at least one word of text.
func coverage(prefixes []string, text string) int {
	words := strings.Fields(normalizeWords(text))
	n := 0
	for _, p := range prefixes {
		for _, w := range words {
			if strings.HasPrefix(w, p) {
				n++
				break
			}
		}
	}
	return n
}

// tierByTitle orders results so typing a session's name finds it: first a
// title equal to the query (ignoring case and punctuation), then titles that
// contain every term, then the rest; BM25 order is kept within each tier.
func tierByTitle(rs []Result, query string, terms []string) []Result {
	q := normalizeWords(query)
	prefixes := termPrefixes(terms)
	tier := func(r Result) int {
		t := normalizeWords(r.Title)
		switch {
		case t == q:
			return 0
		case coverage(prefixes, t) == len(prefixes):
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(rs, func(i, j int) bool { return tier(rs[i]) < tier(rs[j]) })
	return rs
}

// normalizeWords lowercases s and reduces it to space-separated letter/digit runs.
func normalizeWords(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}), " ")
}

// stopwords are dropped from queries: as prefix terms they match nearly every
// chunk, which floods the any-term fallback and the snippet highlights.
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"did": true, "do": true, "for": true, "from": true, "how": true, "i": true, "in": true, "is": true,
	"it": true, "me": true, "my": true, "of": true, "on": true, "or": true, "our": true, "that": true,
	"the": true, "they": true, "this": true, "to": true, "was": true, "we": true, "what": true,
	"when": true, "where": true, "which": true, "who": true, "why": true, "with": true, "you": true,
}

// queryTerms turns free text into quoted FTS5 prefix terms, so user input can
// never be parsed as FTS5 syntax. Single characters and stopwords are dropped
// (stopwords are kept only when the query has nothing else).
func queryTerms(q string) []string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	var terms, stops []string
	for _, w := range words {
		if len([]rune(w)) < 2 {
			continue
		}
		t := `"` + w + `"*`
		if stopwords[strings.ToLower(w)] {
			stops = append(stops, t)
			continue
		}
		terms = append(terms, t)
	}
	if len(terms) == 0 {
		return stops
	}
	return terms
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
