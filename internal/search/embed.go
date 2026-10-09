package search

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Embedder turns text into vectors. Model identifies the vector space, so
// vectors from different models are never compared.
type Embedder interface {
	Model() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

const embeddingsSchema = `
CREATE TABLE IF NOT EXISTS embeddings (
	session_id TEXT, seq INTEGER, model TEXT, vec BLOB, hash TEXT,
	PRIMARY KEY (session_id, seq, model)
);
CREATE TABLE IF NOT EXISTS query_vectors (
	model TEXT, query TEXT, vec BLOB,
	PRIMARY KEY (model, query)
);`

// CachedEmbedder wraps e so single-text embeds (search queries) are stored
// in the index and reused: retyping or backspacing to an earlier query costs
// no API call. Batches pass straight through.
func (ix *Index) CachedEmbedder(e Embedder) Embedder { return &cachedEmbedder{ix: ix, inner: e} }

type cachedEmbedder struct {
	ix    *Index
	inner Embedder
}

func (c *cachedEmbedder) Model() string { return c.inner.Model() }

func (c *cachedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) != 1 {
		return c.inner.Embed(ctx, texts)
	}
	var blob []byte
	if c.ix.db.QueryRowContext(ctx, `SELECT vec FROM query_vectors WHERE model = ? AND query = ?`,
		c.inner.Model(), texts[0]).Scan(&blob) == nil {
		return [][]float32{decodeVec(blob)}, nil
	}
	vecs, err := c.inner.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(vecs) == 1 {
		_, _ = c.ix.db.ExecContext(ctx, `INSERT OR REPLACE INTO query_vectors (model, query, vec) VALUES (?, ?, ?)`,
			c.inner.Model(), texts[0], encodeVec(vecs[0]))
	}
	return vecs, nil
}

// summarySeq marks a session's summary vector (title + all its prompts),
// which lets a query match what a session was about as a whole.
const summarySeq = -1

// pendingEmbedding is one text still to be embedded.
type pendingEmbedding struct {
	sid  string
	seq  int
	text string
	hash string // of text: a vector is redone only when its text changes
}

func textHash(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return strconv.FormatUint(h.Sum64(), 16)
}

// EmbedMissing embeds, for every non-automated session, each chunk's prose and
// one session summary that have no vector for e's model yet, batchSize texts
// per request. Each batch is committed on its own, so an interrupted run keeps
// its progress.
func (ix *Index) EmbedMissing(ctx context.Context, e Embedder, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 64
	}
	todo, err := ix.pendingEmbeddings(ctx, e.Model())
	if err != nil {
		return 0, err
	}
	done := 0
	for start := 0; start < len(todo); start += batchSize {
		batch := todo[start:min(start+batchSize, len(todo))]
		texts := make([]string, len(batch))
		for i, p := range batch {
			texts[i] = p.text
		}
		vecs, err := e.Embed(ctx, texts)
		if err != nil {
			return done, fmt.Errorf("embed batch: %w", err)
		}
		if len(vecs) != len(batch) {
			return done, fmt.Errorf("embedder returned %d vectors for %d inputs", len(vecs), len(batch))
		}
		tx, err := ix.db.BeginTx(ctx, nil)
		if err != nil {
			return done, err
		}
		for i, p := range batch {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO embeddings (session_id, seq, model, vec, hash) VALUES (?, ?, ?, ?, ?)`,
				p.sid, p.seq, e.Model(), encodeVec(normalize(vecs[i])), p.hash); err != nil {
				_ = tx.Rollback()
				return done, err
			}
		}
		if err := tx.Commit(); err != nil {
			return done, err
		}
		done += len(batch)
	}
	return done, nil
}

func (ix *Index) pendingEmbeddings(ctx context.Context, model string) ([]pendingEmbedding, error) {
	have := map[string]string{} // "sid|seq" -> hash of the text that was embedded
	rows, err := ix.db.QueryContext(ctx, `SELECT session_id, seq, hash FROM embeddings WHERE model = ?`, model)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sid string
		var seq int
		var hash sql.NullString
		if err := rows.Scan(&sid, &seq, &hash); err != nil {
			_ = rows.Close()
			return nil, err
		}
		have[sid+"|"+strconv.Itoa(seq)] = hash.String
	}
	_ = rows.Close()

	var todo []pendingEmbedding
	want := func(p pendingEmbedding) {
		p.hash = textHash(p.text)
		if have[p.sid+"|"+strconv.Itoa(p.seq)] != p.hash {
			todo = append(todo, p)
		}
	}

	rows, err = ix.db.QueryContext(ctx, `
		SELECT c.session_id, c.seq, s.title, c.prose, c.text
		FROM chunks c JOIN sessions s ON s.id = c.session_id
		WHERE s.automated = 0`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p pendingEmbedding
		var title, prose, text string
		if err := rows.Scan(&p.sid, &p.seq, &title, &prose, &text); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if prose == "" {
			prose = text
		}
		p.text = title + "\n" + prose
		want(p)
	}
	_ = rows.Close()

	rows, err = ix.db.QueryContext(ctx, `
		SELECT s.id, s.title, s.first_prompt,
		       (SELECT group_concat(prompt, ' | ') FROM chunks c WHERE c.session_id = s.id)
		FROM sessions s WHERE s.automated = 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sid, title, first string
		var prompts sql.NullString
		if err := rows.Scan(&sid, &title, &first, &prompts); err != nil {
			return nil, err
		}
		want(pendingEmbedding{sid: sid, seq: summarySeq, text: truncate(title+"\n"+first+"\n"+prompts.String, 6000)})
	}
	return todo, rows.Err()
}

