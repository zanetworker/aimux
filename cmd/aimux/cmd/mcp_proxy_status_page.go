package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// cell is one table cell: its text and ANSI style ("" for none).
type cell struct{ text, style string }

// table is a header row over aligned rows, columns two spaces apart and
// measured in terminal cells. When it is wider than the terminal, column
// flex is cut with an ellipsis (to minFlex at least; names cuts server
// names so they stay distinct); past that, each line is cut at the width.
type table struct {
	head  []string
	rows  [][]cell
	flex  int
	names bool
}

// minFlex is the narrowest a cut column gets.
const minFlex = 8

// lines renders t; width 0 cuts nothing.
func (t table) lines(color bool, width int) []string {
	w := make([]int, len(t.head))
	for i, h := range t.head {
		w[i] = runewidth.StringWidth(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			w[i] = max(w[i], runewidth.StringWidth(c.text))
		}
	}
	rows := make([][]cell, len(t.rows)) // cut copies, never the caller's cells
	for i, r := range t.rows {
		rows[i] = slices.Clone(r)
	}
	total := 2 * (len(w) - 1)
	for _, n := range w {
		total += n
	}
	if over := total - width; width > 0 && over > 0 {
		w[t.flex] = max(w[t.flex]-over, minFlex, runewidth.StringWidth(t.head[t.flex]))
		col := make([]string, len(rows))
		for i, r := range rows {
			col[i] = r[t.flex].text
		}
		if t.names {
			col = cutDistinct(col, w[t.flex])
		}
		for i := range rows {
			rows[i][t.flex].text = cut(col[i], w[t.flex])
		}
	}
	head := make([]cell, len(t.head))
	for i, h := range t.head {
		head[i] = cell{h, styleBold}
	}
	out := []string{tableLine(head, w, color, width)}
	for _, r := range rows {
		out = append(out, tableLine(r, w, color, width))
	}
	return out
}

// tableLine is one row: each cell but the last non-empty one padded to its
// column, so no line ends in spaces; cut at width when it would pass it.
func tableLine(cells []cell, w []int, color bool, width int) string {
	last := len(cells) - 1
	for last > 0 && cells[last].text == "" {
		last--
	}
	var b strings.Builder
	used := 0
	for i := 0; i <= last; i++ {
		text, gap := cells[i].text, ""
		if i < last {
			gap = strings.Repeat(" ", max(w[i]-runewidth.StringWidth(text), 0))
		}
		if i > 0 {
			b.WriteString("  ")
			used += 2
		}
		if n := runewidth.StringWidth(text); width > 0 && used+n > width {
			b.WriteString(paint(color, cells[i].style, cut(text, max(width-used, 1))))
			break
		}
		b.WriteString(paint(color, cells[i].style, text))
		used += runewidth.StringWidth(text)
		if width > 0 && used+len(gap) > width {
			break // the next column would start past the width
		}
		b.WriteString(gap)
		used += len(gap)
	}
	return b.String()
}

// message is one row of the LEVEL/MESSAGE/FIX table; fix is one command
// to copy, or empty.
type message struct{ text, fix string }

// shownServer is a server with where it goes on the page.
type shownServer struct {
	mcpproxy.ServerReport
	kind serverKind
}

