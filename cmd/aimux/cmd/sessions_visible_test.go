package cmd

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

// cliVisibilityFixture has one session per reason a list may hide it,
// including both half-dated cases, so the undated choice is explicit.
func cliVisibilityFixture() []history.Session {
	at := time.Now().Add(-time.Hour)
	s := func(id string, cost float64, turns int) history.Session {
		return history.Session{ID: id, Provider: "claude", Project: "/w", CostUSD: cost, TurnCount: turns, StartTime: at, LastActive: at}
	}
	empty := s("v-empty", 0, 5)
	sub := s("v-sub", 1, 10)
	sub.IsSubagent = true
	auto := s("v-auto", 1, 10)
	auto.Automated = true
	hook := s("v-hook", 1, 10)
	hook.FirstPrompt = "YOU ARE A helper"
	analyzer := s("v-analyzer", 1, 10)
	analyzer.Title = "a session analyzer"
	undated := history.Session{ID: "v-undated", Provider: "claude", Project: "/w", CostUSD: 1, TurnCount: 10}
	startOnly := undated
	startOnly.ID, startOnly.StartTime = "v-startonly", at
	lastOnly := undated
	lastOnly.ID, lastOnly.LastActive = "v-lastonly", at
	return []history.Session{s("v-plain", 1, 10), s("v-six", 0, 6), empty, sub, auto, hook, analyzer, undated, startOnly, lastOnly}
}

func cliListedIDs(t *testing.T, args ...string) string {
	t.Helper()
	var stdout bytes.Buffer
	c := newSessionsCmd(func(history.DiscoverOpts, string) ([]history.Session, error) { return cliVisibilityFixture(), nil }, nil, nil)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs(append([]string{"sessions", "--list", "--all"}, args...))
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	jsonOutput = true
	defer func() { jsonOutput = false }()
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, id := range []string{"v-plain", "v-six", "v-empty", "v-sub", "v-auto", "v-hook", "v-analyzer", "v-undated", "v-startonly", "v-lastonly"} {
		if strings.Contains(stdout.String(), `"`+id+`"`) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if !json.Valid(bytes.TrimSpace(stdout.Bytes())) {
		t.Fatalf("not JSON: %s", stdout.String())
	}
	return strings.Join(ids, " ")
}

// TestSessionsList_VisibleSetPinned pins what `aimux sessions --list` shows.
// history.CLIOpts counts a session as undated only when neither time is
// known (as the TUI does), so v-startonly (start time, no last-active time)
// is now listed; before the move the CLI hid any session without a
// last-active time. history's scan fills both times from the file's mtime,
// so a real session is never half-dated.
func TestSessionsList_VisibleSetPinned(t *testing.T) {
	if got, want := cliListedIDs(t), "v-analyzer v-hook v-lastonly v-plain v-six v-startonly"; got != want {
		t.Errorf("default: %q, want %q", got, want)
	}
	if got, want := cliListedIDs(t, "--include-automated"), "v-analyzer v-auto v-hook v-lastonly v-plain v-six v-startonly"; got != want {
		t.Errorf("--include-automated: %q, want %q", got, want)
	}
}
