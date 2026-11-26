package modelproxy

import (
	"path/filepath"
	"testing"
	"time"
)

func TestReadRequestFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	log, err := OpenRequestLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, r := range []Record{
		{At: now.Add(-50 * time.Minute), Outcome: "forwarded", Detail: "anthropic", Status: 429, Error: "upstream_error"},
		{At: now.Add(-10 * time.Minute), Outcome: "forwarded", Detail: "anthropic", Status: 429, Error: "upstream_error"},
		{At: now.Add(-5 * time.Minute), Outcome: "forwarded", Detail: "anthropic", Status: 200},
		{At: now.Add(-4 * time.Minute), Outcome: "new_session", Status: 200},
		{At: now.Add(-3 * time.Minute), Outcome: "new_session", Status: 502, Error: "codex_failed"},
		{At: now.Add(-3 * time.Hour), Outcome: "forwarded", Detail: "anthropic", Status: 401, Error: "upstream_error"}, // outside the window
	} {
		log.Record(r)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	total, failures, incomplete, err := ReadRequestFailures(path, time.Hour, now)
	if err != nil || incomplete || total != 5 || len(failures) != 2 {
		t.Fatalf("total %d, %+v, %v", total, failures, err)
	}
	if f := failures[0]; f.Engine != "anthropic" || f.Status != 429 || f.Count != 2 || !f.LastAt.Equal(now.Add(-10*time.Minute)) {
		t.Errorf("first = %+v", f)
	}
	if f := failures[1]; f.Engine != "codex" || f.Status != 502 || f.Error != "codex_failed" || f.Count != 1 {
		t.Errorf("second = %+v", f)
	}
	// A history that does not exist yet is said, as the stats command says it.
	if _, _, _, err := ReadRequestFailures(filepath.Join(t.TempDir(), "none.jsonl"), time.Hour, now); err == nil {
		t.Error("no file gave no error")
	}
}
