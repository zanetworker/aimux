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
// measured in terminal cells. When it is wider than the terminal, the
// shrink columns are cut with an ellipsis, in order, each to minFlex at
// least (names: column 0 holds server names, cut so they stay distinct);
// past that, each line is cut at the width.
type table struct {
	head   []string
	rows   [][]cell
	shrink []int
	names  bool
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
	over := total - width
	for _, c := range t.shrink {
		if width <= 0 || over <= 0 {
			break
		}
		by := min(over, w[c]-max(minFlex, runewidth.StringWidth(t.head[c])))
		if by <= 0 {
			continue
		}
		w[c], over = w[c]-by, over-by
		col := make([]string, len(rows))
		for i, r := range rows {
			col[i] = r[c].text
		}
		if c == 0 && t.names {
			col = cutDistinct(col, w[c])
		}
		for i := range rows {
			rows[i][c].text = cut(col[i], w[c])
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

// message is one row of the LEVEL/MESSAGE/FIX table; fix is the steps to
// run, in order (none for a note that needs nothing).
type message struct {
	text string
	fix  []step
}

// step is one command of a fix; note qualifies it ("again with your
// original flags") on its "then" row.
type step struct{ cmd, note string }

// fixes is a fix of plain steps.
func fixes(cmds ...string) []step {
	out := make([]step, len(cmds))
	for i, c := range cmds {
		out[i] = step{cmd: c}
	}
	return out
}

// thenLabel is a later step's MESSAGE: "then", with its note.
func thenLabel(s step) string {
	if s.note == "" {
		return "then"
	}
	return "then, " + s.note
}

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
	comp := table{head: []string{"COMPONENT", "STATE", "VERSION", "UPTIME", "ADDRESS"}, shrink: []int{4}, rows: [][]cell{proxyRow(p)}}
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
	if nr := noRouteMessage(servers, p.handRun); nr != nil {
		warn = append(warn, *nr)
	}
	if len(warn)+len(info) > 0 {
		blocks = append(blocks, messageLines(warn, info, p.color, p.width))
	}

	for i, b := range blocks {
		if i > 0 {
			_, _ = fmt.Fprintln(out)
		}
		_, _ = fmt.Fprintln(out, strings.Join(b, "\n"))
	}
}

// messageLines is the LEVEL/MESSAGE/FIX table. A fix's later steps are rows
// of their own ("then"), and a fix command is never cut: MESSAGE is cut
// instead, and when a fix leaves no room for MESSAGE (minFlex cells) the
// FIX column goes: each command is then a line of its own under MESSAGE,
// whole, even past the width.
func messageLines(warn, info []message, color bool, width int) []string {
	type row struct {
		level, style string
		m            message
	}
	var rows []row
	for _, m := range warn {
		rows = append(rows, row{"WARNING", "", m})
	}
	for _, m := range info {
		rows = append(rows, row{"INFO", styleDim, m})
	}
	levelW, fixW := runewidth.StringWidth("LEVEL"), runewidth.StringWidth("FIX")
	for _, r := range rows {
		levelW = max(levelW, runewidth.StringWidth(r.level))
		for _, f := range r.m.fix {
			fixW = max(fixW, runewidth.StringWidth(f.cmd))
		}
	}
	levelStyle := func(r row) string { return map[bool]string{true: styleYellow, false: styleDim}[r.level == "WARNING"] }
	if width <= 0 || levelW+2+minFlex+2+fixW <= width {
		t := table{head: []string{"LEVEL", "MESSAGE", "FIX"}, shrink: []int{1}}
		for _, r := range rows {
			first := ""
			if len(r.m.fix) > 0 {
				first = r.m.fix[0].cmd
			}
			t.rows = append(t.rows, []cell{{r.level, levelStyle(r)}, {r.m.text, r.style}, {first, r.style}})
			for _, f := range r.m.fix[min(1, len(r.m.fix)):] {
				t.rows = append(t.rows, []cell{{"", ""}, {thenLabel(f), styleDim}, {f.cmd, r.style}})
			}
		}
		return t.lines(color, width)
	}
	t := table{head: []string{"LEVEL", "MESSAGE"}, shrink: []int{1}}
	var steps [][]string // the fix lines after each row
	for _, r := range rows {
		t.rows = append(t.rows, []cell{{r.level, levelStyle(r)}, {r.m.text, r.style}})
		var s []string
		indent := strings.Repeat(" ", levelW+2)
		for i, f := range r.m.fix {
			if i > 0 { // the label on a line of its own, so the command line pastes as it is
				s = append(s, indent+paint(color, styleDim, thenLabel(f)+":"))
			}
			s = append(s, indent+paint(color, r.style, f.cmd))
		}
		steps = append(steps, s)
	}
	lines := t.lines(color, width)
	out := []string{lines[0]}
	for i, line := range lines[1:] {
		out = append(append(out, line), steps[i]...)
	}
	return out
}

// shellWord is s as one POSIX shell word: bare when it has only characters
// the shell takes literally, else single-quoted, so a name from the Claude
// config pastes as written.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:@+=%") == "" {
		return s
	}
	return shellQuote(s)
}

