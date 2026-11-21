package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
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

func page() statusPage {
	rep := pageReport()
	return statusPage{addr: "127.0.0.1:7381", rep: &rep, install: pageInstall(), claudeConfig: "/home/x/.claude.json",
		upgradeText: "upgrade restart pending: the running proxy is aimux v0.15.0, this binary is v0.16.0; restart it when convenient with `aimux mcp proxy restart`",
		gateway:     func() string { return "tool gateway ● running 17h · 0 leases · 5 backends" }, now: statusNow, width: 80}
}

func render(p statusPage) string {
	var out bytes.Buffer
	printStatusPage(&out, p)
	return out.String()
}

const goldenCompact = `MCP proxy ● running · pid 99 · aimux v0.15.0 · up 3h · 127.0.0.1:7381
9 servers: 1 failed · 1 warning · 2 running · 1 idle · 4 ready · 1 removed
! upgrade restart pending: the running proxy is aimux v0.15.0, this binary is
  v0.16.0; restart it when convenient with ` + "`aimux mcp proxy restart`" + `

  github                     ● failed 12m ago  0 tools   never called
      exec: gh-mcp: not found
      line one
      line two
  jira                       ● running         5 tools   called 4m ago
      timed out after 1m0s · 3m ago

  slack                      ● running         12 tools  called 2m ago
  nanobanana-image-creation  ○ idle            4 tools   called 30m ago

  ○ 4 ready, start on first call: brief, chrome-devtools, deepwiki, pypi

tool gateway ● running 17h · 0 leases · 5 backends

· 9 servers behind the proxy; change one with ` + "`aimux mcp proxy edit <server>`" + `
· hooks on: a server you add in a session moves behind the proxy; servers still
  direct are named when a session starts
· removed from /home/x/.claude.json: minutes (still served by the proxy, unused;
  ` + "`aimux mcp proxy install`" + ` takes them off)
· kept direct: pp (` + "`aimux mcp proxy install --unskip <name>`" + ` moves one)
· kept direct: needs OAuth (later): Amplitude, brand
`

// The default page: problems first with their error, then the active
// servers, the never-started ones on one line, the gateway, dim notes.
func TestStatusPage_GoldenCompact(t *testing.T) {
	if got := render(page()); got != goldenCompact {
		t.Fatalf("compact page\n--- got\n%s\n--- want\n%s", got, goldenCompact)
	}
}

// --all lists every server, never-started and removed ones too, and shows
// an old error dim and marked instead of hiding it.
func TestStatusPage_AllListsEveryServer(t *testing.T) {
	p := page()
	p.all = true
	got := render(p)
	for _, want := range []string{
		"  brief                      ○ ready           13 tools  never called\n",
		"  minutes                    ○ removed         0 tools   never called\n",
		"  slack                      ● running         12 tools  called 2m ago\n      (old) write: broken pipe · 1h ago\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("--all page lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "start on first call: brief") {
		t.Fatalf("--all still collapses the never-started servers:\n%s", got)
	}
	if strings.Index(got, "  minutes ") < strings.Index(got, "  pypi ") {
		t.Fatalf("removed server not after the never-started ones:\n%s", got)
	}
}

// An error the daemon reports as past (answered or started well since, in
// the error's session) is past: never red, and hidden from the default page.
func TestStatusPage_OldErrorNeverRed(t *testing.T) {
	p := page()
	p.color, p.all = true, true
	got := render(p)
	if strings.Contains(got, "\x1b[31m      (old)") || !strings.Contains(got, "\x1b[2m      (old) write: broken pipe") {
		t.Fatalf("old error not dim:\n%q", got)
	}
	p.all = false
	if got := render(p); strings.Contains(got, "broken pipe") {
		t.Fatalf("default page shows a past error:\n%s", got)
	}
	// The daemon says nothing recovered in the error's session (a call came
	// in but got no answer, or another session answered): still a warning.
	rep := pageReport()
	rep.Servers[0].ErrorPast = ptr(false)
	p.rep = &rep
	if got := render(p); !strings.Contains(got, "      write: broken pipe · 1h ago") || strings.Contains(got, "(old)") {
		t.Fatalf("an error with no answer since is not shown as recent:\n%s", got)
	}
}

