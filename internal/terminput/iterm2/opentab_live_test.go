//go:build iterm2

package iterm2

// Opens a real iTerm2 window, so it runs only when asked:
//
//	AIMUX_LIVE_OPENTAB=1 go test -tags iterm2 ./internal/terminput/iterm2/ -run OpenTabLive -v
//
// It first checks, read only, that no aimux window has room (it skips
// otherwise, so it never adds panes to the owner's aimux window), then opens
// three panes running `echo aimux-live`, which must fill one fresh aimux
// window (panes 1, 2, 3), and closes only that window by its id. It touches
// no other window.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// roomJS prints how many windows hold 1..argv[0]-1 aimux panes; read only.
const roomJS = `function run(argv) {
  const max = Number(argv[0]);
  const app = Application('iTerm2');
  if (!app.running()) return '0';
  let room = 0;
  const ids = app.windows.id();
  for (let w = 0; w < ids.length; w++) {
    let n = 0;
    const tabs = app.windows.byId(ids[w]).tabs();
    for (let t = 0; t < tabs.length; t++) {
      const ss = tabs[t].sessions();
      for (let s = 0; s < ss.length; s++) {
        try { if (String(ss[s].variable({ named: 'user.aimux' })) === '1') n++; } catch (e) {}
      }
    }
    if (n > 0 && n < max) room++;
  }
  return String(room);
}`

// closeWindowJS closes the window whose id is argv[0], and nothing else.
const closeWindowJS = `function run(argv) {
  const app = Application('iTerm2');
  const id = Number(argv[0]);
  if (app.windows.id().indexOf(id) < 0) return 'missing';
  app.windows.byId(id).close();
  return 'closed';
}`

func runJS(ctx context.Context, t *testing.T, src string, args ...string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "s.js")
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, "osascript", append([]string{"-l", "JavaScript", script}, args...)...).CombinedOutput() // #nosec G204 -- test script, data as argv
	if err != nil {
		t.Fatalf("osascript: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestOpenTabLive(t *testing.T) {
	if os.Getenv("AIMUX_LIVE_OPENTAB") != "1" {
		t.Skip("AIMUX_LIVE_OPENTAB=1 is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if room := runJS(ctx, t, roomJS, strconv.Itoa(MaxPanes)); room != "0" {
		t.Skipf("%s aimux window(s) have room; the test would add panes there", room)
	}
	o := &Opener{}
	window := 0
	for i := 1; i <= 3; i++ {
		r, err := o.open(ctx, "aimux live test "+strconv.Itoa(i), []string{"/bin/echo", "aimux-live"}, os.TempDir())
		if r.Window > 0 && window == 0 {
			window = r.Window
			defer func() {
				time.Sleep(2 * time.Second) // let the lines run so they can be seen
				if got := runJS(context.Background(), t, closeWindowJS, strconv.Itoa(window)); got != "closed" {
					t.Errorf("close window %d: %q", window, got)
				}
			}()
		}
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if r.Window != window || r.Pane != i {
			t.Fatalf("open %d: window %d pane %d, want window %d pane %d", i, r.Window, r.Pane, window, i)
		}
	}
	t.Logf("opened 3 panes in window %d", window)
}
