//go:build iterm2

package iterm2

// Read-only check of the Mac layout against the real iTerm2 and Claude's
// session registry. It types, focuses and changes nothing, and logs counts
// and booleans only, never a window, tab or session name:
//
//	AIMUX_LIVE_LAYOUT=1 go test -tags iterm2 ./internal/terminput/iterm2/ -run LayoutLive -v -timeout 120s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/terminput"
)

func TestLayoutLive(t *testing.T) {
	if os.Getenv("AIMUX_LIVE_LAYOUT") != "1" {
		t.Skip("AIMUX_LIVE_LAYOUT is not set")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	open := map[string]int{}
	for _, e := range discovery.ReadClaudeRegistry(filepath.Join(home, ".claude", "sessions")) {
		if e.SessionID != "" && discovery.IsLiveHolder(e) {
			open[e.SessionID] = e.PID
		}
	}

	d := New(Options{})
	defer func() { _ = d.Close() }()
	r := &terminput.LayoutReader{
		Source:    d,
		Sys:       terminput.SystemProbe{},
		Sessions:  func() map[string]int { return open },
		FirstWait: 8 * time.Second,
	}
	places := r.Places(context.Background())

	windows, tabs := map[string]bool{}, map[string]bool{}
	named := true
	for _, p := range places {
		windows[p.Window.ID] = true
		tabs[fmt.Sprintf("%s/%d", p.Window.ID, p.Tab.Index)] = true
		if p.Window.Name == "" || p.Tab.Name == "" {
			named = false
		}
	}
	t.Logf("open claude sessions in registry: %d", len(open))
	t.Logf("placed: %d", len(places))
	t.Logf("distinct windows: %d, distinct tabs: %d", len(windows), len(tabs))
	t.Logf("every placed session has a window and tab name: %v", named)
	if len(open) > 0 && len(places) == 0 {
		t.Errorf("no session placed (iTerm2 not running, or Automation access denied?)")
	}
}
