package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
)

// namingFixture is localFixture whose sessions carry descriptions and pins.
func namingFixture(t *testing.T) (*LocalIndex, *int) {
	t.Helper()
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	now := time.Now()
	scans := 0
	x.Discover = func(history.DiscoverOpts, string) ([]history.Session, error) {
		scans++
		return []history.Session{
			{ID: "idle", Provider: "claude", Title: "Fix the cache", FilePath: "/p/idle.jsonl", LastActive: now,
				Description: "Owner words", DescriptionBy: "owner", AwaySummary: "Recap", AwaySummaryAt: now, Starred: true},
			{ID: "open", Provider: "claude", FirstPrompt: "hello", FilePath: "/p/open.jsonl", LastActive: now,
				AwaySummary: "Claude's recap", AwaySummaryAt: now},
			{ID: "busy", Provider: "claude", Title: "Plain", FilePath: "/p/busy.jsonl", LastActive: now},
		}, nil
	}
	return x, &scans
}

func TestLocalIndex_SessionsCarryDescriptionAndPin(t *testing.T) {
	x, _ := namingFixture(t)
	got, err := x.Sessions(context.Background(), 50, false)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Session{}
	for _, s := range got {
		by[s.ID] = s
	}
	if s := by["idle"]; s.Description != "Owner words" || !s.Pinned {
		t.Errorf("idle = %+v, want the owner's description and pinned", s)
	}
	if s := by["open"]; s.Description != "Claude's recap" || s.Pinned {
		t.Errorf("open = %+v, want the away summary and not pinned", s)
	}
	raw, _ := json.Marshal(by["busy"])
	if strings.Contains(string(raw), "description") || !strings.Contains(string(raw), `"pinned":false`) {
		t.Errorf("busy = %s, want no description key and pinned false", raw)
	}
}

func TestLocalIndex_DetailCarriesDescriptionAndPin(t *testing.T) {
	x, _ := namingFixture(t)
	d, err := x.Session(context.Background(), "idle", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Description != "Owner words" || !d.Pinned {
		t.Fatalf("detail = %+v, want description and pinned", d.Session)
	}
}

func TestLocalIndex_LookupReadsTheLastListWithoutScanning(t *testing.T) {
	x, scans := namingFixture(t)
	if _, ok := x.Lookup("idle"); ok {
		t.Fatal("Lookup before any list: want false (it never scans)")
	}
	if _, err := x.Sessions(context.Background(), 1, false); err != nil {
		t.Fatal(err)
	}
	before := *scans
	s, ok := x.Lookup("busy") // beyond the limit of 1, still known from the scan
	if !ok || s.FilePath != "/p/busy.jsonl" {
		t.Fatalf("Lookup(busy) = %+v %v, want the history record", s, ok)
	}
	if _, ok := x.Lookup("missing"); ok {
		t.Error("Lookup(missing) = true")
	}
	if *scans != before {
		t.Errorf("Lookup scanned %d times", *scans-before)
	}
}

func TestLocalIndex_Record(t *testing.T) {
	x, _ := namingFixture(t)
	s, err := x.Record(context.Background(), "open")
	if err != nil || s.FilePath != "/p/open.jsonl" {
		t.Fatalf("Record(open) = %+v %v", s, err)
	}
	if _, err := x.Record(context.Background(), "missing"); !errors.Is(err, acprun.ErrUnknownSession) {
		t.Fatalf("Record(missing) err = %v, want unknown session", err)
	}
	// a record is also what Lookup then knows
	if _, ok := x.Lookup("open"); !ok {
		t.Error("Lookup(open) after Record = false")
	}
}

func TestLocalIndex_TerminalPIDs(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	got := x.TerminalPIDs()
	if len(got) != 1 || got["open"] != os.Getpid() {
		t.Fatalf("TerminalPIDs = %v, want open -> this process", got)
	}
	x.Owned = func(int) bool { return true }
	if got := x.TerminalPIDs(); len(got) != 0 {
		t.Fatalf("TerminalPIDs with aimux's own holder = %v, want none", got)
	}
}

// Search hits carry the same description and pin as the list, from the scan
// the search already makes.
func TestLocalIndex_SearchHitsCarryDescriptionAndPin(t *testing.T) {
	x, _ := namingFixture(t)
	x.SearchIndex = func(context.Context, string, int, []string) ([]search.Result, error) {
		return []search.Result{{SessionID: "idle", Title: "Fix the cache", Snippet: "the [cache]"},
			{SessionID: "busy", Title: "Plain", Snippet: "[plain]"}}, nil
	}
	hits, err := x.Search(context.Background(), "cache", 10)
	if err != nil || len(hits) != 2 {
		t.Fatalf("hits %+v %v", hits, err)
	}
	if hits[0].Description != "Owner words" || !hits[0].Pinned || hits[1].Description != "" || hits[1].Pinned {
		t.Fatalf("hits %+v, want idle described and pinned, busy neither", hits)
	}
}
