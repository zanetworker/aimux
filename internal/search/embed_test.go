package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// conceptEmbedder maps words to a few concept axes, so "presentation" lands
// near "deck"/"slide" without sharing a word with them.
type conceptEmbedder struct {
	calls  int
	inputs int
	fail   error
}

var concepts = map[string]int{
	"service": 0, "accounts": 0, "identity": 0, "credentials": 0, "tokens": 0, "kubernetes": 0,
	"deck": 1, "slide": 1, "presentation": 1, "summary": 1,
	"hypotheses": 2, "proposals": 2,
}

func (c *conceptEmbedder) Model() string { return "concept-test" }

func (c *conceptEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if c.fail != nil {
		return nil, c.fail
	}
	c.calls++
	c.inputs += len(texts)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 4)
		v[3] = 0.01 // keep every vector non-zero
		for _, w := range strings.Fields(strings.ToLower(t)) {
			if axis, ok := concepts[strings.Trim(w, ".,'?s")]; ok {
				v[axis]++
			} else if axis, ok := concepts[strings.Trim(w, ".,'?")]; ok {
				v[axis]++
			}
		}
		out[i] = v
	}
	return out, nil
}

func indexed(t *testing.T) (*Index, string) {
	t.Helper()
	ix := openIndex(t)
	root := newProjects(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	return ix, root
}

func TestEmbedMissing_OnlyHumanSessionsAndOnlyOnce(t *testing.T) {
	ix, root := indexed(t)
	e := &conceptEmbedder{}

	n, err := ix.EmbedMissing(context.Background(), e, 2)
	if err != nil {
		t.Fatalf("EmbedMissing: %v", err)
	}
	// three human sessions: one chunk each plus one session-summary vector
	// each; the automated session is skipped
	if n != 6 {
		t.Errorf("embedded %d vectors, want 6", n)
	}
	if e.calls != 3 { // batch size 2 -> 3 requests for 6 inputs
		t.Errorf("embedder called %d times, want 3 (batching)", e.calls)
	}
	if n, _ := ix.EmbedMissing(context.Background(), e, 2); n != 0 {
		t.Errorf("second run embedded %d, want 0", n)
	}

	// a changed session loses its embeddings and only it is re-embedded
	p := put(t, root, "-Users-me-research", sidDeck, human("/Users/me/research", "a new presentation outline"))
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(p, future, future)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if n, _ := ix.EmbedMissing(context.Background(), e, 10); n != 2 {
		t.Errorf("after change embedded %d, want 2 (its chunk and its summary)", n)
	}
}

func TestEmbedMissing_PropagatesEmbedderError(t *testing.T) {
	ix, _ := indexed(t)
	boom := errors.New("rate limited")
	if _, err := ix.EmbedMissing(context.Background(), &conceptEmbedder{fail: boom}, 10); !errors.Is(err, boom) {
		t.Errorf("err = %v, want rate limited", err)
	}
}

func TestSemantic_FindsMeaningWithoutSharedWords(t *testing.T) {
	ix, _ := indexed(t)
	e := &conceptEmbedder{}
	if _, err := ix.EmbedMissing(context.Background(), e, 10); err != nil {
		t.Fatal(err)
	}
	// keyword search can't see it: no session contains "presentation"
	if rs, _ := ix.Search("presentation", SearchOpts{Limit: 5}); len(rs) != 0 {
		t.Fatalf("precondition: keyword matched %v", ids(rs))
	}
	rs, err := ix.Semantic(context.Background(), "presentation", SearchOpts{Limit: 5}, e)
	if err != nil {
		t.Fatalf("Semantic: %v", err)
	}
	if len(rs) == 0 || rs[0].SessionID != sidDeck {
		t.Errorf("semantic top = %v, want %s first", ids(rs), sidDeck)
	}
	for _, r := range rs {
		if r.SessionID == sidAuto {
			t.Error("automated session in semantic results")
		}
		if r.Title == "" || r.CWD == "" {
			t.Errorf("semantic result missing metadata: %+v", r)
		}
	}
}

func TestHybrid_FusesKeywordAndSemantic(t *testing.T) {
	ix, _ := indexed(t)
	e := &conceptEmbedder{}
	if _, err := ix.EmbedMissing(context.Background(), e, 10); err != nil {
		t.Fatal(err)
	}
	// "kubernetes" matches sidAccounts by keyword only; the query leans on
	// "presentation", so sidDeck is the strong semantic-only hit
	rs, used, err := ix.Hybrid(context.Background(), "presentation presentation kubernetes", SearchOpts{Limit: 5}, e)
	if err != nil || !used {
		t.Fatalf("Hybrid: used=%v err=%v", used, err)
	}
	got := strings.Join(ids(rs), ",")
	if !strings.Contains(got, sidAccounts) || !strings.Contains(got, sidDeck) {
		t.Errorf("hybrid = %v, want both the keyword and the semantic hit", ids(rs))
	}
	if len(rs) > 5 {
		t.Errorf("limit ignored: %d results", len(rs))
	}
}

func TestHybrid_FallsBackToKeyword(t *testing.T) {
	ix, _ := indexed(t)
	kw, _ := ix.Search("service accounts", SearchOpts{Limit: 5})

	// no embedder configured
	rs, used, err := ix.Hybrid(context.Background(), "service accounts", SearchOpts{Limit: 5}, nil)
	if err != nil || used || strings.Join(ids(rs), ",") != strings.Join(ids(kw), ",") {
		t.Errorf("nil embedder: used=%v err=%v got=%v want=%v", used, err, ids(rs), ids(kw))
	}
	// embedder fails at query time: still answer with keyword results
	rs, used, err = ix.Hybrid(context.Background(), "service accounts", SearchOpts{Limit: 5}, &conceptEmbedder{fail: errors.New("offline")})
	if err != nil || used || len(rs) != len(kw) {
		t.Errorf("failing embedder: used=%v err=%v got=%v", used, err, ids(rs))
	}
	// embedder fine but nothing embedded yet
	rs, used, _ = ix.Hybrid(context.Background(), "service accounts", SearchOpts{Limit: 5}, &conceptEmbedder{})
	if used || len(rs) != len(kw) {
		t.Errorf("no stored vectors: used=%v got=%v", used, ids(rs))
	}
}

func TestOpenAIEmbedder_RequestAndResponse(t *testing.T) {
	var got struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions int      `json:"dimensions"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		// return out of order to prove results are placed by index
		_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()

	e := &OpenAIEmbedder{APIKey: "test-key", BaseURL: srv.URL, ModelName: "text-embedding-3-small", Dimensions: 512}
	vecs, err := e.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got.Model != "text-embedding-3-small" || got.Dimensions != 512 || len(got.Input) != 2 {
		t.Errorf("request = %+v", got)
	}
	if vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Errorf("vectors not ordered by index: %v", vecs)
	}
	if e.Model() != "text-embedding-3-small@512" {
		t.Errorf("Model() = %q", e.Model())
	}
}

func TestOpenAIEmbedder_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"quota exceeded"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := (&OpenAIEmbedder{APIKey: "k", BaseURL: srv.URL}).Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("err = %v, want status and message", err)
	}
	if _, err := (&OpenAIEmbedder{}).Embed(context.Background(), []string{"x"}); err == nil {
		t.Error("missing API key: want error")
	}
	if vecs, err := (&OpenAIEmbedder{APIKey: "k", BaseURL: srv.URL}).Embed(context.Background(), nil); err != nil || len(vecs) != 0 {
		t.Errorf("empty input: %v %v, want no call and no error", vecs, err)
	}
}

func TestNewOpenAIEmbedderFromEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if NewOpenAIEmbedderFromEnv() != nil {
		t.Error("no key in env: want nil embedder")
	}
	t.Setenv("OPENAI_API_KEY", "from-env")
	e := NewOpenAIEmbedderFromEnv()
	if e == nil || e.APIKey != "from-env" || e.Dimensions != 512 {
		t.Errorf("from env: %+v", e)
	}
}

func TestPendingEmbeddings(t *testing.T) {
	ix, _ := indexed(t)
	e := &conceptEmbedder{}
	if n, err := ix.PendingEmbeddings(e.Model()); err != nil || n != 6 {
		t.Errorf("before: n=%d err=%v, want 6 (3 chunks + 3 summaries)", n, err)
	}
	if _, err := ix.EmbedMissing(context.Background(), e, 10); err != nil {
		t.Fatal(err)
	}
	if n, _ := ix.PendingEmbeddings(e.Model()); n != 0 {
		t.Errorf("after: n=%d, want 0", n)
	}
	if n, _ := ix.PendingEmbeddings("other-model"); n != 6 {
		t.Errorf("other model: n=%d, want 6 (vectors are per model)", n)
	}
}

func TestHybrid_DropsWeakSemanticMatches(t *testing.T) {
	ix, _ := indexed(t)
	e := &conceptEmbedder{}
	if _, err := ix.EmbedMissing(context.Background(), e, 10); err != nil {
		t.Fatal(err)
	}
	// "presentation" is close to the deck session only; the others share no
	// concept with it and must not ride in on semantic similarity alone
	rs, used, err := ix.Hybrid(context.Background(), "presentation", SearchOpts{Limit: 20}, e)
	if err != nil || !used {
		t.Fatalf("used=%v err=%v", used, err)
	}
	if got := ids(rs); len(got) != 1 || got[0] != sidDeck {
		t.Errorf("hybrid = %v, want only %s", got, sidDeck)
	}
}

func TestEmbedMissing_UsesProseAndSessionSummary(t *testing.T) {
	root := t.TempDir()
	put(t, root, "-p", sidAccounts,
		human("/p", "why do agents need their own identity"),
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"kubectl get pods -A"}}]}}`,
		reply("Because tokens must be short-lived."),
		human("/p", "and where would the tokens come from"))
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	rec := &recordingEmbedder{}
	if _, err := ix.EmbedMissing(context.Background(), rec, 10); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rec.inputs, "\n---\n")
	if strings.Contains(joined, "kubectl") {
		t.Errorf("tool commands were embedded:\n%s", joined)
	}
	var summary string
	for _, in := range rec.inputs {
		if strings.Contains(in, "why do agents need") && strings.Contains(in, "where would the tokens come from") {
			summary = in
		}
	}
	if summary == "" {
		t.Errorf("no session-summary input combining the prompts:\n%s", joined)
	}
}

