package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The owner's hide (Wingman's history delete): the remote lists never show
// it, whatever they reveal; other lists are unchanged.
func TestRemoteOpts_HideOwnerHiddenWhateverIsRevealed(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := Session{CostUSD: 1, TurnCount: 10, StartTime: at, LastActive: at, Hidden: true}
	for _, o := range []VisibleOpts{RemoteOpts(false, false), RemoteOpts(true, true)} {
		if Visible(s, o) {
			t.Errorf("hidden session visible with %+v", o)
		}
	}
	s.Hidden = false
	if !Visible(s, RemoteOpts(false, false)) {
		t.Error("a plain session is hidden")
	}
	if HiddenBy(Session{CostUSD: 1, TurnCount: 10, StartTime: at, Hidden: true}, TUIOpts(false, false)) != 0 {
		t.Error("the TUI list hides an owner-hidden session; only the remote lists do")
	}
}

func TestApplyMeta_ReadsHidden(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMeta(file, func(m *Meta) { m.Hidden = true }); err != nil {
		t.Fatal(err)
	}
	s := Session{FilePath: file}
	applyMeta(&s)
	if !s.Hidden {
		t.Fatal("applyMeta left Hidden false")
	}
}