// printStatusPage prints the page as tables: the proxy and the tool
// gateway, the servers (problems first, ready ones counted in the summary
// line unless all), their errors, then what needs the owner (WARNING) and
// notes (INFO), each with a command to run when there is one. Colour only
// when p.color; the text is the same either way. The gateway is asked
// first (its status call is bounded), so its row lines up with the proxy's.
func printStatusPage(out io.Writer, p statusPage) {
	var warn []message
	comp := table{head: []string{"COMPONENT", "STATE", "VERSION", "UPTIME", "ADDRESS"}, flex: 4, rows: [][]cell{proxyRow(p)}}
	if p.gateway != nil {
		row, w := gatewayRow(p)
		comp.rows = append(comp.rows, row)
		warn = append(warn, w...)
	}
	blocks := [][]string{comp.lines(p.color, p.width)}

	var servers []shownServer
	if p.rep != nil {
		servers = sortedServers(p)
		shown := shownServers(servers, p.all)
		blocks = append(blocks, append(serverTable(shown, p).lines(p.color, p.width), serverSummary(servers, p)))
		if errs := errorTable(shown, p); len(errs.rows) > 0 {
			blocks = append(blocks, errs.lines(p.color, p.width))
		}
	}

	install, info := installMessages(p)
	warn = append(install, warn...)
	if nr := noRouteMessage(servers); nr != nil {
		warn = append(warn, *nr)
	}
	if len(warn)+len(info) > 0 {
		t := table{head: []string{"LEVEL", "MESSAGE", "FIX"}, flex: 1}
		for _, m := range warn {
			t.rows = append(t.rows, []cell{{"WARNING", styleYellow}, {m.text, ""}, {m.fix, ""}})
		}
		for _, m := range info {
			t.rows = append(t.rows, []cell{{"INFO", styleDim}, {m.text, styleDim}, {m.fix, styleDim}})
		}
		blocks = append(blocks, t.lines(p.color, p.width))
	}

	for i, b := range blocks {
		if i > 0 {
			_, _ = fmt.Fprintln(out)
		}
		_, _ = fmt.Fprintln(out, strings.Join(b, "\n"))
	}
}

// proxyRow is the proxy's COMPONENT row.
func proxyRow(p statusPage) []cell {
	installed := p.install != nil && p.install.State != mcpproxy.InstallNotInstalled
	switch {
	case p.noAnswer:
		return []cell{{"proxy", ""}, {"no status", styleYellow}, {"-", ""}, {"-", ""}, {p.addr, ""}}
	case p.rep == nil:
		return []cell{{"proxy", ""}, {"not running", map[bool]string{true: styleRed, false: styleDim}[installed]}, {"-", ""}, {"-", ""}, {p.addr, ""}}
	}
	return []cell{{"proxy", ""}, {"running", styleGreen}, {cmp.Or(printable(p.rep.Version), "-"), ""}, {span(p.now, p.rep.StartedAt), ""}, {p.addr, ""}}
}

// gatewayRow is the tool gateway's COMPONENT row and what needs the owner
// about it: not running, unknown, or backends down.
func gatewayRow(p statusPage) ([]cell, []message) {
	st, err := p.gateway()
	switch {
	case errors.Is(err, toolgateway.ErrGatewayNotRunning):
		return []cell{{"gateway", ""}, {"not running", styleRed}, {"-", ""}, {"-", ""}, {"Workspace turns fail", ""}},
			[]message{{"tool gateway not running: Workspace turns fail", "aimux mcp proxy gateway install"}}
	case err != nil:
		return []cell{{"gateway", ""}, {"unknown", styleYellow}, {"-", ""}, {"-", ""}, {"", ""}},
			[]message{{"tool gateway: " + err.Error(), ""}}
	}
	uptime := "-"
	if !st.StartedAt.IsZero() {
		uptime = span(p.now, st.StartedAt)
	}
	row := []cell{{"gateway", ""}, {"running", styleGreen}, {cmp.Or(shortVersion(printable(st.Version)), "-"), ""}, {uptime, ""},
		{plural(st.Leases, "lease", "leases") + ", " + plural(len(st.Backends), "backend", "backends"), ""}}
	if len(st.Down) == 0 {
		return row, nil
	}
	down := make([]string, len(st.Down))
	for i, n := range st.Down {
		down[i] = n
		if at, ok := st.Checked[n]; ok { // an older worker reports no times
			down[i] += " (checked " + mcpproxy.Ago(p.now, at) + ")"
		}
	}
	return row, []message{{plural(len(st.Down), "gateway backend", "gateway backends") + " down: " + strings.Join(down, ", "), ""}}
}

// shortVersion shortens an image digest ("sha256:" and 64 hex digits) to
// its first 12 digits, as container tools show it; other versions stay.
func shortVersion(v string) string {
	const prefix, keep = "sha256:", 12
	if strings.HasPrefix(v, prefix) && len(v) > len(prefix)+keep {
		return v[:len(prefix)+keep]
	}
	return v
}

