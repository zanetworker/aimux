package search

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestSearchQuality scores ranking against a private set of queries with
// known answers. It runs only when AIMUX_SEARCH_EVAL points at a TSV file of
// "query<TAB>session-id-prefix<TAB>note" lines, against the real index at
// AIMUX_SEARCH_DB (default ~/.aimux/search.db):
//
//	AIMUX_SEARCH_EVAL=~/.aimux/search-eval.tsv go test ./internal/search -run SearchQuality -v
//
// AIMUX_SEARCH_EVAL_EXCLUDE lists session-id prefixes to drop from results,
// e.g. the session in which the queries were written (it contains them all).
func TestSearchQuality(t *testing.T) {
	path := os.Getenv("AIMUX_SEARCH_EVAL")
	if path == "" {
		t.Skip("set AIMUX_SEARCH_EVAL to a query file to measure ranking quality")
	}
	db := os.Getenv("AIMUX_SEARCH_DB")
	if db == "" {
		db = DefaultPath()
	}
	ix, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ix.Close() }()

	type q struct{ text, want, note string }
	var qs []q
	f, err := os.Open(path) // #nosec G304 G703 -- developer-supplied eval file
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) >= 2 && !strings.HasPrefix(p[0], "#") {
			qs = append(qs, q{p[0], p[1], strings.Join(p[2:], " ")})
		}
	}
	_ = f.Close()

	exclude := strings.Fields(os.Getenv("AIMUX_SEARCH_EVAL_EXCLUDE"))
	keep := func(rs []Result) []Result {
		var out []Result
	next:
		for _, r := range rs {
			for _, x := range exclude {
				if strings.HasPrefix(r.SessionID, x) {
					continue next
				}
			}
			out = append(out, r)
		}
		return out
	}
	e := NewOpenAIEmbedderFromEnv()
	modes := map[string]func(string) []Result{
		"keyword": func(s string) []Result { rs, _ := ix.Search(s, SearchOpts{Limit: 30}); return keep(rs) },
	}
	if e != nil {
		modes["semantic"] = func(s string) []Result {
			rs, _ := ix.Semantic(context.Background(), s, SearchOpts{Limit: 30}, e)
			return keep(rs)
		}
		modes["hybrid"] = func(s string) []Result {
			rs, _, _ := ix.Hybrid(context.Background(), s, SearchOpts{Limit: 30}, e)
			return keep(rs)
		}
	}
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		run, ok := modes[mode]
		if !ok {
			continue
		}
		var top1, top5, total int
		var mrr float64
		var misses []string
		for _, c := range qs {
			rank := 0
			rs := run(c.text)
			total += len(rs)
			for i, r := range rs {
				if strings.HasPrefix(r.SessionID, c.want) {
					rank = i + 1
					break
				}
			}
			if rank == 1 {
				top1++
			}
			if rank >= 1 && rank <= 5 {
				top5++
			}
			if rank > 0 {
				mrr += 1 / float64(rank)
			}
			if rank != 1 {
				misses = append(misses, fmt.Sprintf("    rank %-2s %s  (%s)", rankStr(rank), c.text, c.note))
			}
		}
		n := float64(len(qs))
		t.Logf("%-8s top1 %2d/%d  top5 %2d/%d  MRR %.2f  avg results %.1f", mode, top1, len(qs), top5, len(qs), mrr/n, float64(total)/n)
		for _, m := range misses {
			t.Log(m)
		}
	}
}

func rankStr(r int) string {
	if r == 0 {
		return "-"
	}
	return fmt.Sprint(r)
}
