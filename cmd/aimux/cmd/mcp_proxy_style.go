package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
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
	cliVersion   string                                    // this aimux's version, for an upgrade restart pending
	installPort  int                                       // the port install recorded; 0 when none. Install fixes carry --port when it is not the default
	handRun      bool                                      // the proxy answering is not the installed launchd job: restart cannot reach it
	gateway      func() (toolgateway.ControlStatus, error) // the tool gateway's status; nil leaves its row out
	noAnswer     bool                                      // the proxy took the connection but gave no status
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

// span is how long ago t was, without "ago": "3h", "0s".
func span(now, t time.Time) string {
	if s := mcpproxy.Ago(now, t); s != "just now" {
		return strings.TrimSuffix(s, " ago")
	}
	return "0s"
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
