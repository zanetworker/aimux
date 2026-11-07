package remoteapi

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

// visibilityIndex lists one session per reason a list may hide it, plus an
// automated session created from the phone ("made").
func visibilityIndex(t *testing.T) *LocalIndex {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	s := func(id string) history.Session {
		return history.Session{ID: id, Provider: "claude", Project: dir, Cwd: dir, CostUSD: 1, TurnCount: 10, StartTime: now, LastActive: now}
	}
	empty := s("empty")
	empty.CostUSD, empty.TurnCount = 0, 2
	sub := s("sub")
	sub.IsSubagent = true
	auto := s("auto")
	auto.Automated = true
	hook := s("hook")
	hook.FirstPrompt = "YOU ARE A SESSION ANALYZER"
	made := s("made")
	made.Automated = true
	undated := history.Session{ID: "undated", Provider: "claude", Project: dir, Cwd: dir}
	x := &LocalIndex{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Providers: stubProviders(&stubStore{Discover: func(history.DiscoverOpts, string) ([]history.Session, error) {
		return []history.Session{s("plain"), empty, sub, auto, hook, made, undated}, nil
	}}, nil)}
	x.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	if err := x.Created.Add("made", dir, "x"); err != nil {
		t.Fatal(err)
	}
	x.Running = func(string) bool { return false }
	return x
}

func joinedIDs(ids []string) string {
	sort.Strings(ids)
	return strings.Join(ids, " ")
}

// TestLocalIndex_VisibleSetPinned pins what the remote list shows: never
// subagents, automated sessions only when asked for or created here, and
// none of the dashboard's or TUI's noise rules.
func TestLocalIndex_VisibleSetPinned(t *testing.T) {
	x := visibilityIndex(t)
	for _, c := range []struct {
		automated bool
		want      string
	}{
		{false, "empty hook made plain undated"},
		{true, "auto empty hook made plain undated"},
	} {
		got, err := x.Sessions(context.Background(), 100, c.automated)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range got {
			ids = append(ids, s.ID)
		}
		if g := joinedIDs(ids); g != c.want {
			t.Errorf("includeAutomated=%v: %q, want %q", c.automated, g, c.want)
		}
	}
	if g := joinedIDs(x.listableIDs()); g != "empty hook made plain undated" {
		t.Errorf("listable: %q", g)
	}
}

func TestLocalIndex_ProjectsSkipHidden(t *testing.T) {
	x := visibilityIndex(t)
	root := t.TempDir()
	sub, auto, made, plain := filepath.Join(root, "sub"), filepath.Join(root, "auto"), filepath.Join(root, "made"), filepath.Join(root, "plain")
	for _, d := range []string{sub, auto, made, plain} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stubOf(x).Discover = func(history.DiscoverOpts, string) ([]history.Session, error) {
		now := time.Now()
		return []history.Session{
			{ID: "sub", Cwd: sub, IsSubagent: true, LastActive: now},
			{ID: "auto", Cwd: auto, Automated: true, LastActive: now},
			{ID: "made", Cwd: made, Automated: true, LastActive: now},
			{ID: "plain", Cwd: plain, LastActive: now},
		}, nil
	}
	got, err := x.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range got {
		names = append(names, p.Name)
	}
	if g := joinedIDs(names); g != "made plain" {
		t.Errorf("projects %q, want made plain", g)
	}
}
