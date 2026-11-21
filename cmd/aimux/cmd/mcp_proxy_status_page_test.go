package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/pflag"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// pageReport is a proxy with one server of every kind: failed (github), a
// running one whose last error is recent (jira), a running one whose error
// is older than its last call (slack), idle, four never started, and one
// whose entry left the Claude config (minutes).
func pageReport() mcpproxy.StatusReport {
	srv := func(name string, st mcpproxy.ServerState, tools int) mcpproxy.ServerReport {
		return mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: name, State: st}, Tools: tools, Endpoint: true}
	}
	slack := srv("slack", mcpproxy.StateRunning, 12)
	slack.LastCall, slack.LastError, slack.LastErrorAt = statusNow.Add(-2*time.Minute), "write: broken pipe", statusNow.Add(-time.Hour)
	slack.LastOK, slack.ErrorPast = slack.LastCall, ptr(true)
	github := srv("github", mcpproxy.StateFailed, 0)
	github.LastError, github.LastErrorAt = "exec: gh-mcp: not found", statusNow.Add(-12*time.Minute)
	github.StderrTail = []string{"line one", "line two"}
	jira := srv("jira", mcpproxy.StateRunning, 5)
	jira.LastCall, jira.LastError, jira.LastErrorAt = statusNow.Add(-4*time.Minute), "timed out after 1m0s", statusNow.Add(-3*time.Minute)
	jira.ErrorPast = ptr(false)
	idle := srv("nanobanana-image-creation", mcpproxy.StateIdleStopped, 4)
	idle.LastCall = statusNow.Add(-30 * time.Minute)
	return mcpproxy.StatusReport{Running: true, PID: 99, StartedAt: statusNow.Add(-3 * time.Hour), Port: 7381, Version: "v0.15.0",
		Servers: []mcpproxy.ServerReport{slack, github, jira, idle,
			srv("pypi", mcpproxy.StateNotStarted, 0), srv("brief", mcpproxy.StateNotStarted, 13),
			srv("deepwiki", mcpproxy.StateNotStarted, 3), srv("chrome-devtools", mcpproxy.StateNotStarted, 20),
			srv("minutes", mcpproxy.StateNotStarted, 0)}}
}

func pageInstall() *mcpproxy.InstallState {
	return &mcpproxy.InstallState{State: mcpproxy.InstallInPlace, Hooks: true, UpgradePending: true, DaemonVersion: "v0.15.0",
		Moved:   []string{"brief", "chrome-devtools", "deepwiki", "github", "jira", "minutes", "nanobanana-image-creation", "pypi", "slack"},
		Removed: []string{"minutes"},
		Skipped: []string{"pp"},
		KeptDirect: []mcpproxy.KeptDirect{{Server: "Amplitude", Reason: mcpproxy.ReasonNeedsOAuth},
			{Server: "brand", Reason: mcpproxy.ReasonNeedsOAuth}, {Server: "brand", Reason: mcpproxy.ReasonNeedsOAuth}}}
}

func pageGateway() (toolgateway.ControlStatus, error) {
	return toolgateway.ControlStatus{StartedAt: statusNow.Add(-17 * time.Hour), Backends: make([]string, 5)}, nil
}

func page() statusPage {
	rep := pageReport()
	return statusPage{addr: "127.0.0.1:7381", rep: &rep, install: pageInstall(), claudeConfig: "/home/x/.claude.json",
		cliVersion: "v0.16.0", gateway: pageGateway, now: statusNow, width: 120}
}

func render(p statusPage) string {
	var out bytes.Buffer
	printStatusPage(&out, p)
	return out.String()
}

// messages is the LEVEL/MESSAGE/FIX table with MESSAGE padded to msgW
// cells; a row without a fix ends after its message.
func messages(msgW int, rows ...[3]string) string {
	var b strings.Builder
	line := func(level, msg, fix string) {
		if fix == "" {
			fmt.Fprintf(&b, "%-7s  %s\n", level, msg)
			return
		}
		fmt.Fprintf(&b, "%-7s  %s  %s\n", level, msg+strings.Repeat(" ", max(msgW-runewidth.StringWidth(msg), 0)), fix)
	}
	line("LEVEL", "MESSAGE", "FIX")
	for _, r := range rows {
		line(r[0], r[1], r[2])
	}
	return b.String()
}

