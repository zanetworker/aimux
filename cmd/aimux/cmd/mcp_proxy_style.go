package cmd

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
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

// pad pads s with spaces to width terminal cells; colour is applied after padding, so
// escape codes never count as width.
func pad(s string, width int) string {
	if n := runewidth.StringWidth(s); n < width {
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
	case mcpproxy.StateNotStarted: // lazy: the proxy starts it on its first tools/call
		return "○ ready", styleDim
	}
	return "○ " + string(st), styleDim
}

// stateAge is " 12m ago" for a state last seen at at, "" when at is unknown.
func stateAge(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return " " + mcpproxy.Ago(now, at)
}

// stateRank orders servers of one kind: active before at rest.
var stateRank = map[mcpproxy.ServerState]int{mcpproxy.StateFailed: 0, mcpproxy.StateStarting: 1, mcpproxy.StateRunning: 2,
	mcpproxy.StateIdleStopped: 3, mcpproxy.StateNotStarted: 4}

// termWidth is w's terminal width in columns, 0 when w is not a terminal
// (piped output is never cut or wrapped).
func termWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) { // #nosec G115 -- small fd
		return 0
	}
	cols, _, err := term.GetSize(int(f.Fd())) // #nosec G115 -- small fd
	if err != nil {
		return 0
	}
	return cols
}

// statusPage is everything `aimux mcp proxy status` prints.
type statusPage struct {
	addr         string
	rep          *mcpproxy.StatusReport // nil: the proxy is not running
	install      *mcpproxy.InstallState // nil: not read
	installErr   error                  // why the install state could not be read
	claudeConfig string
	upgradeText  string
	gateway      func() string // the tool gateway line, styled, asked once the proxy's part is out; nil or "" leaves it out
	noAnswer     bool          // the proxy took the connection but gave no status
	now          time.Time
	color        bool
	width        int  // terminal columns; 0 cuts and wraps nothing
	all          bool // every server as a row, instead of never-started ones on one line
}

// serverKind is where a server goes on the page, problems first.
type serverKind int

const (
	kindFailed     serverKind = iota // failed, or a probe could not start it
	kindWarning                      // live, but its last error is newer than its last call and start
	kindNoRoute                      // never started, and the proxy has no endpoint for it (no cached tools)
	kindActive                       // starting, running or idle
	kindNotStarted                   // never started
	kindRemoved                      // its entry left the Claude config
)

// errorIsPast reports whether the daemon says the server answered a call
// or started well after its last error, in the session the error came
// from, so the error no longer describes it. A daemon too old to say
// (no errorPast) is not guessed at: its error is old unless it came after
// the server's last answer.
func errorIsPast(s mcpproxy.ServerReport) bool {
	if s.State == mcpproxy.StateFailed {
		return false
	}
	if s.ErrorPast == nil {
		return s.LastOK.IsZero() || !s.LastErrorAt.After(s.LastOK)
	}
	return *s.ErrorPast
}

// classify places a server; errorKind is how its own error reads (failed,
// warning or neither), whether or not its entry was removed.
func classify(s mcpproxy.ServerReport, removed bool) serverKind {
	switch k := errorKind(s); {
	case s.ProbeError != "": // a probe the owner just asked for
		return kindFailed
	case removed:
		return kindRemoved
	default:
		return k
	}
}

func errorKind(s mcpproxy.ServerReport) serverKind {
	switch {
	case s.ProbeError != "" || s.State == mcpproxy.StateFailed:
		return kindFailed
	case s.LastError != "" && !errorIsPast(s):
		return kindWarning
	case s.State == mcpproxy.StateNotStarted && !s.Endpoint:
		return kindNoRoute
	case s.State == mcpproxy.StateNotStarted:
		return kindNotStarted
	}
	return kindActive
}

// cut shortens s to n terminal cells ending in an ellipsis; n <= 0 keeps
// it whole.
func cut(s string, n int) string {
	if n <= 0 {
		return s
	}
	return runewidth.Truncate(s, n, "…")
}

