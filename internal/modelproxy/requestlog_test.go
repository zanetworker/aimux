package modelproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequestLogWritesOneLinePerRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	log, err := OpenRequestLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	at := time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)
	log.Record(Record{
		At: at, Outcome: "unmatched_tool_result", Detail: "tail=[user:tool_result user:text] pending=2",
		Model: "codex:gpt-6.2-sol", Effort: "medium", Status: 200, TTFB: 3 * time.Second, Duration: 7 * time.Second,
		Usage: Usage{InputTokens: 40, CacheReadInputTokens: 5504, OutputTokens: 150},
	})
	log.Record(Record{At: at.Add(time.Second), Outcome: "hit_continuation", Status: 200, TTFB: time.Second})
	records := readRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("got %d lines, want 2", len(records))
	}
	first := records[0]
	if first["outcome"] != "unmatched_tool_result" || first["detail"] == nil || first["model"] != "codex:gpt-6.2-sol" {
		t.Errorf("first record = %v", first)
	}
	if first["at"] != "2026-11-22T21:00:00Z" || first["ttfb_ms"] != float64(3000) || first["duration_ms"] != float64(7000) {
		t.Errorf("timing fields = %v", first)
	}
	if first["input_tokens"] != float64(40) || first["cached_input_tokens"] != float64(5504) || first["output_tokens"] != float64(150) {
		t.Errorf("token fields = %v", first)
	}
	if _, ok := records[1]["detail"]; ok {
		t.Errorf("an empty detail should be left out: %v", records[1])
	}
}

// The log runs unattended for months, so it must not grow without end.
func TestRequestLogRollsOverAtItsLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	log, err := OpenRequestLog(path, 400)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	for i := range 12 {
		log.Record(Record{At: time.Unix(int64(i), 0).UTC(), Outcome: "hit_continuation", Status: 200})
	}
	if size := fileSize(t, path); size > 400 {
		t.Errorf("live log is %d bytes, want it kept under the limit", size)
	}
	previous := fileSize(t, path+".1")
	if previous == 0 {
		t.Fatal("the rolled-over log is missing, so the oldest requests were lost")
	}
	newest := readRecords(t, path)
	if len(newest) == 0 || newest[len(newest)-1]["at"] != "1970-01-01T00:00:11Z" {
		t.Errorf("the live log should hold the newest request: %v", newest)
	}
}

func TestRequestLogKeepsWritingWhenTheFileIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenRequestLog(filepath.Join(dir, "missing", "requests.jsonl"), 1<<20); err == nil {
		t.Fatal("a log in a missing folder should report the problem at open")
	}
	var nilLog *RequestLog
	nilLog.Record(Record{Outcome: "hit_idle"}) // A proxy without a log must still serve requests.
}

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// A client picks the model name, so a record must not grow past what a reader can scan in one line.
func TestRequestLogBoundsClientSuppliedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	log, err := OpenRequestLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	log.Record(Record{Outcome: "stateless", Model: strings.Repeat("m", 5000)})
	records := readRecords(t, path)
	model, _ := records[0]["model"].(string)
	if len(model) > 400 {
		t.Errorf("a %d-character model name reached the history", len(model))
	}
}