// PendingEmbeddings counts texts (chunks and session summaries) of
// non-automated sessions with no vector for model.
func (ix *Index) PendingEmbeddings(model string) (int, error) {
	todo, err := ix.pendingEmbeddings(context.Background(), model)
	return len(todo), err
}

// Semantic ranks sessions by cosine similarity between the query and their
// best vector (a chunk or the session summary). It returns nothing (and no
// error) if no vectors are stored.
func (ix *Index) Semantic(ctx context.Context, query string, opts SearchOpts, e Embedder) ([]Result, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	rs, seqs, err := ix.semanticRank(ctx, query, opts, e)
	if err != nil {
		return nil, err
	}
	if len(rs) > opts.Limit {
		rs = rs[:opts.Limit]
	}
	ix.attachSnippets(ctx, rs, seqs)
	return rs, nil
}

// semanticRank scores every stored vector against the query and returns
// sessions best first, without snippets, plus each session's best seq.
func (ix *Index) semanticRank(ctx context.Context, query string, opts SearchOpts, e Embedder) ([]Result, map[string]int, error) {
	if e == nil {
		return nil, nil, errors.New("semantic search needs an embedder")
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil, nil
	}
	qv, err := e.Embed(ctx, []string{query})
	if err != nil {
		return nil, nil, err
	}
	if len(qv) != 1 {
		return nil, nil, fmt.Errorf("embedder returned %d vectors for the query", len(qv))
	}
	q := normalize(qv[0])

	rows, err := ix.db.QueryContext(ctx, `
		SELECT v.session_id, v.seq, v.vec, s.path, s.cwd, s.title, s.automated, s.mtime
		FROM embeddings v JOIN sessions s ON s.id = v.session_id
		WHERE v.model = ? AND (s.automated = 0 OR ?)`, e.Model(), boolInt(opts.IncludeAutomated))
	if err != nil {
		return nil, nil, err
	}
	best := map[string]*Result{}
	bestSeq := map[string]int{}
	for rows.Next() {
		var r Result
		var seq, auto int
		var mtime int64
		var blob []byte
		if err := rows.Scan(&r.SessionID, &seq, &blob, &r.Path, &r.CWD, &r.Title, &auto, &mtime); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		sim := dot(q, decodeVec(blob))
		if cur, ok := best[r.SessionID]; ok && -sim >= cur.Score {
			continue
		}
		r.Score = -sim // lower is better, like BM25
		r.Automated = auto == 1
		r.ModTime = time.Unix(0, mtime)
		best[r.SessionID] = &r
		bestSeq[r.SessionID] = seq
	}
	_ = rows.Close()

	out := make([]Result, 0, len(best))
	for _, r := range best {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score < out[j].Score })
	return out, bestSeq, nil
}

// attachSnippets fills Snippet for rs in two queries: the matching chunk's
// text, or for a summary match, how the session began.
func (ix *Index) attachSnippets(ctx context.Context, rs []Result, seqs map[string]int) {
	if len(rs) == 0 {
		return
	}
	ids := make([]any, len(rs))
	marks := make([]string, len(rs))
	for i, r := range rs {
		ids[i], marks[i] = r.SessionID, "?"
	}
	in := strings.Join(marks, ",")
	texts := map[string]string{}
	// #nosec G202 -- only "?" placeholders are concatenated; ids are bound
	if rows, err := ix.db.QueryContext(ctx, `SELECT session_id, seq, text FROM chunks WHERE session_id IN (`+in+`)`, ids...); err == nil {
		for rows.Next() {
			var id, text string
			var seq int
			if rows.Scan(&id, &seq, &text) == nil && seqs[id] == seq {
				texts[id] = text
			}
		}
		_ = rows.Close()
	}
	// #nosec G202 -- only "?" placeholders are concatenated; ids are bound
	if rows, err := ix.db.QueryContext(ctx, `SELECT id, first_prompt FROM sessions WHERE id IN (`+in+`)`, ids...); err == nil {
		for rows.Next() {
			var id, first string
			if rows.Scan(&id, &first) == nil && seqs[id] == summarySeq {
				texts[id] = first
			}
		}
		_ = rows.Close()
	}
	for i := range rs {
		rs[i].Snippet = oneLine(texts[rs[i].SessionID], 240)
	}
}