const goldenTop = `COMPONENT  STATE    VERSION  UPTIME  ADDRESS
proxy      running  v0.15.0  3h      127.0.0.1:7381
gateway    running  -        17h     0 leases, 5 backends

SERVER                     STATE    TOOLS  LAST CALL
github                     failed   0      never
jira                       running  5      4m ago
slack                      running  12     2m ago
nanobanana-image-creation  idle     4      30m ago
(4 ready, 1 failed, 1 warning, 1 removed; --all lists them)

SERVER  AGE  MESSAGE
github  12m  exec: gh-mcp: not found
             line one
             line two
jira    3m   timed out after 1m0s

`

var pageMessages = [][3]string{
	{"WARNING", "upgrade restart pending (proxy v0.15.0, cli v0.16.0)", "aimux mcp proxy restart"},
	{"INFO", "9 servers behind the proxy", ""},
	{"INFO", "hooks on", ""},
	{"INFO", "removed from /home/x/.claude.json: minutes (still served, unused)", "aimux mcp proxy install"},
	{"INFO", "kept direct (--skip): pp", "aimux mcp proxy install --unskip pp"},
	{"INFO", "kept direct: needs OAuth (later): Amplitude, brand", ""},
}

// The default page at 120 columns: four tables, problems first, ready
// servers in the summary line, errors in their own table, one table of
// what needs you and the notes.
func TestStatusPage_Golden120(t *testing.T) {
	want := goldenTop + messages(65, pageMessages...)
	if got := render(page()); got != want {
		t.Fatalf("page\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// At 60 columns the widest text column (MESSAGE) is cut with an ellipsis;
// FIX commands stay whole and nothing passes the width.
func TestStatusPage_Golden60(t *testing.T) {
	p := page()
	p.width = 60
	var rows [][3]string
	for _, r := range pageMessages {
		r[1] = runewidth.Truncate(r[1], 14, "…")
		rows = append(rows, r)
	}
	want := goldenTop + messages(14, rows...)
	got := render(p)
	if got != want {
		t.Fatalf("page\n--- got\n%s\n--- want\n%s", got, want)
	}
	for _, line := range strings.Split(got, "\n") {
		if n := runewidth.StringWidth(line); n > 60 {
			t.Fatalf("line of %d cells over 60: %q", n, line)
		}
	}
}

// Below 60 columns the columns stay and lines are cut at the width; only a
// fix command, on a line of its own, may pass it, whole.
func TestStatusPage_NarrowCutsLines(t *testing.T) {
	p := page()
	for _, width := range []int{40, 30} {
		p.width = width
		got := render(p)
		for _, line := range strings.Split(got, "\n") {
			if cmd := strings.TrimLeft(line, " "); strings.HasPrefix(cmd, "aimux mcp proxy ") && !strings.Contains(cmd, "…") {
				continue
			}
			if n := runewidth.StringWidth(line); n > width {
				t.Fatalf("width %d: line of %d cells: %q\n%s", width, n, line, got)
			}
		}
		for _, head := range []string{"COMPONENT  STATE", "\nSERVER  ", "\nLEVEL    MESSAGE"} {
			if !strings.Contains(got, head) {
				t.Fatalf("width %d: header %q gone:\n%s", width, head, got)
			}
		}
	}
}

// --all adds the ready servers (STATE ready) and the removed ones as rows,
// shows a past error dim and marked old, and drops the --all hint.
func TestStatusPage_AllListsEveryServer(t *testing.T) {
	p := page()
	p.all = true
	got := render(p)
	for _, want := range []string{
		"SERVER                     STATE    TOOLS  LAST CALL\n",
		"brief                      ready    13     never\n",
		"chrome-devtools            ready    20     never\n",
		"minutes                    removed  0      never\n",
		"slack   1h   (old) write: broken pipe\n",
		"(4 ready, 1 failed, 1 warning, 1 removed)\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("--all page lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "\nminutes ") < strings.Index(got, "\npypi ") {
		t.Fatalf("removed server not after the ready ones:\n%s", got)
	}
}

// Piped (no colour, width 0): no escape codes and nothing cut; with colour
// the text and alignment are the same once the escapes are gone.
func TestStatusPage_NoTTYIsPlain(t *testing.T) {
	p := page()
	p.width = 0
	rep := pageReport()
	rep.Servers[1].LastError = strings.Repeat("x", 200)
	p.rep = &rep
	plain := render(p)
	if strings.Contains(plain, "\x1b") || !strings.Contains(plain, strings.Repeat("x", 200)) {
		t.Fatalf("piped page has escapes or a cut error:\n%s", plain)
	}
	p.color = true
	if got := ansiRE.ReplaceAllString(render(p), ""); got != plain {
		t.Fatalf("colour changed the text or alignment:\n%s", got)
	}
}

func TestStatusPage_Colors(t *testing.T) {
	p := page()
	p.color = true
	got := render(p)
	for _, want := range []string{
		"\x1b[1mCOMPONENT", "\x1b[1mSERVER", "\x1b[1mLEVEL",
		"\x1b[32mrunning", "\x1b[31mfailed", "\x1b[2midle",
		"\x1b[33mrunning", // jira: live, with a recent error
		"\x1b[31mexec: gh-mcp: not found", "\x1b[33mtimed out after 1m0s",
		"\x1b[33mWARNING", "\x1b[2mINFO",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("coloured page lacks %q:\n%q", want, got)
		}
	}
}

// The page is tables: no bullets, glyphs, "!" lines or " · " separators.
func TestStatusPage_NoProseMarkers(t *testing.T) {
	got := render(page())
	for _, bad := range []string{"·", "●", "○", "◐", "\n! "} {
		if strings.Contains(got, bad) {
			t.Fatalf("page has %q:\n%s", bad, got)
		}
	}
}

// A server install kept direct with a recorded reason is a note with that
// reason, never a warning to run install.
func TestStatusPage_KeptDirectWithReasonIsInfo(t *testing.T) {
	p := page()
	st := pageInstall()
	st.KeptDirect = append(st.KeptDirect, mcpproxy.KeptDirect{Server: "productpages", Reason: "the server refused its headers (401)"})
	p.install = st
	got := render(p)
	if !strings.Contains(got, "\nINFO     kept direct: the server refused its headers (401): productpages\n") {
		t.Fatalf("productpages not an INFO kept-direct row:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "WARNING") && strings.Contains(line, "productpages") {
			t.Fatalf("productpages in a warning: %q", line)
		}
	}
}

func TestStatusPage_NotRunning(t *testing.T) {
	want := "COMPONENT  STATE        VERSION  UPTIME  ADDRESS\nproxy      not running  -        -       127.0.0.1:7381\n"
	if got := render(statusPage{addr: "127.0.0.1:7381", now: statusNow}); got != want {
		t.Fatalf("not running, not installed:\n%q\nwant\n%q", got, want)
	}
	p := page()
	p.rep, p.color = nil, true
	got := render(p)
	if !strings.Contains(got, "\x1b[31mnot running") || !strings.Contains(got, "hooks on") || strings.Contains(got, "SERVER") {
		t.Fatalf("not running while installed:\n%q", got)
	}
	p.noAnswer, p.color = true, false
	if got := render(p); !strings.Contains(got, "\nproxy      no status") {
		t.Fatalf("no answer:\n%s", got)
	}
}

func TestStatusPage_InstallWarnings(t *testing.T) {
	st := &mcpproxy.InstallState{State: mcpproxy.InstallDrifted, Moved: []string{"slack"}, Drifted: []string{"slack"}, NotProxied: []string{"pypi"}}
	got := render(statusPage{addr: "a", install: st, now: statusNow})
	for _, want := range []string{
		"WARNING  not behind the proxy: pypi",
		"WARNING  hooks off: servers you add later stay direct",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	// Drift's two steps: uninstall on its row, then install on the next,
	// both in the FIX column.
	lines := strings.Split(got, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "WARNING  drifted: slack (entry is no longer the proxy URL)") {
			next := lines[i+1]
			col := strings.Index(line, "aimux mcp proxy uninstall")
			if !strings.HasSuffix(line, "aimux mcp proxy uninstall") || !strings.HasPrefix(next, "         then ") ||
				strings.Index(next, "aimux mcp proxy install") != col || !strings.HasSuffix(next, "aimux mcp proxy install") {
				t.Fatalf("drift steps:\n%s\n%s", line, next)
			}
		}
	}
	got = render(statusPage{addr: "a", install: &mcpproxy.InstallState{State: mcpproxy.InstallNotInstalled}, now: statusNow})
	if !strings.Contains(got, "WARNING  not installed: Claude Code's servers are direct  aimux mcp proxy install\n") {
		t.Fatalf("not installed:\n%s", got)
	}
	got = render(statusPage{addr: "a", installErr: errors.New("parse Claude config"), now: statusNow})
	if !strings.Contains(got, "WARNING  install state unknown: parse Claude config\n") {
		t.Fatalf("install error:\n%s", got)
	}
}

// The gateway row and its warnings: not running, unknown, down backends.
func TestStatusPage_Gateway(t *testing.T) {
	p := page()
	p.gateway = func() (toolgateway.ControlStatus, error) {
		return toolgateway.ControlStatus{}, toolgateway.ErrGatewayNotRunning
	}
	got := render(p)
	if !strings.Contains(got, "\ngateway    not running  -        -       Workspace turns fail\n") ||
		!strings.Contains(got, "WARNING  tool gateway not running: Workspace turns fail") || !strings.Contains(got, "aimux mcp proxy gateway install\n") {
		t.Fatalf("gateway not running:\n%s", got)
	}
	p.gateway = func() (toolgateway.ControlStatus, error) {
		return toolgateway.ControlStatus{}, errors.New("dial: refused")
	}
	if got := render(p); !strings.Contains(got, "\ngateway    unknown ") || !strings.Contains(got, "WARNING  tool gateway: dial: refused") {
		t.Fatalf("gateway unknown:\n%s", got)
	}
	p.gateway = func() (toolgateway.ControlStatus, error) {
		return toolgateway.ControlStatus{Leases: 1, Backends: []string{"jira", "wiki", "slack"}, Down: []string{"wiki", "slack"},
			StartedAt: statusNow.Add(-time.Hour), Checked: map[string]time.Time{"wiki": statusNow.Add(-40 * time.Second)}}, nil
	}
	got = render(p)
	if !strings.Contains(got, "\ngateway    running  -        1h      1 lease, 3 backends\n") ||
		strings.Count(got, "gateway backend") != 1 || !strings.Contains(got, "WARNING  2 gateway backends down: wiki (checked 40s ago), slack") {
		t.Fatalf("down backends not one warning:\n%s", got)
	}
	// The worker's version is its tool-policy snapshot, not a build: VERSION
	// says -, and the snapshot (short) goes with the details.
	p.gateway = func() (toolgateway.ControlStatus, error) {
		return toolgateway.ControlStatus{Version: "sha256:33b556ac47673560952dbe03f13d29a9b54f5b5cf1b7801ecbbde5246926b430"}, nil
	}
	if got := render(p); !strings.Contains(got, "\ngateway    running  -        -       0 leases, 0 backends, snapshot sha256:33b556ac4767\n") {
		t.Fatalf("snapshot not in the details:\n%s", got)
	}
	p.gateway = nil
	if got := render(p); strings.Contains(got, "\ngateway ") {
		t.Fatalf("no gateway asked, but a row:\n%s", got)
	}
}

// A never-started server the proxy has no route for is a problem row and a
// warning with the fix, not ready.
func TestStatusPage_NoRoute(t *testing.T) {
	p := page()
	rep := pageReport()
	rep.Servers[4].Endpoint = false // pypi
	p.rep = &rep
	got := render(p)
	for _, want := range []string{
		"jira                       running   5      4m ago\npypi                       no route  0      never\n",
		"(3 ready, 1 failed, 1 warning, 1 no route, 1 removed; --all lists them)\n",
		"WARNING  1 no route: pypi (no cached tools)",
		"  aimux mcp proxy refresh\n         then",
		"  aimux mcp proxy restart\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("page lacks %q:\n%s", want, got)
		}
	}
	// A proxy without the launchd job (run by hand): restart cannot work,
	// so the second step is serve, again with the original flags.
	p.handRun = true
	got = render(p)
	if strings.Contains(got, "aimux mcp proxy restart\n") && !strings.Contains(got, "upgrade restart pending") ||
		!strings.Contains(got, "then, again with your original flags") || !strings.Contains(got, "  aimux mcp proxy serve\n") {
		t.Fatalf("hand-run proxy: want refresh, then serve again:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "then") && strings.HasSuffix(line, "aimux mcp proxy restart") {
			t.Fatalf("hand-run proxy still told to restart: %q", line)
		}
	}
}

// A probe that fails on a server that keeps running is counted apart from
// failed servers; its error is in the errors table.
func TestStatusPage_ProbeFailureOnARunningServer(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Servers: []mcpproxy.ServerReport{
		{ServerStatus: mcpproxy.ServerStatus{Server: "slack", State: mcpproxy.StateRunning}, ProbeError: "tools/list: method not found"}}}
	got := render(statusPage{addr: "a", rep: &rep, now: statusNow})
	for _, want := range []string{"\nslack   running  0      never\n", "(0 ready, 0 failed, 1 probe failed)\n", "\nslack   -    tools/list: method not found\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
}

// A probe the owner just asked for shows its failure, also for a server
// whose entry left the Claude config.
func TestStatusPage_RemovedServerProbeFailureShows(t *testing.T) {
	p := page()
	rep := pageReport()
	rep.Servers[8].ProbeError = "exec: minutes: not found"
	p.rep = &rep
	got := render(p)
	if !strings.Contains(got, "\nminutes  ") || !strings.Contains(got, "\nminutes  -    exec: minutes: not found\n") {
		t.Fatalf("probe failure of a removed server hidden:\n%s", got)
	}
}

// A removed server's entry left the Claude config, but a failed start of
// its backend is current: --all keeps its error red with the stderr.
func TestStatusPage_AllRemovedServerKeepsCurrentFailure(t *testing.T) {
	p := page()
	p.all, p.color = true, true
	rep := pageReport()
	m := &rep.Servers[8]
	m.State, m.LastError, m.LastErrorAt, m.StderrTail = mcpproxy.StateFailed, "exec: minutes: not found", statusNow.Add(-time.Minute), []string{"no such file"}
	p.rep = &rep
	got := render(p)
	for _, want := range []string{"removed", "\x1b[31mexec: minutes: not found", "\x1b[2mno such file"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, "(old) exec: minutes") {
		t.Fatalf("current failure marked old:\n%s", got)
	}
}

// An error the daemon reports as past is never red, and hidden from the
// default page; one with nothing recovered since is a warning.
func TestStatusPage_OldErrorNeverRed(t *testing.T) {
	p := page()
	p.color, p.all = true, true
	got := render(p)
	if !strings.Contains(got, "\x1b[2m(old) write: broken pipe") || strings.Contains(got, "\x1b[31m(old)") {
		t.Fatalf("old error not dim:\n%q", got)
	}
	p.all, p.color = false, false
	if got := render(p); strings.Contains(got, "broken pipe") {
		t.Fatalf("default page shows a past error:\n%s", got)
	}
	rep := pageReport()
	rep.Servers[0].ErrorPast = ptr(false)
	p.rep = &rep
	if got := render(p); !strings.Contains(got, "\nslack   1h   write: broken pipe\n") || strings.Contains(got, "(old)") {
		t.Fatalf("an error with no answer since is not shown as recent:\n%s", got)
	}
}

func ptr[T any](v T) *T { return &v }

// A daemon from before errorPast sends none: an error it kept is old
// unless it came after the server's last answer, never a warning on a guess.
func TestStatusPage_OlderDaemonErrorsAreOld(t *testing.T) {
	srv := func(lastOK time.Time) mcpproxy.ServerReport {
		return mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: "slack", State: mcpproxy.StateRunning,
			LastError: "write: broken pipe", LastErrorAt: statusNow.Add(-time.Hour), LastOK: lastOK}}
	}
	for _, c := range []struct {
		name    string
		lastOK  time.Time
		warning bool
	}{
		{"no lastOK either", time.Time{}, false},
		{"answered after the error", statusNow.Add(-time.Minute), false},
		{"error after the last answer", statusNow.Add(-2 * time.Hour), true},
	} {
		rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Servers: []mcpproxy.ServerReport{srv(c.lastOK)}}
		got := render(statusPage{addr: "a", rep: &rep, now: statusNow})
		if strings.Contains(got, "1 warning") != c.warning {
			t.Fatalf("%s: warning=%v wanted:\n%s", c.name, c.warning, got)
		}
	}
}