// sortedServers classifies the report's servers, problems first, then by
// state and name.
func sortedServers(p statusPage) []shownServer {
	var removed []string
	if p.install != nil {
		removed = p.install.Removed
	}
	var out []shownServer
	for _, s := range p.rep.Servers {
		out = append(out, shownServer{s, classify(s, slices.Contains(removed, s.Server))})
	}
	sort.SliceStable(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.kind != y.kind {
			return x.kind < y.kind
		}
		if rx, ry := stateRank[x.State], stateRank[y.State]; rx != ry {
			return rx < ry
		}
		return x.Server < y.Server
	})
	return out
}

// shownServers are the servers with a row: all of them with all, else
// all but the ready and removed ones.
func shownServers(servers []shownServer, all bool) []shownServer {
	var out []shownServer
	for _, s := range servers {
		if all || (s.kind != kindNotStarted && s.kind != kindRemoved) {
			out = append(out, s)
		}
	}
	return out
}

// stateCell is a server's STATE: its state word in its colour, yellow for
// a live server with a recent error.
func stateCell(s shownServer) cell {
	switch s.kind {
	case kindRemoved:
		return cell{"removed", styleDim}
	case kindNoRoute:
		return cell{"no route", styleYellow}
	}
	c := cell{string(s.State), styleDim}
	switch s.State {
	case mcpproxy.StateRunning:
		c = cell{"running", styleGreen}
	case mcpproxy.StateFailed:
		c = cell{"failed", styleRed}
	case mcpproxy.StateStarting:
		c = cell{"starting", styleBold}
	case mcpproxy.StateIdleStopped:
		c = cell{"idle", styleDim}
	case mcpproxy.StateNotStarted: // lazy: the proxy starts it on its first tools/call
		c = cell{"ready", styleDim}
	}
	if s.kind == kindWarning {
		c.style = styleYellow
	}
	return c
}

func serverTable(shown []shownServer, p statusPage) table {
	t := table{head: []string{"SERVER", "STATE", "TOOLS", "LAST CALL"}, flex: 0, names: true}
	for _, s := range shown {
		last := cell{"never", styleDim}
		if !s.LastCall.IsZero() {
			last = cell{mcpproxy.Ago(p.now, s.LastCall), ""}
		}
		t.rows = append(t.rows, []cell{{printable(s.Server), ""}, stateCell(s), {fmt.Sprint(s.Tools), ""}, last})
	}
	return t
}

// serverSummary is the dim count line under the server rows; it says how
// to see the rows left out.
func serverSummary(servers []shownServer, p statusPage) string {
	var ready, failed, probeFailed, warnings, noRoute, removed, hidden int
	for _, s := range servers {
		isRemoved := p.install != nil && slices.Contains(p.install.Removed, s.Server)
		switch {
		case isRemoved:
			removed++
		case s.kind == kindNotStarted:
			ready++
		case s.kind == kindWarning:
			warnings++
		case s.kind == kindNoRoute:
			noRoute++
		}
		if !isRemoved && s.State == mcpproxy.StateFailed {
			failed++
		}
		if s.ProbeError != "" && (isRemoved || s.State != mcpproxy.StateFailed) {
			probeFailed++
		}
		if !p.all && (s.kind == kindNotStarted || s.kind == kindRemoved) {
			hidden++
		}
	}
	parts := []string{fmt.Sprintf("%d ready", ready), fmt.Sprintf("%d failed", failed)}
	for _, x := range []struct {
		n           int
		one, others string
	}{{probeFailed, "probe failed", "probe failed"}, {warnings, "warning", "warnings"}, {noRoute, "no route", "no route"}, {removed, "removed", "removed"}} {
		if x.n > 0 {
			parts = append(parts, plural(x.n, x.one, x.others))
		}
	}
	line := "(" + strings.Join(parts, ", ")
	if hidden > 0 {
		line += "; --all lists them"
	}
	line += ")"
	if p.width > 0 {
		line = cut(line, p.width)
	}
	return paint(p.color, styleDim, line)
}