// Hybrid fuses keyword (BM25) and semantic rankings with reciprocal rank
// fusion. It degrades to keyword results, with used=false, when there is no
// embedder, the embedder fails, or no vectors are stored yet.
func (ix *Index) Hybrid(ctx context.Context, query string, opts SearchOpts, e Embedder) (rs []Result, used bool, err error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if e == nil || HasPhrase(query) { // quotes mean exact: no semantic neighbours
		rs, err = ix.Search(query, opts)
		return rs, false, err
	}
	wide := opts
	wide.Limit = opts.Limit * 3
	sem, seqs, semErr := ix.semanticRank(ctx, query, wide, e)
	sem = strongSemantic(sem)
	ix.attachSnippets(ctx, sem, seqs)
	if semErr != nil || len(sem) == 0 {
		rs, err = ix.Search(query, opts)
		return rs, false, err
	}
	kw, err := ix.Search(query, wide)
	if err != nil {
		return nil, false, err
	}

	const k = 60.0 // standard RRF damping
	fused := map[string]float64{}
	byID := map[string]Result{}
	for rank, r := range sem {
		fused[r.SessionID] += 1 / (k + float64(rank+1))
		byID[r.SessionID] = r
	}
	for rank, r := range kw {
		fused[r.SessionID] += 1 / (k + float64(rank+1))
		byID[r.SessionID] = r // prefer the keyword snippet: it highlights the match
	}
	for id, score := range fused {
		r := byID[id]
		r.Score = -score
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Score != rs[j].Score {
			return rs[i].Score < rs[j].Score
		}
		return rs[i].ModTime.After(rs[j].ModTime)
	})
	if len(rs) > opts.Limit {
		rs = rs[:opts.Limit]
	}
	return rs, true, nil
}

// Similarity scores for unrelated text sit close together (0.3-0.5 for
// text-embedding-3-small), so no absolute cutoff works. Hybrid keeps only
// semantic hits close to the best one, and only a few of them.
const (
	semanticMargin = 0.08
	semanticMax    = 10
)

// strongSemantic keeps results within semanticMargin of the best similarity.
// Results arrive best first with Score = -similarity.
func strongSemantic(rs []Result) []Result {
	if len(rs) == 0 {
		return rs
	}
	best := -rs[0].Score
	var out []Result
	for _, r := range rs {
		if -r.Score < best-semanticMargin || len(out) == semanticMax {
			break
		}
		out = append(out, r)
	}
	return out
}

// OpenAIEmbedder calls the OpenAI embeddings API.
type OpenAIEmbedder struct {
	APIKey     string
	BaseURL    string // default https://api.openai.com
	ModelName  string // default text-embedding-3-small
	Dimensions int    // 0 = model default; 512 keeps the index small
	HTTPClient *http.Client
}

// NewOpenAIEmbedderFromEnv returns an embedder using OPENAI_API_KEY, or nil
// when the variable is unset so callers can fall back to keyword search.
func NewOpenAIEmbedderFromEnv() *OpenAIEmbedder {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil
	}
	return &OpenAIEmbedder{APIKey: key, Dimensions: 512}
}

// Model identifies the vector space (model and dimensions).
func (o *OpenAIEmbedder) Model() string {
	name := o.ModelName
	if name == "" {
		name = "text-embedding-3-small"
	}
	if o.Dimensions > 0 {
		return fmt.Sprintf("%s@%d", name, o.Dimensions)
	}
	return name
}

// Embed returns one vector per input text, in input order.
func (o *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if o.APIKey == "" {
		return nil, errors.New("openai embeddings: no API key")
	}
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	model := o.ModelName
	if model == "" {
		model = "text-embedding-3-small"
	}
	payload := map[string]interface{}{"model": model, "input": texts}
	if o.Dimensions > 0 {
		payload["dimensions"] = o.Dimensions
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embeddings: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("openai embeddings: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("openai embeddings: decode: %w", err)
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("openai embeddings: index %d out of range", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("openai embeddings: missing vector %d", i)
		}
	}
	return out, nil
}

func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * n
	}
	return out
}

func dot(a, b []float32) float64 {
	n := min(len(a), len(b))
	var s float64
	for i := 0; i < n; i++ {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