// The daemon's version comes from whatever answers on the port: control
// characters never reach the terminal.
func TestStatusPage_VersionIsPrintable(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Version: "v1\x1b[2J"}
	if got := render(statusPage{addr: "a", rep: &rep, now: statusNow}); strings.Contains(got, "\x1b") || !strings.Contains(got, "v1?[2J") {
		t.Fatalf("version: %q", got)
	}
}

// Width is measured in terminal cells: a Japanese error is cut to fit and
// wide names keep the STATE column aligned.
func TestStatusPage_WideCharactersFit(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Servers: []mcpproxy.ServerReport{
		{ServerStatus: mcpproxy.ServerStatus{Server: "日本語サーバー", State: mcpproxy.StateFailed,
			LastError: strings.Repeat("接続できません", 20), LastErrorAt: statusNow}},
		{ServerStatus: mcpproxy.ServerStatus{Server: "slack", State: mcpproxy.StateFailed, LastErrorAt: statusNow}}}}
	for _, width := range []int{80, 60} {
		got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: width})
		for _, line := range strings.Split(got, "\n") {
			if n := runewidth.StringWidth(line); n > width {
				t.Fatalf("width %d: line of %d cells: %q\n%s", width, n, line, got)
			}
		}
		col := func(name string) int {
			for _, line := range strings.Split(got, "\n") {
				if strings.HasPrefix(line, name+" ") && strings.Contains(line, "failed") {
					return runewidth.StringWidth(line[:strings.Index(line, "failed")])
				}
			}
			t.Fatalf("no row for %s:\n%s", name, got)
			return 0
		}
		if a, b := col("日本語サーバー"), col("slack"); a != b {
			t.Fatalf("width %d: state column at cell %d and %d:\n%s", width, a, b, got)
		}
	}
}