type recordingEmbedder struct{ inputs []string }

func (r *recordingEmbedder) Model() string { return "rec" }
func (r *recordingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	r.inputs = append(r.inputs, texts...)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func TestCachedEmbedder_ReusesQueryVectors(t *testing.T) {
	ix := openIndex(t)
	inner := &conceptEmbedder{}
	e := ix.CachedEmbedder(inner)
	if e.Model() != inner.Model() {
		t.Errorf("Model() = %q, want the inner model", e.Model())
	}
	first, err := e.Embed(context.Background(), []string{"deck presentation"})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := e.Embed(context.Background(), []string{"deck presentation"})
	if inner.calls != 1 {
		t.Errorf("inner embedder called %d times, want 1 (second served from cache)", inner.calls)
	}
	if len(second) != 1 || dot(normalize(first[0]), normalize(second[0])) < 0.999 {
		t.Error("cached vector differs from the original")
	}
	// a different query, or a batch, goes to the inner embedder
	_, _ = e.Embed(context.Background(), []string{"service accounts"})
	if inner.calls != 2 {
		t.Errorf("new query: inner calls = %d, want 2", inner.calls)
	}
	// errors are not cached
	failing := ix.CachedEmbedder(&conceptEmbedder{fail: errors.New("offline")})
	if _, err := failing.Embed(context.Background(), []string{"x"}); err == nil {
		t.Error("want the inner error")
	}
}

func TestEmbeddings_SurviveSessionGrowth(t *testing.T) {
	root := t.TempDir()
	lines := []string{
		human("/p", "why do agents need service accounts"), reply("for short-lived tokens"),
		human("/p", "where do the tokens come from"), reply("the gateway mints them"),
		human("/p", "how long do they live"), reply("minutes"),
	}
	p := put(t, root, "-p", sidAccounts, lines...)
	ix := openIndex(t)
	e := &conceptEmbedder{}
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.EmbedMissing(context.Background(), e, 50); err != nil {
		t.Fatal(err)
	}

	grow := func(extra ...string) {
		t.Helper()
		lines = append(lines, extra...)
		put(t, root, "-p", sidAccounts, lines...)
		future := time.Now().Add(time.Duration(len(lines)) * time.Minute)
		_ = os.Chtimes(p, future, future)
		if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
			t.Fatal(err)
		}
	}

	// the assistant adds to the last exchange: only that exchange changed
	grow(reply("and they are bound to one sandbox"))
	if n, _ := ix.PendingEmbeddings(e.Model()); n != 1 {
		t.Errorf("after reply: %d pending, want 1 (the changed exchange only)", n)
	}
	if _, err := ix.EmbedMissing(context.Background(), e, 50); err != nil {
		t.Fatal(err)
	}

	// a new question: one new exchange plus the session summary (its prompts changed)
	grow(human("/p", "can vault trust them"))
	if n, _ := ix.PendingEmbeddings(e.Model()); n != 2 {
		t.Errorf("after new prompt: %d pending, want 2", n)
	}
	// unchanged exchanges stay searchable semantically meanwhile
	if rs, _ := ix.Semantic(context.Background(), "identity credentials", SearchOpts{Limit: 5}, e); len(rs) != 1 {
		t.Errorf("session lost its vectors while growing: %v", ids(rs))
	}
}