func TestStatusPage_ColorsSignalState(t *testing.T) {
	p := page()
	p.color = true
	got := render(p)
	for _, want := range []string{
		"\x1b[32m● running\x1b[0m", // header
		"\x1b[31m1 failed\x1b[0m", "\x1b[33m1 warning\x1b[0m", "\x1b[32m2 running\x1b[0m", "\x1b[2m4 ready\x1b[0m",
		"\x1b[31m● failed 12m ago",                    // failed row
		"\x1b[31m      exec: gh-mcp: not found",       // its error
		"\x1b[33m      timed out after 1m0s · 3m ago", // a recent error on a live server
		"\x1b[2m○ idle",
		"\x1b[33m! upgrade restart pending",
		"\x1b[2m· kept direct: needs OAuth (later): Amplitude, brand",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("coloured page lacks %q:\n%q", want, got)
		}
	}
	if plain := ansiRE.ReplaceAllString(got, ""); plain != goldenCompact {
		t.Fatalf("colour changed the text or alignment:\n%s", plain)
	}
	if strings.Contains(render(page()), "\x1b") {
		t.Fatal("plain page carries escape codes")
	}
}

// Long errors end in an ellipsis within the terminal width; piped output
// (width 0) keeps them whole.
func TestStatusPage_TruncatesToWidth(t *testing.T) {
	p := page()
	rep := pageReport()
	rep.Servers[1].LastError = strings.Repeat("x", 200)
	p.rep = &rep
	got := render(p)
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > 80 {
			t.Fatalf("line of %d runes over width 80: %q", n, line)
		}
	}
	if !strings.Contains(got, "x…\n") {
		t.Fatalf("long error not cut with an ellipsis:\n%s", got)
	}
	p.width = 0
	if !strings.Contains(render(p), strings.Repeat("x", 200)) {
		t.Fatal("piped output cut the error")
	}
}

// The never-started line wraps under itself, never past the width.
func TestStatusPage_NotStartedWraps(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow}
	for _, n := range []string{"alpha-server", "bravo-server", "charlie-server", "delta-server", "echo-server", "foxtrot-server", "golf-server"} {
		rep.Servers = append(rep.Servers, mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: n, State: mcpproxy.StateNotStarted}, Endpoint: true})
	}
	got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: 60})
	want := "  ○ 7 ready, start on first call: alpha-server,\n" +
		"    bravo-server, charlie-server, delta-server, echo-server,\n" +
		"    foxtrot-server, golf-server\n"
	if !strings.Contains(got, want) {
		t.Fatalf("not-started line:\n%s", got)
	}
}

func TestStatusPage_NotRunning(t *testing.T) {
	if got := render(statusPage{addr: "127.0.0.1:7381", now: statusNow}); got != "MCP proxy ○ not running on 127.0.0.1:7381\n" {
		t.Fatalf("not running, not installed: %q", got)
	}
	p := page()
	p.rep, p.color = nil, true
	got := render(p)
	if !strings.HasPrefix(got, "\x1b[1mMCP proxy\x1b[0m \x1b[31m● not running\x1b[0m on 127.0.0.1:7381\n") || !strings.Contains(got, "· hooks on") {
		t.Fatalf("not running while installed:\n%q", got)
	}
}