// Names cut to fit that would read the same keep a short ending each.
func TestStatusPage_CutNamesStayDistinct(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow}
	for _, n := range []string{"playwright-browser-alpha-long-name-a", "playwright-browser-alpha-long-name-o", "slack"} {
		rep.Servers = append(rep.Servers, mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: n, State: mcpproxy.StateRunning}})
	}
	got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: 44})
	var names []string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "playw") {
			names = append(names, strings.Fields(line)[0])
		}
	}
	if len(names) != 2 || names[0] == names[1] || !strings.Contains(names[0], "…") {
		t.Fatalf("cut names %q:\n%s", names, got)
	}
	if got := cutDistinct([]string{"slack", "abcdefgh-one", "abcdefgh-two", "abcdefgh-two"}, 8); got[0] != "slack" ||
		got[1] == got[2] || got[2] != got[3] {
		t.Fatalf("cutDistinct %q", got)
	}
}

// Colour only on a terminal: a buffer or a file never gets it.
func TestStatusColor_OnlyOnATerminal(t *testing.T) {
	if statusColor(&bytes.Buffer{}) {
		t.Fatal("colour for a buffer")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if statusColor(f) || termWidth(f) != 0 {
		t.Fatal("colour or a width for a file")
	}
}

// `aimux mcp proxy gateway status` keeps its one styled line.
func TestGatewayStatusStyled(t *testing.T) {
	now := statusNow
	st := toolgateway.ControlStatus{Leases: 2, Backends: []string{"jira", "slack", "wiki"}, Down: []string{"slack", "wiki"},
		StartedAt: now.Add(-17 * time.Hour), Checked: map[string]time.Time{"wiki": now.Add(-40 * time.Second)}}
	line, ok := gatewayStatusStyled(st, nil, now, false, 0)
	if want := "tool gateway ● running 17h · 2 leases · 3 backends · 2 down: slack, wiki (checked 40s ago)"; line != want || !ok {
		t.Fatalf("line\n got %q\nwant %q", line, want)
	}
	line, _ = gatewayStatusStyled(st, nil, now, true, 0)
	if !strings.Contains(line, "\x1b[32m● running") || !strings.Contains(line, "\x1b[31m2 down: slack, wiki") {
		t.Fatalf("coloured line %q", line)
	}
	st.Down, st.Leases, st.Backends = nil, 1, []string{"jira"}
	if line, _ = gatewayStatusStyled(st, nil, now, false, 0); line != "tool gateway ● running 17h · 1 lease · 1 backend" {
		t.Fatalf("singular line %q", line)
	}
	line, ok = gatewayStatusStyled(st, toolgateway.ErrGatewayNotRunning, now, false, 0)
	if ok || line != "tool gateway ● not running · Workspace turns fail · run `aimux mcp proxy gateway install`" {
		t.Fatalf("not running line %q", line)
	}
	for _, color := range []bool{false, true} {
		line, _ = gatewayStatusStyled(st, toolgateway.ErrGatewayNotRunning, now, color, 60)
		if got := ansiRE.ReplaceAllString(line, ""); got != "tool gateway ● not running · Workspace turns fail\n  run `aimux mcp proxy gateway install`" {
			t.Fatalf("color=%v: wrapped line %q", color, got)
		}
	}
	line, ok = gatewayStatusStyled(st, errors.New("dial: refused"), now, false, 0)
	if ok || line != "tool gateway ? unknown: dial: refused" {
		t.Fatalf("unknown line %q", line)
	}
}

// A long list of down backends wraps between backends, each keeping its
// check age.
func TestGatewayStatusStyled_DownListWraps(t *testing.T) {
	st := toolgateway.ControlStatus{Backends: []string{"a", "b", "c"}, Down: []string{"jira-cloud", "slack-workspace", "wiki-internal"},
		Checked: map[string]time.Time{"jira-cloud": statusNow.Add(-40 * time.Second), "slack-workspace": statusNow.Add(-time.Minute), "wiki-internal": statusNow.Add(-2 * time.Minute)}}
	for _, color := range []bool{false, true} {
		line, _ := gatewayStatusStyled(st, nil, statusNow, color, 60)
		want := "tool gateway ● running · 0 leases · 3 backends\n" +
			"  3 down: jira-cloud (checked 40s ago),\n" +
			"    slack-workspace (checked 1m ago),\n" +
			"    wiki-internal (checked 2m ago)"
		if got := ansiRE.ReplaceAllString(line, ""); got != want {
			t.Fatalf("color=%v:\n got %q\nwant %q", color, got, want)
		}
	}
}

// The separator is measured like the text: where the locale makes " · "
// wider than three cells, the gateway line still fits.
func TestJoinWrapped_EastAsianSeparatorFits(t *testing.T) {
	old := runewidth.DefaultCondition.EastAsianWidth
	runewidth.DefaultCondition.EastAsianWidth = true
	defer func() { runewidth.DefaultCondition.EastAsianWidth = old }()
	parts := []string{"tool gateway ● running", "2 leases", "3 backends", "1 down"}
	for width := 30; width <= 80; width++ {
		for _, line := range joinWrapped(parts, false, width) {
			if n := runewidth.StringWidth(line); n > width {
				t.Fatalf("line of %d cells over %d: %q", n, width, line)
			}
		}
	}
}

// Every step of a fix stays visible and whole: follow-up steps are rows of
// their own ("then"), MESSAGE is cut instead of FIX, and a FIX too wide to
// share a line goes on a line of its own, uncut.
func TestStatusPage_FixStepsNeverCut(t *testing.T) {
	st := &mcpproxy.InstallState{State: mcpproxy.InstallDrifted, Hooks: true, Moved: []string{"slack"},
		Drifted: []string{"slack-with-a-rather-long-server-name-that-drifted"}}
	for _, width := range []int{80, 60, 40} {
		got := render(statusPage{addr: "a", install: st, now: statusNow, width: width})
		for _, cmd := range []string{"aimux mcp proxy uninstall", "aimux mcp proxy install"} {
			if !strings.Contains(got, cmd+"\n") {
				t.Fatalf("width %d: fix step %q not whole on its line:\n%s", width, cmd, got)
			}
		}
		if strings.Index(got, "aimux mcp proxy uninstall") > strings.Index(got, "aimux mcp proxy install\n") {
			t.Fatalf("width %d: steps out of order:\n%s", width, got)
		}
	}
	// A skip list too long to share a line with any message: the command
	// gets a line of its own, whole, past the width if it must be.
	long := &mcpproxy.InstallState{State: mcpproxy.InstallInPlace, Hooks: true, Moved: []string{"a"},
		Skipped: []string{"first-skipped-server", "second-skipped-server", "third-skipped-server"}}
	got := render(statusPage{addr: "a", install: long, now: statusNow, width: 60})
	want := "aimux mcp proxy install --unskip first-skipped-server --unskip second-skipped-server --unskip third-skipped-server\n"
	if !strings.Contains(got, "\n       "+want) { // under MESSAGE: LEVEL is 5 cells with INFO rows only
		t.Fatalf("long fix not on a line of its own, whole:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if n := runewidth.StringWidth(line); n > 60 && !strings.Contains(line, "aimux mcp proxy install --unskip") {
			t.Fatalf("a line other than the long fix passes 60: %q", line)
		}
	}
}

// A very long server name in the errors table is cut so AGE and some of
// MESSAGE always show at 60 columns.
func TestStatusPage_ErrorsKeepAgeAndMessage(t *testing.T) {
	name := strings.Repeat("n", 60)
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Servers: []mcpproxy.ServerReport{
		{ServerStatus: mcpproxy.ServerStatus{Server: name, State: mcpproxy.StateFailed, LastError: "exec: not found", LastErrorAt: statusNow.Add(-12 * time.Minute)}}}}
	got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: 60})
	var row string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "nnnn") && strings.Contains(line, "12m") {
			row = line
		}
	}
	if row == "" || !strings.Contains(row, "…  12m  exec: n") || runewidth.StringWidth(row) > 60 {
		t.Fatalf("errors row %q: want the name cut, AGE and MESSAGE shown, within 60:\n%s", row, got)
	}
}