// csvField is s as one field of a comma-separated flag value (pflag's
// StringSlice reads it as CSV): quoted, with quotes doubled, when it holds
// a comma, a quote or a line break.
func csvField(s string) string {
	if !strings.ContainsAny(s, ",\"\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// defaultPort is the proxy's port unless install was given another.
const defaultPort = 7381

// installCmd is `aimux mcp proxy install` with args, on the port install
// recorded when it is not the default (install refuses another port).
func installCmd(p statusPage, args ...string) string {
	cmd := "aimux mcp proxy install"
	if p.installPort != 0 && p.installPort != defaultPort {
		cmd += fmt.Sprintf(" --port %d", p.installPort)
	}
	for _, a := range args {
		cmd += " " + a
	}
	return cmd
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
			[]message{{"tool gateway not running: Workspace turns fail", fixes("aimux mcp proxy gateway install")}}
	case err != nil:
		return []cell{{"gateway", ""}, {"unknown", styleYellow}, {"-", ""}, {"-", ""}, {"", ""}},
			[]message{{"tool gateway: " + err.Error(), nil}}
	}
	uptime := "-"
	if !st.StartedAt.IsZero() {
		uptime = span(p.now, st.StartedAt)
	}
	// The worker reports no build version; its Version is the tool-policy
	// snapshot it serves, a detail.
	detail := plural(st.Leases, "lease", "leases") + ", " + plural(len(st.Backends), "backend", "backends")
	if st.Version != "" {
		detail += ", snapshot " + shortVersion(printable(st.Version))
	}
	row := []cell{{"gateway", ""}, {"running", styleGreen}, {"-", ""}, {uptime, ""}, {detail, ""}}
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
	return row, []message{{plural(len(st.Down), "gateway backend", "gateway backends") + " down: " + strings.Join(down, ", "), nil}}
}

// shortVersion shortens a digest ("sha256:" and 64 hex digits) to its
// first 12 digits, as container tools show one; anything else stays.
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
	t := table{head: []string{"SERVER", "STATE", "TOOLS", "LAST CALL"}, shrink: []int{0}, names: true}
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
	t := table{head: []string{"SERVER", "AGE", "MESSAGE"}, shrink: []int{2, 0}, names: true}
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
// no route for (no cached tools): refresh, then restart the installed
// proxy, or start a hand-run one again; nil when there are none.
func noRouteMessage(servers []shownServer, handRun bool) *message {
	var names []string
	for _, s := range servers {
		if s.kind == kindNoRoute {
			names = append(names, printable(s.Server))
		}
	}
	if len(names) == 0 {
		return nil
	}
	m := &message{fmt.Sprintf("%d no route: %s (no cached tools)", len(names), strings.Join(names, ", ")),
		fixes("aimux mcp proxy refresh", "aimux mcp proxy restart")}
	if handRun {
		m.fix[1] = step{cmd: "aimux mcp proxy serve", note: "again with your original flags"}
	}
	return m
}

// installMessages are what the install state asks of the owner (warn) and
// its facts at rest (info).
func installMessages(p statusPage) (warn, info []message) {
	if p.installErr != nil {
		return []message{{"install state unknown: " + p.installErr.Error(), nil}}, nil
	}
	st := p.install
	if st == nil {
		return nil, nil
	}
	if st.State == mcpproxy.InstallNotInstalled {
		return []message{{"not installed: Claude Code's servers are direct", fixes(installCmd(p))}}, nil
	}
	if len(st.Drifted) > 0 {
		warn = append(warn, message{fmt.Sprintf("drifted: %s (entry is no longer the proxy URL)", strings.Join(st.Drifted, ", ")),
			fixes("aimux mcp proxy uninstall", installCmd(p))})
	}
	if len(st.NotProxied) > 0 {
		warn = append(warn, message{"not behind the proxy: " + strings.Join(st.NotProxied, ", "), fixes(installCmd(p))})
	}
	if st.UpgradePending && p.cliVersion != "" {
		warn = append(warn, message{fmt.Sprintf("upgrade restart pending (proxy %s, cli %s)", cmp.Or(printable(st.DaemonVersion), "older"), p.cliVersion),
			fixes("aimux mcp proxy restart")})
	}
	if !st.Hooks {
		warn = append(warn, message{"hooks off: servers you add later stay direct", fixes(installCmd(p))})
	}
	info = append(info, message{plural(len(st.Moved), "server", "servers") + " behind the proxy", nil})
	if st.Hooks {
		info = append(info, message{"hooks on", nil})
	}
	if len(st.Removed) > 0 {
		info = append(info, message{fmt.Sprintf("removed from %s: %s (still served, unused)", p.claudeConfig, strings.Join(st.Removed, ", ")),
			fixes(installCmd(p))})
	}
	if len(st.Skipped) > 0 {
		// One --unskip per name: the flag splits a value at commas.
		var args []string
		for _, n := range st.Skipped {
			args = append(args, "--unskip", shellWord(csvField(n)))
		}
		info = append(info, message{"kept direct (--skip): " + strings.Join(st.Skipped, ", "), fixes(installCmd(p, args...))})
	}
	for _, line := range mcpproxy.KeptDirectLines(st.KeptDirect) {
		info = append(info, message{line, nil})
	}
	return warn, info
}
