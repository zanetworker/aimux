package history

import (
	"testing"
	"time"
)

func TestFindEmpty(t *testing.T) {
	sessions := []Session{
		{ID: "a", TurnCount: 16, CostUSD: 0.42},
		{ID: "b", TurnCount: 1, CostUSD: 0},
		{ID: "c", TurnCount: 2, CostUSD: 0},
		{ID: "d", TurnCount: 3, CostUSD: 0.10},
	}
	empty := FindEmpty(sessions)
	if len(empty) != 2 {
		t.Fatalf("expected 2 empty, got %d", len(empty))
	}
	ids := map[string]bool{}
	for _, s := range empty {
		ids[s.ID] = true
	}
	if !ids["b"] || !ids["c"] {
		t.Errorf("expected b and c as empty, got IDs %v", ids)
	}
}

func TestFindEmpty_NoneEmpty(t *testing.T) {
	sessions := []Session{
		{ID: "a", TurnCount: 10, CostUSD: 1.0},
	}
	if got := FindEmpty(sessions); len(got) != 0 {
		t.Errorf("expected 0 empty, got %d", len(got))
	}
}

func TestFindDuplicates(t *testing.T) {
	now := time.Now()
	sessions := []Session{
		{ID: "a", Project: "/proj", FirstPrompt: "fix the bug", TurnCount: 20, LastActive: now},
		{ID: "b", Project: "/proj", FirstPrompt: "fix the bug", TurnCount: 5, LastActive: now.Add(-time.Hour)},
		{ID: "c", Project: "/proj", FirstPrompt: "fix the bug", TurnCount: 2, LastActive: now.Add(-2 * time.Hour)},
		{ID: "d", Project: "/proj", FirstPrompt: "different task", TurnCount: 10, LastActive: now},
	}
	dupes := FindDuplicates(sessions)
	if len(dupes) != 2 {
		t.Fatalf("expected 2 duplicates, got %d", len(dupes))
	}
	ids := map[string]bool{}
	for _, s := range dupes {
		ids[s.ID] = true
	}
	if ids["a"] {
		t.Error("session 'a' should be the keeper, not a duplicate")
	}
	if !ids["b"] || !ids["c"] {
		t.Errorf("expected b and c as duplicates, got IDs %v", ids)
	}
}

func TestFindDuplicates_DifferentProjects(t *testing.T) {
	sessions := []Session{
		{ID: "a", Project: "/proj1", FirstPrompt: "fix the bug", TurnCount: 10},
		{ID: "b", Project: "/proj2", FirstPrompt: "fix the bug", TurnCount: 5},
	}
	dupes := FindDuplicates(sessions)
	if len(dupes) != 0 {
		t.Errorf("expected 0 duplicates across projects, got %d", len(dupes))
	}
}

func TestFindDuplicates_NoPrompt(t *testing.T) {
	sessions := []Session{
		{ID: "a", Project: "/proj", FirstPrompt: "(no prompt)", TurnCount: 10},
		{ID: "b", Project: "/proj", FirstPrompt: "(no prompt)", TurnCount: 5},
	}
	dupes := FindDuplicates(sessions)
	if len(dupes) != 0 {
		t.Errorf("expected 0 duplicates for (no prompt), got %d", len(dupes))
	}
}
