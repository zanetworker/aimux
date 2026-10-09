package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/search"
)

func searchResponse(t *testing.T, s *Server, q string) (int, []map[string]string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleSearch(rec, httptest.NewRequest(http.MethodGet, "/api/search?q="+q, nil))
	var body struct {
		Results []map[string]string `json:"results"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
	}
	return rec.Code, body.Results
}

func TestHandleSearch_UsesIndexRankingAndKeepsFields(t *testing.T) {
	s := NewServer(0)
	var gotQuery string
	s.SetSearchFunc(func(q string) ([]search.Result, error) {
		gotQuery = q
		return []search.Result{
			{SessionID: "best", Path: "/p/best.jsonl", Title: "agent-ops", CWD: "/Users/me/OpenShell", Snippet: "[token] exchange"},
			{SessionID: "second", Path: "/p/second.jsonl", Title: "other", CWD: "/Users/me/research", Snippet: "token"},
		}, nil
	})
	code, rs := searchResponse(t, s, "token")
	if code != http.StatusOK || gotQuery != "token" {
		t.Fatalf("code=%d query=%q", code, gotQuery)
	}
	if len(rs) != 2 || rs[0]["sessionId"] != "best" || rs[1]["sessionId"] != "second" {
		t.Fatalf("ranking not preserved: %v", rs)
	}
	// the fields the web frontend already reads, plus title and project
	for _, k := range []string{"sessionId", "filePath", "snippet", "title", "project"} {
		if rs[0][k] == "" {
			t.Errorf("result missing %q: %v", k, rs[0])
		}
	}
}

func TestHandleSearch_ErrorsAndUnconfigured(t *testing.T) {
	s := NewServer(0)
	s.SetSearchFunc(func(string) ([]search.Result, error) { return nil, errors.New("index locked") })
	rec := httptest.NewRecorder()
	s.handleSearch(rec, httptest.NewRequest(http.MethodGet, "/api/search?q=x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("search error: code %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "index locked") {
		t.Errorf("internal error text leaked to the client: %q", rec.Body.String())
	}
	// no search function wired: an empty, valid answer rather than a scan
	if code, rs := searchResponse(t, NewServer(0), "x"); code != http.StatusOK || len(rs) != 0 {
		t.Errorf("unconfigured: code=%d results=%v", code, rs)
	}
}