func TestStatusPage_InstallWarnings(t *testing.T) {
	st := &mcpproxy.InstallState{State: mcpproxy.InstallDrifted, Moved: []string{"slack"}, Drifted: []string{"slack"}, NotProxied: []string{"pypi"}}
	got := render(statusPage{addr: "a", install: st, now: statusNow})
	for _, want := range []string{"! drifted: slack", "! not behind the proxy: pypi", "! hooks off"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
	got = render(statusPage{addr: "a", install: &mcpproxy.InstallState{State: mcpproxy.InstallNotInstalled}, now: statusNow})
	if !strings.Contains(got, "! not installed: `aimux mcp proxy install` moves") {
		t.Fatalf("not installed:\n%s", got)
	}
	got = render(statusPage{addr: "a", installErr: errors.New("parse Claude config"), now: statusNow})
	if !strings.Contains(got, "! install state unknown: parse Claude config") {
		t.Fatalf("install error:\n%s", got)
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
	// On a narrow terminal it breaks at a separator, colour or not.
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

// The header and summary lines break at a separator, never past the width.
func TestStatusPage_HeaderWraps(t *testing.T) {
	p := page()
	p.width = 50
	got := render(p)
	for _, want := range []string{
		"MCP proxy ● running · pid 99 · aimux v0.15.0\n  up 3h · 127.0.0.1:7381\n",
		"9 servers: 1 failed · 1 warning · 2 running\n  1 idle · 4 ready · 1 removed\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%s", want, got)
		}
	}
}

// On a narrow terminal a long server name is cut so rows fit too.
func TestStatusPage_NarrowTerminalFits(t *testing.T) {
	p := page()
	p.width = 60
	got := render(p)
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > 60 {
			t.Fatalf("line of %d runes over width 60: %q\n%s", n, line, got)
		}
	}
	if !strings.Contains(got, "  nanobanana-im…  ○ idle            4 tools   called 30m ago\n") {
		t.Fatalf("long name not cut:\n%s", got)
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
	if !strings.Contains(got, "  minutes ") || !strings.Contains(got, "      exec: minutes: not found\n") {
		t.Fatalf("probe failure of a removed server hidden:\n%s", got)
	}
}

// Too narrow for every column even with the name cut: the row keeps the
// name and state only.
func TestStatusPage_VeryNarrowDropsColumns(t *testing.T) {
	p := page()
	p.width = 40
	gw, _ := gatewayStatusStyled(toolgateway.ControlStatus{StartedAt: statusNow.Add(-17 * time.Hour), Backends: make([]string, 5)}, nil, statusNow, false, 40)
	p.gateway = func() string { return gw }
	got := render(p)
	for _, line := range strings.Split(got, "\n") {
		// A command wider than the terminal stays whole on a line of its
		// own: it overflows, but pastes as one command.
		if words := commandWords(line); len(words) == 1 && strings.Count(words[0], "`") == 2 {
			continue
		}
		if n := len([]rune(line)); n > 40 {
			t.Fatalf("line of %d runes over width 40: %q\n%s", n, line, got)
		}
	}
	if !strings.Contains(got, "  github                ● failed 12m ago\n") || !strings.Contains(got, "  nanobanana-image-cr…  ○ idle\n") {
		t.Fatalf("narrow row:\n%s", got)
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

// A probe that fails on a server that keeps running is counted apart from
// failed servers, so the header never says failed for a running row.
func TestStatusPage_ProbeFailureOnARunningServer(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Servers: []mcpproxy.ServerReport{
		{ServerStatus: mcpproxy.ServerStatus{Server: "slack", State: mcpproxy.StateRunning}, ProbeError: "tools/list: method not found"}}}
	got := render(statusPage{addr: "a", rep: &rep, now: statusNow})
	if !strings.Contains(got, "1 servers: 0 failed · 1 probe failed · 1 running\n") {
		t.Fatalf("counts:\n%s", got)
	}
	if !strings.Contains(got, "  slack  ● running  0 tools  never called\n      tools/list: method not found\n") {
		t.Fatalf("row:\n%s", got)
	}
}

// The daemon's version comes from whatever answers on the port: control
// characters never reach the terminal.
func TestStatusPage_VersionIsPrintable(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Version: "v1\x1b[2J"}
	if got := render(statusPage{addr: "a", rep: &rep, now: statusNow}); strings.Contains(got, "\x1b") || !strings.Contains(got, "aimux v1?[2J") {
		t.Fatalf("version: %q", got)
	}
}

// A list item too long for the line after the prefix moves to the next
// line, and one too long for any line is cut, never past the width.
func TestStatusPage_LongListItemFits(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Servers: []mcpproxy.ServerReport{
		{ServerStatus: mcpproxy.ServerStatus{Server: "nanobanana-image-creation", State: mcpproxy.StateNotStarted}, Endpoint: true},
		{ServerStatus: mcpproxy.ServerStatus{Server: strings.Repeat("z", 60), State: mcpproxy.StateNotStarted}, Endpoint: true}}}
	got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: 40})
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > 40 {
			t.Fatalf("line of %d runes over width 40: %q\n%s", n, line, got)
		}
	}
	if !strings.Contains(got, "  ○ 2 ready, start on first call:\n    nanobanana-image-creation,\n    zzz") {
		t.Fatalf("list:\n%s", got)
	}
}