// wrap breaks text at spaces so no line passes width cells; lines after
// the first start with hang spaces. A command in backticks is one word, so
// it is never broken and pastes whole. width 0 keeps one line.
func wrap(text string, width, hang int) []string {
	words := commandWords(text)
	lead := text[:len(text)-len(strings.TrimLeft(text, " "))]
	if width <= 0 || len(words) == 0 {
		return []string{text}
	}
	var lines []string
	line := lead + words[0]
	for _, w := range words[1:] {
		if runewidth.StringWidth(line)+1+runewidth.StringWidth(w) > width {
			lines = append(lines, line)
			line = strings.Repeat(" ", hang) + w
			continue
		}
		line += " " + w
	}
	return append(lines, line)
}

// escapes matches the ANSI styles paint adds.
var escapes = regexp.MustCompile("\x1b\\[[0-9;]*m")

// cutDistinct cuts each name to n cells; names that would then read the
// same as another's keep their ending after the ellipsis, as short as
// tells them apart ("playwright-b…a", "playwright-b…o"). The same name
// twice stays the same.
func cutDistinct(names []string, n int) []string {
	out := make([]string, len(names))
	for i, s := range names {
		out[i] = cut(s, n)
	}
	clash := func(i int) bool {
		for j := range names {
			if j != i && names[j] != names[i] && out[j] == out[i] {
				return true
			}
		}
		return false
	}
	var clashing []int
	for i := range names {
		if out[i] != names[i] && clash(i) {
			clashing = append(clashing, i)
		}
	}
	// Endings grow together, one rune at a time, until no cut name reads
	// like another's or the head would be gone.
	for k := 1; len(clashing) > 0 && k < n-1; k++ {
		for _, i := range clashing {
			r := []rune(names[i])
			tail := string(r[max(len(r)-k, 0):])
			out[i] = runewidth.Truncate(names[i], max(n-runewidth.StringWidth(tail), 1), "…") + tail
		}
		var still []int
		for _, i := range clashing {
			if clash(i) {
				still = append(still, i)
			}
		}
		if len(still) == 0 {
			break
		}
	}
	return out
}

