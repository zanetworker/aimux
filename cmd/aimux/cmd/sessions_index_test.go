package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
)

func fakeHits() []search.Result {
	return []search.Result{
		{SessionID: "a97a2cca", CWD: "/Users/me/OpenShell", Title: "openshell-service-accounts", ModTime: time.Now().Add(-time.Hour), Snippet: "we need [short]-[lived] tokens"},
		{SessionID: "43ce13d4", CWD: "/Users/me/research", Title: "yingzhao-hypotheses", ModTime: time.Now().Add(-2 * time.Hour), Snippet: "add the proposals to Ying's [deck]"},
	}
}

// runSessions executes `aimux sessions <args>` with the given search deps.
func runSessions(t *testing.T, deps sessionsSearchDeps, discoverCalled *bool, args ...string) (string, error) {
	t.Helper()
	orig := sessionsSearch
	sessionsSearch = deps
	t.Cleanup(func() { sessionsSearch = orig })

	var stdout bytes.Buffer
	c := newSessionsCmd(func(history.DiscoverOpts, string) ([]history.Session, error) {
		if discoverCalled != nil {
			*discoverCalled = true
		}
		return fakeSessions(), nil
	}, nil, nil)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs(append([]string{"sessions"}, args...))
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	err := rootCmd.Execute()
	return stdout.String(), err
}