// A long recent error is cut, never its age.
func TestStatusPage_WarningKeepsItsAge(t *testing.T) {
	p := page()
	rep := pageReport()
	rep.Servers[2].LastError = strings.Repeat("y", 200)
	p.rep = &rep
	got := render(p)
	if !strings.Contains(got, "y… · 3m ago\n") {
		t.Fatalf("age lost:\n%s", got)
	}
}

// The proxy's part of the page is out before the tool gateway is asked, so
// a gateway that stalls never hides what the proxy said.
func TestStatusPage_ProxyPartBeforeTheGateway(t *testing.T) {
	p := page()
	var out bytes.Buffer
	p.gateway = func() string {
		if !strings.Contains(out.String(), "○ 4 ready, start on first call: brief") {
			t.Fatalf("gateway asked before the servers were printed:\n%s", out.String())
		}
		return "tool gateway ● running 17h · 0 leases · 5 backends"
	}
	printStatusPage(&out, p)
	if out.String() != goldenCompact {
		t.Fatalf("page:\n%s", out.String())
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
	for _, want := range []string{"○ removed", "\x1b[31m      exec: minutes: not found", "\x1b[2m      no such file"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lacks %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, "(old) exec: minutes") {
		t.Fatalf("current failure marked old:\n%s", got)
	}
}

func ptr[T any](v T) *T { return &v }

// A daemon from before errorPast sends none: an error it kept is old
// unless it came after the server's last answer, never a yellow warning
// on a guess.
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

// Width is measured in terminal cells: wide characters count twice, so a
// Japanese error is cut to fit and wide names keep the columns aligned.
func TestStatusPage_WideCharactersFit(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow, Servers: []mcpproxy.ServerReport{
		{ServerStatus: mcpproxy.ServerStatus{Server: "日本語サーバー", State: mcpproxy.StateFailed,
			LastError: strings.Repeat("接続できません", 20), LastErrorAt: statusNow}},
		{ServerStatus: mcpproxy.ServerStatus{Server: "slack", State: mcpproxy.StateFailed, LastErrorAt: statusNow}}}}
	for _, width := range []int{80, 40} {
		got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: width})
		for _, line := range strings.Split(got, "\n") {
			if width < 30 && !strings.Contains(line, "○") {
				continue // the header has its own minimum; this is the ready heading's test
			}
			if n := runewidth.StringWidth(line); n > width {
				t.Fatalf("width %d: line of %d cells: %q\n%s", width, n, line, got)
			}
		}
		col := func(name string) int {
			for _, line := range strings.Split(got, "\n") {
				if strings.HasPrefix(line, "  "+name) {
					return runewidth.StringWidth(line[:strings.Index(line, "●")])
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

// A command in backticks is never broken across lines, so it pastes whole.
func TestWrap_KeepsCommandsWhole(t *testing.T) {
	text := "! drifted: slack (its entry is no longer the proxy URL); to fix it, " + mcpproxy.DriftFix
	for _, width := range []int{40, 60, 80} {
		for _, line := range wrap(text, width, 2) {
			if strings.Count(line, "`")%2 != 0 {
				t.Fatalf("width %d: command broken across lines at %q\n%q", width, line, wrap(text, width, 2))
			}
		}
	}
	if got := wrap("a `b c` d", 4, 2); len(got) != 3 || got[1] != "  `b c`" {
		t.Fatalf("a command too long for a line goes whole on its own: %q", got)
	}
}

// The separator is measured like the text: where the locale makes " · "
// wider than three cells, header lines still fit.
func TestJoinWrapped_EastAsianSeparatorFits(t *testing.T) {
	old := runewidth.DefaultCondition.EastAsianWidth
	runewidth.DefaultCondition.EastAsianWidth = true
	defer func() { runewidth.DefaultCondition.EastAsianWidth = old }()
	parts := []string{"MCP proxy ● running", "pid 99", "aimux v0.15.0", "up 3h", "127.0.0.1:7381"}
	for width := 30; width <= 80; width++ {
		for _, line := range joinWrapped(parts, false, width) {
			if n := runewidth.StringWidth(line); n > width {
				t.Fatalf("line of %d cells over %d: %q", n, width, line)
			}
		}
	}
}

// Names cut to fit that would read the same keep a short ending each, so
// every row still says which server it is.
func TestStatusPage_CutNamesStayDistinct(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow}
	for _, n := range []string{"playwright-browser-alpha", "playwright-browser-bravo", "slack"} {
		rep.Servers = append(rep.Servers, mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: n, State: mcpproxy.StateRunning}})
	}
	rep.Servers[0].State = mcpproxy.StateFailed
	got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: 44, all: true})
	var names []string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "  playw") {
			names = append(names, strings.Fields(line)[0])
		}
	}
	if len(names) != 2 || names[0] == names[1] || !strings.HasSuffix(names[0], "a") || !strings.HasSuffix(names[1], "o") ||
		!strings.Contains(names[0], "…") {
		t.Fatalf("cut names %q:\n%s", names, got)
	}
	if got := cutDistinct([]string{"slack", "abcdefgh-one", "abcdefgh-two", "abcdefgh-two"}, 8); got[0] != "slack" ||
		got[1] == got[2] || got[2] != got[3] {
		t.Fatalf("cutDistinct %q", got)
	}
}