// Skipped names come from ~/.claude.json: the --unskip command quotes each
// for the shell, so it runs as written and the flag gets every name intact.
func TestStatusPage_UnskipCommandQuotesNames(t *testing.T) {
	names := []string{"pp", "my server$HOME", "it's", "team,dev", `say "hi"`}
	st := &mcpproxy.InstallState{State: mcpproxy.InstallInPlace, Hooks: true, Moved: []string{"a"}, Skipped: names}
	got := render(statusPage{addr: "a", install: st, now: statusNow})
	i := strings.Index(got, "aimux mcp proxy install --unskip")
	if i < 0 {
		t.Fatalf("no unskip command:\n%s", got)
	}
	cmd := strings.TrimSpace(got[i : i+strings.Index(got[i:], "\n")])
	// The real shell splits the command; the real flag parser reads it.
	argv, err := exec.Command("sh", "-c", "set -- "+strings.TrimPrefix(cmd, "aimux mcp proxy install ")+`; for a in "$@"; do printf '%s\0' "$a"; done`).Output() // #nosec G204 -- the test runs the page's own command through a real shell on purpose
	if err != nil {
		t.Fatalf("shell could not run %q: %v", cmd, err)
	}
	fs := pflag.NewFlagSet("install", pflag.ContinueOnError)
	unskip := fs.StringSlice("unskip", nil, "")
	if err := fs.Parse(strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*unskip, "|") != strings.Join(names, "|") {
		t.Fatalf("--unskip parsed as %q from %q, want %q", *unskip, cmd, names)
	}
}

// A proxy installed on another port than the default: every install fix
// carries --port, or install would refuse the default port.
func TestStatusPage_InstallFixesCarryThePort(t *testing.T) {
	st := &mcpproxy.InstallState{State: mcpproxy.InstallInPlace, Hooks: false, Moved: []string{"a"}, NotProxied: []string{"x"}, Skipped: []string{"pp"}}
	got := render(statusPage{addr: "127.0.0.1:8000", install: st, installPort: 8000, now: statusNow})
	for _, want := range []string{"aimux mcp proxy install --port 8000 --unskip pp\n", "not behind the proxy: x", "aimux mcp proxy install --port 8000\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "aimux mcp proxy install\n") || strings.Contains(got, "install --unskip") {
		t.Fatalf("an install fix without --port:\n%s", got)
	}
	// The default port needs no flag.
	if got := render(statusPage{addr: "a", install: st, installPort: 7381, now: statusNow}); strings.Contains(got, "--port") {
		t.Fatalf("--port for the default port:\n%s", got)
	}
}