// commandWords splits text at spaces outside backticks.
func commandWords(text string) []string {
	var words []string
	var cur strings.Builder
	in := false
	for _, r := range text {
		switch {
		case r == '`':
			in = !in
			cur.WriteRune(r)
		case r == ' ' && !in:
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

// joinWrapped joins styled parts with a dim " · ", starting a new line
// (indented two spaces) before a part that would pass width; width 0 keeps
// one line. Escape codes never count as width.
func joinWrapped(parts []string, color bool, width int) []string {
	sep := paint(color, styleDim, " · ")
	visible := func(s string) int { return runewidth.StringWidth(escapes.ReplaceAllString(s, "")) }
	var lines []string
	line := ""
	for i, part := range parts {
		switch {
		case i == 0:
			line = part
		case width > 0 && visible(line)+visible(" · ")+visible(part) > width:
			lines = append(lines, line)
			line = "  " + part
		default:
			line += sep + part
		}
	}
	return append(lines, line)
}

// wrapItems lists items after prefix, comma separated, breaking only
// between items so no line passes width; later lines start with hang
// spaces, and an item too long for a line of its own is cut. width 0
// keeps one line.
func wrapItems(prefix string, items []string, width, hang int) []string {
	var lines []string
	line, onLine := prefix, 0
	for i, it := range items {
		if i < len(items)-1 {
			it += ","
		}
		sep := " "
		if onLine == 0 {
			sep = ""
		}
		if width > 0 && runewidth.StringWidth(line)+len(sep)+runewidth.StringWidth(it) > width && (onLine > 0 || i == 0) {
			lines = append(lines, strings.TrimRight(line, " "))
			line, onLine, sep = strings.Repeat(" ", hang), 0, ""
		}
		if width > 0 {
			it = cut(it, max(width-runewidth.StringWidth(line), 1))
		}
		line += sep + it
		onLine++
	}
	return append(lines, line)
}

// noRouteFix is what gives a never-started server without an endpoint its
// route: cache its tools, then have the proxy serve them.
const noRouteFix = "`aimux mcp proxy refresh`, then `aimux mcp proxy restart` (a proxy you run by hand: start `aimux mcp proxy serve` again)"

// heading is the first of a list's headings that fits width with room for
// one short name (width 0: the first), else the last cut to width.
func heading(width int, headings ...string) string {
	for _, h := range headings {
		if width <= 0 || runewidth.StringWidth(h)+3 <= width {
			return h
		}
	}
	h := headings[len(headings)-1]
	if runewidth.StringWidth(strings.TrimRight(h, " ")) > width {
		return cut(strings.TrimRight(h, " "), width)
	}
	return h
}

// span is how long ago t was, without "ago": "3h", "0s".
func span(now, t time.Time) string {
	if s := mcpproxy.Ago(now, t); s != "just now" {
		return strings.TrimSuffix(s, " ago")
	}
	return "0s"
}

// printStatusPage prints the proxy line and a count per state, what needs
// the owner (yellow), the servers with a problem (red failed, yellow recent
// error) and then the active ones, never-started servers on one line
// (every server with all), the gateway line, and dim notes. Colour only
// when p.color; the text is the same either way.
func printStatusPage(out io.Writer, p statusPage) {
	c := p.color
	var blocks [][]string

	installed := p.install != nil && p.install.State != mcpproxy.InstallNotInstalled
	var head []string
	switch {
	case p.noAnswer:
		head = append(head, paint(c, styleBold, "MCP proxy")+" "+paint(c, styleYellow, "? no status")+" from "+p.addr)
	case p.rep == nil:
		glyph, style := "○ not running", styleDim
		if installed {
			glyph, style = "● not running", styleRed
		}
		head = append(head, paint(c, styleBold, "MCP proxy")+" "+paint(c, style, glyph)+" on "+p.addr)
	default:
		parts := []string{paint(c, styleBold, "MCP proxy") + " " + paint(c, styleGreen, "● running"), fmt.Sprintf("pid %d", p.rep.PID)}
		if p.rep.Version != "" {
			parts = append(parts, "aimux "+printable(p.rep.Version))
		}
		parts = append(parts, "up "+span(p.now, p.rep.StartedAt), p.addr)
		head = append(head, joinWrapped(parts, c, p.width)...)
	}

	var removed []string
	if p.install != nil {
		removed = p.install.Removed
	}
	type server struct {
		mcpproxy.ServerReport
		kind serverKind
	}
	var servers []server
	if p.rep != nil {
		for _, s := range p.rep.Servers {
			servers = append(servers, server{s, classify(s, slices.Contains(removed, s.Server))})
		}
		sort.SliceStable(servers, func(a, b int) bool {
			x, y := servers[a], servers[b]
			if x.kind != y.kind {
				return x.kind < y.kind
			}
			if rx, ry := stateRank[x.State], stateRank[y.State]; rx != ry {
				return rx < ry
			}
			return x.Server < y.Server
		})
		// A count per state of the servers still configured; a probe that
		// failed on a server that did not fail, and a recent error on a live
		// one, are counted apart and not instead of its state.
		count := map[serverKind]int{}
		byState := map[mcpproxy.ServerState]int{}
		probeFailed := 0
		for _, s := range servers {
			isRemoved := slices.Contains(removed, s.Server)
			switch {
			case isRemoved:
				count[kindRemoved]++
			case s.kind == kindWarning:
				count[kindWarning]++
				byState[s.State]++
			case s.kind == kindNoRoute:
				count[kindNoRoute]++
			default:
				byState[s.State]++
			}
			if s.ProbeError != "" && (isRemoved || s.State != mcpproxy.StateFailed) {
				probeFailed++
			}
		}
		parts := []string{}
		part := func(n int, label, style string, always bool) {
			if n == 0 && !always {
				return
			}
			if n == 0 {
				style = styleDim
			}
			parts = append(parts, paint(c, style, fmt.Sprintf("%d %s", n, label)))
		}
		part(byState[mcpproxy.StateFailed], "failed", styleRed, true)
		part(probeFailed, "probe failed", styleRed, false)
		part(count[kindWarning], map[bool]string{true: "warning", false: "warnings"}[count[kindWarning] == 1], styleYellow, false)
		part(count[kindNoRoute], "no route", styleYellow, false)
		part(byState[mcpproxy.StateRunning], "running", styleGreen, true)
		part(byState[mcpproxy.StateStarting], "starting", styleBold, false)
		part(byState[mcpproxy.StateIdleStopped], "idle", styleDim, false)
		part(byState[mcpproxy.StateNotStarted], "ready", styleDim, false)
		part(count[kindRemoved], "removed", styleDim, false)
		parts[0] = fmt.Sprintf("%d servers: ", len(p.rep.Servers)) + parts[0]
		head = append(head, joinWrapped(parts, c, p.width)...)
	}
	for _, w := range installWarnings(p) {
		for _, line := range wrap("! "+w, p.width, 2) {
			head = append(head, paint(c, styleYellow, line))
		}
	}
	blocks = append(blocks, head)

	// Rows: the shown servers in aligned columns, each problem followed by
	// its error (and a failed start's stderr) on indented lines.
	type row struct {
		server
		cells  [4]string
		style  string
		detail []string // plain text
		dStyle []string
	}
	var rows []row
	var notStarted, noRoute []string
	w := [3]int{}
	for _, s := range servers {
		if !p.all && (s.kind == kindNotStarted || s.kind == kindNoRoute || s.kind == kindRemoved) {
			switch s.kind {
			case kindNotStarted:
				notStarted = append(notStarted, printable(s.Server))
			case kindNoRoute:
				noRoute = append(noRoute, printable(s.Server))
			}
			continue
		}
		label, style := stateLabel(s.State)
		if s.kind == kindFailed && s.State == mcpproxy.StateFailed {
			label += stateAge(p.now, s.LastErrorAt) // a failure is last known, not live
		}
		switch s.kind {
		case kindRemoved:
			label, style = "○ removed", styleDim
		case kindNoRoute:
			label, style = "○ no route", styleYellow
		}
		last := "never called"
		if !s.LastCall.IsZero() {
			last = "called " + mcpproxy.Ago(p.now, s.LastCall)
		}
		r := row{server: s, cells: [4]string{printable(s.Server), label, plural(s.Tools, "tool", "tools"), last}, style: style}
		add := func(text, style string) {
			r.detail = append(r.detail, cut(printable(text), p.width-6))
			r.dStyle = append(r.dStyle, style)
		}
		age := ""
		if !s.LastErrorAt.IsZero() {
			age = " · " + mcpproxy.Ago(p.now, s.LastErrorAt)
		}
		ek := s.kind
		if ek == kindRemoved {
			ek = errorKind(s.ServerReport) // its entry left the config; its backend's errors are still its own
		}
		switch {
		case ek == kindFailed:
			if e := cmp.Or(s.ProbeError, s.LastError); e != "" {
				add(e, styleRed)
			}
			if s.State == mcpproxy.StateFailed {
				for _, line := range s.StderrTail {
					add(line, styleDim)
				}
			}
		case ek == kindWarning:
			// The error is cut, never its age.
			r.detail = append(r.detail, cut(printable(s.LastError), p.width-6-runewidth.StringWidth(age))+age)
			r.dStyle = append(r.dStyle, styleYellow)
		case s.LastError != "" && p.all:
			add("(old) "+s.LastError+age, styleDim)
		}
		for i, v := range r.cells[:3] {
			w[i] = max(w[i], runewidth.StringWidth(v))
		}
		rows = append(rows, r)
	}
	// On a narrow terminal the name column gives way (to 8 at least); when
	// that is not enough, rows keep the name and state only.
	lastW := 0
	for _, r := range rows {
		lastW = max(lastW, runewidth.StringWidth(r.cells[3]))
	}
	narrow := false
	if over := 2 + w[0] + 2 + w[1] + 2 + w[2] + 2 + lastW - p.width; p.width > 0 && over > 0 {
		if w[0]-over >= 8 {
			w[0] -= over
		} else {
			narrow = true
			w[0] = max(min(w[0], p.width-4-w[1]), 8)
		}
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.cells[0]
	}
	names = cutDistinct(names, w[0])
	var problems, rest []string
	for i, r := range rows {
		r.cells[0] = names[i]
		// The last column is not padded, so a line never ends in spaces.
		line := "  " + pad(r.cells[0], w[0]) + "  " + paint(c, r.style, r.cells[1])
		if !narrow {
			line = "  " + pad(r.cells[0], w[0]) + "  " + paint(c, r.style, pad(r.cells[1], w[1])) + "  " + pad(r.cells[2], w[2]) + "  " +
				paint(c, map[bool]string{true: styleDim}[r.LastCall.IsZero()], r.cells[3])
		}
		lines := []string{line}
		for i, d := range r.detail {
			lines = append(lines, paint(c, r.dStyle[i], "      "+d))
		}
		if r.kind <= kindNoRoute {
			problems = append(problems, lines...)
		} else {
			rest = append(rest, lines...)
		}
	}
	if len(noRoute) > 0 { // with the problems: a session's calls to these fail
		for _, line := range wrapItems(heading(p.width, fmt.Sprintf("  ! %d no route: ", len(noRoute))), noRoute, p.width, 4) {
			problems = append(problems, paint(c, styleYellow, line))
		}
		for _, line := range wrap("    "+noRouteFix, p.width, 4) {
			problems = append(problems, paint(c, styleDim, line))
		}
	}
	blocks = append(blocks, problems, rest)
	if len(notStarted) > 0 {
		var ns []string
		h := heading(p.width, fmt.Sprintf("  ○ %d ready, start on first call: ", len(notStarted)), fmt.Sprintf("  ○ %d ready: ", len(notStarted)))
		for _, line := range wrapItems(h, notStarted, p.width, 4) {
			ns = append(ns, paint(c, styleDim, line))
		}
		blocks = append(blocks, ns)
	}

	first := true
	emit := func(blocks ...[]string) {
		for _, b := range blocks {
			if len(b) == 0 {
				continue
			}
			if !first {
				_, _ = fmt.Fprintln(out)
			}
			first = false
			_, _ = fmt.Fprintln(out, strings.Join(b, "\n"))
		}
	}
	// The proxy's part is out before the gateway is asked: a gateway that
	// stalls never holds back what the proxy said.
	emit(blocks...)
	if p.gateway != nil {
		if g := p.gateway(); g != "" {
			emit([]string{g})
		}
	}
	var notes []string
	for _, n := range installNotes(p) {
		for _, line := range wrap("· "+n, p.width, 2) {
			notes = append(notes, paint(c, styleDim, line))
		}
	}
	emit(notes)
}

// installWarnings are what the owner should act on, from the install state.
func installWarnings(p statusPage) []string {
	if p.installErr != nil {
		return []string{"install state unknown: " + p.installErr.Error()}
	}
	st := p.install
	if st == nil {
		return nil
	}
	if st.State == mcpproxy.InstallNotInstalled {
		return []string{"not installed: `aimux mcp proxy install` moves Claude Code's stdio and HTTP servers behind the proxy"}
	}
	var out []string
	if len(st.Drifted) > 0 {
		out = append(out, fmt.Sprintf("drifted: %s (its entry is no longer the proxy URL: a running session saved an old copy, or it was edited); to fix it, %s",
			strings.Join(st.Drifted, ", "), mcpproxy.DriftFix))
	}
	if len(st.NotProxied) > 0 {
		out = append(out, fmt.Sprintf("not behind the proxy: %s (`aimux mcp proxy install` moves them)", strings.Join(st.NotProxied, ", ")))
	}
	if st.UpgradePending && p.upgradeText != "" {
		out = append(out, p.upgradeText)
	}
	if !st.Hooks {
		out = append(out, "hooks off: servers you add later stay direct; `aimux mcp proxy install` adds the hooks")
	}
	return out
}

// installNotes are the install facts at rest, shown dim.
func installNotes(p statusPage) []string {
	st := p.install
	if st == nil || st.State == mcpproxy.InstallNotInstalled {
		return nil
	}
	out := []string{fmt.Sprintf("%d servers behind the proxy; change one with `aimux mcp proxy edit <server>`", len(st.Moved))}
	if st.Hooks {
		out = append(out, "hooks on: a server you add in a session moves behind the proxy; servers still direct are named when a session starts")
	}
	if len(st.Removed) > 0 {
		out = append(out, fmt.Sprintf("removed from %s: %s (still served by the proxy, unused; `aimux mcp proxy install` takes them off)",
			p.claudeConfig, strings.Join(st.Removed, ", ")))
	}
	if len(st.Skipped) > 0 {
		out = append(out, fmt.Sprintf("kept direct: %s (`aimux mcp proxy install --unskip <name>` moves one)", strings.Join(st.Skipped, ", ")))
	}
	return append(out, mcpproxy.KeptDirectLines(st.KeptDirect)...)
}

// gatewayStatusStyled is the tool gateway in the page's style, from its
// status or the error asking for it, broken at a separator to fit width;
// ok is false when the worker is not running or did not answer.
func gatewayStatusStyled(st toolgateway.ControlStatus, err error, now time.Time, color bool, width int) (line string, ok bool) {
	name := paint(color, styleBold, "tool gateway")
	var parts []string
	switch {
	case errors.Is(err, toolgateway.ErrGatewayNotRunning):
		parts = []string{name + " " + paint(color, styleRed, "● not running"), "Workspace turns fail", "run `aimux mcp proxy gateway install`"}
	case err != nil:
		parts = []string{name + " " + paint(color, styleYellow, "? unknown") + ": " + err.Error()}
	default:
		state := "● running"
		if !st.StartedAt.IsZero() {
			state += " " + span(now, st.StartedAt)
		}
		parts = []string{name + " " + paint(color, styleGreen, state), plural(st.Leases, "lease", "leases"), plural(len(st.Backends), "backend", "backends")}
		if len(st.Down) > 0 {
			down := make([]string, len(st.Down))
			for i, n := range st.Down {
				down[i] = n
				if at, ok := st.Checked[n]; ok { // an older worker reports no times
					down[i] += " (checked " + mcpproxy.Ago(now, at) + ")"
				}
			}
			lines := joinWrapped(parts, color, width)
			prefix := fmt.Sprintf("%d down: ", len(st.Down))
			last := len(lines) - 1
			if one := prefix + strings.Join(down, ", "); width <= 0 ||
				runewidth.StringWidth(escapes.ReplaceAllString(lines[last], ""))+runewidth.StringWidth(" · ")+runewidth.StringWidth(one) <= width {
				lines[last] += paint(color, styleDim, " · ") + paint(color, styleRed, one)
			} else {
				for _, l := range wrapItems("  "+prefix, down, width, 4) {
					lines = append(lines, paint(color, styleRed, l))
				}
			}
			return strings.Join(lines, "\n"), true
		}
	}
	return strings.Join(joinWrapped(parts, color, width), "\n"), err == nil
}