func TestSessionsQuery_UsesIndexRankingAndSkipsDiscovery(t *testing.T) {
	var gotQuery string
	var gotOpts sessionsIndexQuery
	deps := sessionsSearchDeps{Index: func(q string, o sessionsIndexQuery) ([]search.Result, bool, error) {
		gotQuery, gotOpts = q, o
		return fakeHits(), true, nil
	}}
	var discovered bool
	jsonOutput = true
	defer func() { jsonOutput = false }()

	out, err := runSessions(t, deps, &discovered, "service accounts", "--list", "--limit", "7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if discovered {
		t.Error("query path should not run full session discovery")
	}
	if gotQuery != "service accounts" || gotOpts.Mode != "hybrid" || gotOpts.Limit != 7 || gotOpts.IncludeAutomated {
		t.Errorf("index called with %q %+v", gotQuery, gotOpts)
	}
	var res struct {
		Results []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Match   string `json:"match"`
			Project string `json:"project"`
		} `json:"results"`
		Count    int  `json:"count"`
		Semantic bool `json:"semantic"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if res.Count != 2 || res.Results[0].ID != "a97a2cca" || res.Results[1].ID != "43ce13d4" {
		t.Errorf("ranking not preserved: %+v", res.Results)
	}
	if res.Results[0].Match == "" || res.Results[0].Title != "openshell-service-accounts" || !res.Semantic {
		t.Errorf("missing match/title/semantic: %+v semantic=%v", res.Results[0], res.Semantic)
	}
}

func TestSessionsQuery_TableShowsTitleAndMatch(t *testing.T) {
	deps := sessionsSearchDeps{Index: func(string, sessionsIndexQuery) ([]search.Result, bool, error) {
		return fakeHits(), false, nil
	}}
	out, err := runSessions(t, deps, nil, "deck", "--list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TITLE", "MATCH", "yingzhao-hypotheses", "Ying's [deck]"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestSessionsQuery_Flags(t *testing.T) {
	var got sessionsIndexQuery
	deps := sessionsSearchDeps{Index: func(_ string, o sessionsIndexQuery) ([]search.Result, bool, error) {
		got = o
		return fakeHits(), false, nil
	}}
	if _, err := runSessions(t, deps, nil, "x", "--list", "--mode", "semantic", "--include-automated", "--dir", "/Users/me/research"); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "semantic" || !got.IncludeAutomated {
		t.Errorf("flags not passed: %+v", got)
	}

	_, err := runSessions(t, deps, nil, "x", "--list", "--mode", "fuzzy")
	if err == nil || !strings.Contains(err.Error(), "keyword") || !strings.Contains(err.Error(), "hybrid") {
		t.Errorf("bad mode: err=%v, want the valid values listed", err)
	}
}

func TestSessionsQuery_DirIsPassedIntoTheQuery(t *testing.T) {
	var got sessionsIndexQuery
	deps := sessionsSearchDeps{Index: func(_ string, q sessionsIndexQuery) ([]search.Result, bool, error) {
		got = q
		return fakeHits(), false, nil
	}}
	if _, err := runSessions(t, deps, nil, "x", "--list", "--dir", "/Users/me/research"); err != nil {
		t.Fatal(err)
	}
	// the index scopes before applying the limit (see search.TestSearch_DirScopesBeforeLimit)
	if got.Dir != "/Users/me/research" {
		t.Errorf("Dir = %q, want it passed to the index", got.Dir)
	}
}

func TestSessionsQuery_NoMatches(t *testing.T) {
	deps := sessionsSearchDeps{Index: func(string, sessionsIndexQuery) ([]search.Result, bool, error) {
		return nil, false, nil
	}}
	if _, err := runSessions(t, deps, nil, "zzz", "--list"); err == nil || !strings.Contains(err.Error(), "no sessions matching") {
		t.Errorf("err = %v", err)
	}
}

func TestOpenSession_FocusesLivePaneBeforeResuming(t *testing.T) {
	var resumed string
	resume := func(id string, _ bool) { resumed = id }
	focus := func(id string) (string, bool) {
		if id == "a97a2cca" {
			return "tab 2 · pane 1 of 2", true
		}
		return "", false
	}

	msg := openSession("a97a2cca", "openshell-service-accounts", false, focus, resume)
	if resumed != "" {
		t.Errorf("live session was resumed instead of focused")
	}
	for _, want := range []string{"openshell-service-accounts", "tab 2", "pane 1 of 2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	openSession("43ce13d4", "yingzhao-hypotheses", false, focus, resume)
	if resumed != "43ce13d4" {
		t.Errorf("not live: resumed=%q, want resume", resumed)
	}
	resumed = ""
	openSession("43ce13d4", "", true, nil, resume)
	if resumed != "43ce13d4" {
		t.Error("nil focus func: want plain resume")
	}
}

func TestOpenSessionGuarded_RefusesRecentlyActiveUnfocusable(t *testing.T) {
	var resumed string
	resume := func(id string, _ bool) { resumed = id }
	notLive := func(string) (string, bool) { return "", false }

	msg := openSessionGuarded("571c817e", "Resume lost sessions", time.Now().Add(-30*time.Second), false, notLive, resume)
	if resumed != "" {
		t.Fatal("resumed a session that was written 30s ago (likely open in another terminal)")
	}
	if !strings.Contains(msg, "active") || !strings.Contains(msg, "aimux resume 571c817e") {
		t.Errorf("message should explain and give the override: %q", msg)
	}

	openSessionGuarded("1299b1c0", "WX Orchestrate", time.Now().Add(-10*time.Minute), false, notLive, resume)
	if resumed != "1299b1c0" {
		t.Error("idle session should resume")
	}

	// boundary: exactly at the window counts as idle
	resumed = ""
	openSessionGuarded("edge", "", time.Now().Add(-activeWindow), false, notLive, resume)
	if resumed != "edge" {
		t.Error("session idle for exactly the window should resume")
	}
}

func TestSessionsQuery_RelativeDirIsResolved(t *testing.T) {
	var got sessionsIndexQuery
	deps := sessionsSearchDeps{Index: func(_ string, q sessionsIndexQuery) ([]search.Result, bool, error) {
		got = q
		return fakeHits(), false, nil
	}}
	cwd := t.TempDir()
	t.Chdir(cwd)
	if _, err := runSessions(t, deps, nil, "x", "--list", "--dir", "."); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(".")
	if got.Dir != want {
		t.Errorf("Dir = %q, want the absolute path %q", got.Dir, want)
	}
}

func TestSessionsLive_QueryRestrictsToLiveIDs(t *testing.T) {
	var got sessionsIndexQuery
	deps := sessionsSearchDeps{
		Index: func(_ string, q sessionsIndexQuery) ([]search.Result, bool, error) {
			got = q
			return fakeHits(), false, nil
		},
		LiveIDs: func() []string { return []string{"a97a2cca"} },
	}
	if _, err := runSessions(t, deps, nil, "x", "--list", "--live"); err != nil {
		t.Fatal(err)
	}
	if len(got.IDs) != 1 || got.IDs[0] != "a97a2cca" {
		t.Errorf("IDs = %v, want the live session ids passed to the index", got.IDs)
	}
	// without --live the query is unrestricted
	if _, err := runSessions(t, deps, nil, "x", "--list"); err != nil {
		t.Fatal(err)
	}
	if got.IDs != nil {
		t.Errorf("IDs = %v without --live, want nil (no restriction)", got.IDs)
	}
}

func TestSessionsLive_ListKeepsOnlyLiveSessions(t *testing.T) {
	deps := sessionsSearchDeps{LiveIDs: func() []string { return []string{"sess-002"} }}
	jsonOutput = true
	defer func() { jsonOutput = false }()
	out, err := runSessions(t, deps, nil, "--list", "--all", "--live")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sess-002") || strings.Contains(out, "sess-001") {
		t.Errorf("--live list should keep only live sessions:\n%s", out)
	}
}
