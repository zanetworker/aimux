package cmd

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// ANSI styles for `aimux mcp proxy status`: one colour for a server's state
// (green running, red failed), dim for what is at rest, yellow for what
// needs the owner. Only on a terminal, and never with NO_COLOR or TERM=dumb.
const (
	styleDim    = "2"
	styleBold   = "1"
	styleGreen  = "32"
	styleRed    = "31"
	styleYellow = "33"
)

// statusColor reports whether w is a terminal that takes colour.
func statusColor(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" // #nosec G115 -- small fd
}

func paint(on bool, style, s string) string {
	if !on || style == "" || s == "" {
		return s
	}
	return "\x1b[" + style + "m" + s + "\x1b[0m"
}

// pad pads s with spaces to width runes; colour is applied after padding, so
// escape codes never count as width.
func pad(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

func stateLabel(st mcpproxy.ServerState) (string, string) {
	switch st {
	case mcpproxy.StateRunning:
		return "● running", styleGreen
	case mcpproxy.StateFailed:
		return "● failed", styleRed
	case mcpproxy.StateStarting:
		return "◐ starting", styleBold
	case mcpproxy.StateIdleStopped:
		return "○ idle", styleDim
	case mcpproxy.StateNotStarted:
		return "○ not started", styleDim
	}
	return "○ " + string(st), styleDim
}

// stateRank orders the table by what needs attention: failed, then active,
// then at rest; removed servers go last.
var stateRank = map[mcpproxy.ServerState]int{mcpproxy.StateFailed: 0, mcpproxy.StateStarting: 1, mcpproxy.StateRunning: 2,
	mcpproxy.StateIdleStopped: 3, mcpproxy.StateNotStarted: 4}

// printProxyStatus prints the daemon line, a summary of server states, the
// server table (failed and active first), then the stderr tail of each
// failed server. removed are servers whose entry left ~/.claude.json: shown
// as removed, last, and counted apart.
func printProxyStatus(out io.Writer, addr string, rep mcpproxy.StatusReport, now time.Time, color bool, removed ...string) {
	isRemoved := func(name string) bool { return slices.Contains(removed, name) }
	servers := slices.Clone(rep.Servers)
	sort.SliceStable(servers, func(a, b int) bool {
		ra, rb := stateRank[servers[a].State], stateRank[servers[b].State]
		if isRemoved(servers[a].Server) {
			ra = 9
		}
		if isRemoved(servers[b].Server) {
			rb = 9
		}
		if ra != rb {
			return ra < rb
		}
		return servers[a].Server < servers[b].Server
	})
	_, _ = fmt.Fprintf(out, "%s on %s · pid %d · started %s\n", paint(color, styleBold, "MCP proxy"), addr, rep.PID, mcpproxy.Ago(now, rep.StartedAt))

	count := map[mcpproxy.ServerState]int{}
	nRemoved := 0
	for _, s := range servers {
		if isRemoved(s.Server) {
			nRemoved++
			continue
		}
		count[s.State]++
	}
	parts := []string{fmt.Sprintf("%d servers", len(rep.Servers))}
	part := func(n int, label, style string, always bool) {
		if n == 0 && !always {
			return
		}
		if n == 0 {
			style = styleDim
		}
		parts = append(parts, paint(color, style, fmt.Sprintf("%d %s", n, label)))
	}
	part(count[mcpproxy.StateRunning], "running", styleGreen, true)
	part(count[mcpproxy.StateStarting], "starting", styleBold, false)
	part(count[mcpproxy.StateIdleStopped], "idle", styleDim, false)
	part(count[mcpproxy.StateNotStarted], "not started", styleDim, false)
	part(count[mcpproxy.StateFailed], "failed", styleRed, true)
	part(nRemoved, "removed", styleDim, false)
	_, _ = fmt.Fprintf(out, "%s\n\n", strings.Join(parts, paint(color, styleDim, " · ")))

	type row struct{ name, state, stateStyle, tools, last, err string }
	rows := make([]row, 0, len(servers))
	w := [4]int{len("SERVER"), len("STATE"), len("TOOLS"), len("LAST CALL")}
	for _, s := range servers {
		label, style := stateLabel(s.State)
		if isRemoved(s.Server) {
			label, style = "○ removed", styleDim
		}
		lastErr := s.LastError
		if s.ProbeError != "" {
			lastErr = s.ProbeError
		}
		r := row{printable(s.Server), label, style, fmt.Sprint(s.Tools), mcpproxy.Ago(now, s.LastCall), printable(lastErr)}
		for i, v := range []string{r.name, r.state, r.tools, r.last} {
			if n := utf8.RuneCountInString(v); n > w[i] {
				w[i] = n
			}
		}
		rows = append(rows, r)
	}
	_, _ = fmt.Fprintf(out, "  %s\n", paint(color, styleDim, strings.TrimRight(
		pad("SERVER", w[0])+"  "+pad("STATE", w[1])+"  "+pad("TOOLS", w[2])+"  "+pad("LAST CALL", w[3])+"  LAST ERROR", " ")))
	for _, r := range rows {
		last := paint(color, map[bool]string{true: styleDim}[r.last == "never"], pad(r.last, w[3]))
		errText := r.err
		if errText == "" {
			errText = paint(color, styleDim, "-")
		} else {
			errText = paint(color, styleRed, errText)
		}
		_, _ = fmt.Fprintf(out, "  %s  %s  %s  %s  %s\n", pad(r.name, w[0]), paint(color, r.stateStyle, pad(r.state, w[1])),
			pad(r.tools, w[2]), last, errText)
	}
	for _, s := range servers {
		if s.State != mcpproxy.StateFailed || len(s.StderrTail) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(out, "\n%s\n", paint(color, styleRed, printable(s.Server)+" stderr:"))
		for _, line := range s.StderrTail {
			_, _ = fmt.Fprintf(out, "  %s\n", paint(color, styleDim, printable(line)))
		}
	}
}

// printInstallState prints what install moved, compactly (the table lists
// the servers): removed entries and drift apart, things to act on in yellow.
func printInstallState(out io.Writer, st mcpproxy.InstallState, claudeConfig, upgradeText string, color bool) {
	if st.State == mcpproxy.InstallNotInstalled {
		_, _ = fmt.Fprintf(out, "Install: not installed (`aimux mcp proxy install` moves Claude Code's stdio servers behind the proxy)\n")
		return
	}
	_, _ = fmt.Fprintf(out, "%d servers behind the proxy %s\n", len(st.Moved),
		paint(color, styleDim, "· change one with `aimux mcp proxy edit <server>`"))
	warn := func(format string, a ...any) {
		_, _ = fmt.Fprintf(out, "  %s %s\n", paint(color, styleYellow, "!"), fmt.Sprintf(format, a...))
	}
	note := func(format string, a ...any) {
		_, _ = fmt.Fprintf(out, "  %s\n", paint(color, styleDim, "· "+fmt.Sprintf(format, a...)))
	}
	if len(st.Drifted) > 0 {
		warn("drifted: %s (its entry is no longer the proxy URL: a running session saved an old copy, or it was edited); to fix it, %s",
			strings.Join(st.Drifted, ", "), mcpproxy.DriftFix)
	}
	if len(st.NotProxied) > 0 {
		warn("not behind the proxy: %s (`aimux mcp proxy install` moves them)", strings.Join(st.NotProxied, ", "))
	}
	if st.UpgradePending {
		warn("%s", upgradeText)
	}
	if st.Hooks {
		note("hooks on: a server you add in a session moves behind the proxy; servers still direct are named when a session starts")
	} else {
		warn("hooks off: servers you add later stay direct; `aimux mcp proxy install` adds the hooks")
	}
	if len(st.Removed) > 0 {
		note("removed from %s: %s (still served by the proxy, unused; `aimux mcp proxy install` takes them off)", claudeConfig, strings.Join(st.Removed, ", "))
	}
	if len(st.Skipped) > 0 {
		note("kept direct: %s (`aimux mcp proxy install --unskip <name>` moves one)", strings.Join(st.Skipped, ", "))
	}
}