// A never-started server the proxy has no route for (no cached tools) is
// not ready: it is counted and shown apart, with the fix.
func TestStatusPage_NoRouteIsNotReady(t *testing.T) {
	p := page()
	rep := pageReport()
	for i := range rep.Servers {
		if rep.Servers[i].Server == "pypi" {
			rep.Servers[i].Endpoint = false
		}
	}
	p.rep = &rep
	got := render(p)
	for _, want := range []string{
		"9 servers: 1 failed · 1 warning · 1 no route · 2 running · 1 idle · 3 ready\n  1 removed\n",
		"  ! 1 no route: pypi\n    `aimux mcp proxy refresh`, then `aimux mcp proxy restart`",
		"start `aimux mcp proxy serve` again with your original flags",
		"  ○ 3 ready, start on first call: brief, chrome-devtools, deepwiki\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("page lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "no route: pypi") > strings.Index(got, "  slack ") {
		t.Fatalf("no-route servers not with the problems, before the active ones:\n%s", got)
	}
	p.all = true
	if got := render(p); !strings.Contains(got, "  pypi                       ○ no route        0 tools   never called\n") {
		t.Fatalf("--all row for a server without a route:\n%s", got)
	}
}

// On a narrow terminal the ready heading shortens so no line passes the
// width; on a wide one it keeps the explanation.
func TestStatusPage_ReadyHeadingFitsNarrowTerminal(t *testing.T) {
	rep := mcpproxy.StatusReport{Running: true, PID: 1, StartedAt: statusNow}
	for _, n := range []string{"ab", "cd", "ef"} {
		rep.Servers = append(rep.Servers, mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: n, State: mcpproxy.StateNotStarted}, Endpoint: true})
	}
	for _, width := range []int{30, 12, 11} {
		got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: width})
		for _, line := range strings.Split(got, "\n") {
			if width < 30 && !strings.Contains(line, "○") {
				continue // the header has its own minimum; this is the ready heading's test
			}
			if n := runewidth.StringWidth(line); n > width {
				t.Fatalf("width %d: line of %d cells: %q\n%s", width, n, line, got)
			}
		}
		if width == 30 && !strings.Contains(got, "  ○ 3 ready: ab, cd, ef\n") {
			t.Fatalf("width 30: short heading missing:\n%s", got)
		}
	}
	// The full heading plus its first name fits exactly at its own width.
	full := "  ○ 3 ready, start on first call: ab,"
	got := render(statusPage{addr: "a", rep: &rep, now: statusNow, width: runewidth.StringWidth(full)})
	if !strings.Contains(got, full+"\n") {
		t.Fatalf("full heading not kept when it fits:\n%s", got)
	}
}