// errorTable holds the errors of the shown servers: a failure (with the
// last lines of a failed start's stderr) red, a recent error on a live
// server yellow, and with all an error that is past, dim and marked old.
func errorTable(shown []shownServer, p statusPage) table {
	t := table{head: []string{"SERVER", "AGE", "MESSAGE"}, flex: 2}
	for _, s := range shown {
		age := "-"
		if !s.LastErrorAt.IsZero() {
			age = span(p.now, s.LastErrorAt)
		}
		name := printable(s.Server)
		ek := s.kind
		if ek == kindRemoved {
			ek = errorKind(s.ServerReport) // its entry left the config; its backend's errors are still its own
		}
		switch {
		case ek == kindFailed:
			if s.ProbeError != "" {
				age = "-" // a probe's failure is now, not at the last error
			}
			if e := cmp.Or(s.ProbeError, s.LastError); e != "" {
				t.rows = append(t.rows, []cell{{name, ""}, {age, ""}, {printable(e), styleRed}})
			}
			if s.State == mcpproxy.StateFailed {
				for _, line := range s.StderrTail {
					t.rows = append(t.rows, []cell{{"", ""}, {"", ""}, {printable(line), styleDim}})
				}
			}
		case ek == kindWarning:
			t.rows = append(t.rows, []cell{{name, ""}, {age, ""}, {printable(s.LastError), styleYellow}})
		case s.LastError != "" && p.all:
			t.rows = append(t.rows, []cell{{name, styleDim}, {age, styleDim}, {"(old) " + printable(s.LastError), styleDim}})
		}
	}
	return t
}

// noRouteMessage is the warning for never-started servers the proxy has
// no route for (no cached tools); nil when there are none.
func noRouteMessage(servers []shownServer) *message {
	var names []string
	for _, s := range servers {
		if s.kind == kindNoRoute {
			names = append(names, printable(s.Server))
		}
	}
	if len(names) == 0 {
		return nil
	}
	return &message{fmt.Sprintf("%d no route: %s (no cached tools; restart the proxy after the refresh)", len(names), strings.Join(names, ", ")),
		"aimux mcp proxy refresh"}
}

// installMessages are what the install state asks of the owner (warn) and
// its facts at rest (info).
func installMessages(p statusPage) (warn, info []message) {
	if p.installErr != nil {
		return []message{{"install state unknown: " + p.installErr.Error(), ""}}, nil
	}
	st := p.install
	if st == nil {
		return nil, nil
	}
	if st.State == mcpproxy.InstallNotInstalled {
		return []message{{"not installed: Claude Code's servers are direct", "aimux mcp proxy install"}}, nil
	}
	if len(st.Drifted) > 0 {
		warn = append(warn, message{fmt.Sprintf("drifted: %s (entry is no longer the proxy URL); then aimux mcp proxy install",
			strings.Join(st.Drifted, ", ")), "aimux mcp proxy uninstall"})
	}
	if len(st.NotProxied) > 0 {
		warn = append(warn, message{"not behind the proxy: " + strings.Join(st.NotProxied, ", "), "aimux mcp proxy install"})
	}
	if st.UpgradePending && p.cliVersion != "" {
		warn = append(warn, message{fmt.Sprintf("upgrade restart pending (proxy %s, cli %s)", cmp.Or(printable(st.DaemonVersion), "older"), p.cliVersion),
			"aimux mcp proxy restart"})
	}
	if !st.Hooks {
		warn = append(warn, message{"hooks off: servers you add later stay direct", "aimux mcp proxy install"})
	}
	info = append(info, message{plural(len(st.Moved), "server", "servers") + " behind the proxy", ""})
	if st.Hooks {
		info = append(info, message{"hooks on", ""})
	}
	if len(st.Removed) > 0 {
		info = append(info, message{fmt.Sprintf("removed from %s: %s (still served, unused)", p.claudeConfig, strings.Join(st.Removed, ", ")),
			"aimux mcp proxy install"})
	}
	if len(st.Skipped) > 0 {
		info = append(info, message{"kept direct (--skip): " + strings.Join(st.Skipped, ", "),
			"aimux mcp proxy install --unskip " + strings.Join(st.Skipped, ",")})
	}
	for _, line := range mcpproxy.KeptDirectLines(st.KeptDirect) {
		info = append(info, message{line, ""})
	}
	return warn, info
}
