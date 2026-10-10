// Package sessionmcp serves Claude Code session search and resume tools over
// MCP, on top of the internal/search index.
package sessionmcp

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zanetworker/aimux/internal/search"
)

const (
	maxOutputChars      = 100_000 // a tool result never exceeds this
	maxExchangeChars    = 20_000  // one exchange's prose is cut here
	fullExchanges       = 20      // virtual sessions this short are shown whole
	defaultNumExchanges = 10      // otherwise the last this many
	maxVirtualSessions  = 20      // most sessions one virtual session merges
	truncMark           = "…[truncated]"
)

// formatResults renders search hits, naming the ranking actually used.
func formatResults(rs []search.Result, mode string, semantic bool) string {
	if len(rs) == 0 {
		return "No sessions found."
	}
	ranking := search.ModeKeyword
	if semantic {
		ranking = mode
		if ranking == "" {
			ranking = search.ModeHybrid
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d sessions (ranking: %s", len(rs), ranking)
	if !semantic && mode != search.ModeKeyword {
		b.WriteString("; semantic unavailable: set OPENAI_API_KEY")
	}
	b.WriteString(")\n\n")
	for i, r := range rs {
		fmt.Fprintf(&b, "%d. **%s** `%s`\n   %s · %s\n", i+1, title(r.Title), r.SessionID, r.CWD, age(r.ModTime))
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatList renders one line per session, newest first as given.
func formatList(rs []search.Result) string {
	if len(rs) == 0 {
		return "No sessions found."
	}
	var b strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&b, "- `%s` %s · **%s** · %s\n", r.SessionID, r.ModTime.Format("2006-01-02 15:04"), title(r.Title), r.CWD)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatSession renders a session. beforeSeq > 0 keeps only exchanges
// before that seq; lastN > 0 then keeps the last lastN of those. Output past
// maxOutputChars keeps the newest, and the heading says how to page back.
func formatSession(t search.Transcript, lastN, beforeSeq int) string {
	ex := t.Exchanges
	if beforeSeq > 0 {
		ex = ex[:sort.Search(len(ex), func(i int) bool { return ex[i].Seq >= beforeSeq })]
	}
	if lastN > 0 && lastN < len(ex) {
		ex = ex[len(ex)-lastN:]
	}
	head := header(t)
	blocks, kept := fitNewest(ex, maxOutputChars-len(head)-200)
	heading := fmt.Sprintf("## Transcript (%d of %d exchanges)", kept, len(t.Exchanges))
	if kept > 0 {
		shown := ex[len(ex)-kept:]
		if shown[0].Seq > t.Exchanges[0].Seq {
			heading = fmt.Sprintf("## Transcript (exchanges %d to %d of %d exchanges; for earlier ones call get_session with before_seq=%d)",
				shown[0].Seq, shown[kept-1].Seq, len(t.Exchanges), shown[0].Seq)
		}
	}
	return head + "\n" + heading + "\n\n" + blocks
}

// formatContinue renders a session for picking up work: how to resume it
// natively, then the newest exchanges that fit.
func formatContinue(t search.Transcript) string {
	d := t.Detail
	head := header(t) + fmt.Sprintf("**Native resume:** `%s`\n", resumeCommand(d.CWD, d.SessionID))
	blocks, kept := fitNewest(t.Exchanges, maxOutputChars-len(head)-200)
	heading := fmt.Sprintf("## Transcript (%d exchanges)", len(t.Exchanges))
	if kept < len(t.Exchanges) {
		heading = fmt.Sprintf("## Transcript (last %d of %d exchanges; for earlier ones call get_session with before_seq=%d)",
			kept, len(t.Exchanges), t.Exchanges[len(t.Exchanges)-kept].Seq)
	}
	return head + "\n" + heading + "\n\n" + blocks
}

// formatVirtual merges sessions oldest first. Short sessions are shown whole,
// long ones by their last numExchanges; the longest section shrinks first
// until the whole fits maxOutputChars.
func formatVirtual(ts []search.Transcript, numExchanges int) string {
	if numExchanges <= 0 {
		numExchanges = defaultNumExchanges
	}
	ts = append([]search.Transcript(nil), ts...)
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Detail.ModTime.Before(ts[j].Detail.ModTime) })

	shown := make([][]search.Exchange, len(ts))
	for i, t := range ts {
		shown[i] = t.Exchanges
		if len(t.Exchanges) > fullExchanges && len(t.Exchanges) > numExchanges {
			shown[i] = t.Exchanges[len(t.Exchanges)-numExchanges:]
		}
	}
	skip := 0 // oldest sessions dropped when one exchange each still does not fit
	render := func() string {
		var b strings.Builder
		fmt.Fprintf(&b, "# Virtual session (%d sessions merged, oldest first)\n\n", len(ts))
		if skip > 0 {
			fmt.Fprintf(&b, "_%d oldest sessions omitted to fit the output cap; load them with get_session._\n\n", skip)
		}
		for i, t := range ts {
			if i < skip {
				continue
			}
			d := t.Detail
			fmt.Fprintf(&b, "## Session %d of %d: %s (%s)\n\n", i+1, len(ts), short(d.SessionID), d.CWD)
			fmt.Fprintf(&b, "**Title:** %s · **Last modified:** %s · ", title(d.Title), d.ModTime.Format("2006-01-02 15:04"))
			if len(shown[i]) < len(t.Exchanges) {
				fmt.Fprintf(&b, "last %d of %d exchanges\n\n", len(shown[i]), len(t.Exchanges))
			} else {
				fmt.Fprintf(&b, "%d exchanges\n\n", len(t.Exchanges))
			}
			for _, e := range shown[i] {
				b.WriteString(exchange(e))
			}
		}
		return b.String()
	}
	out := render()
	for len(out) > maxOutputChars {
		longest := -1
		for i := skip; i < len(shown); i++ {
			if len(shown[i]) > 1 && (longest < 0 || sectionLen(shown[i]) > sectionLen(shown[longest])) {
				longest = i
			}
		}
		if longest < 0 {
			if skip >= len(ts)-1 {
				break // only the newest session left, and its one exchange is clipped
			}
			skip++
			out = render()
			continue
		}
		shown[longest] = shown[longest][1:]
		out = render()
	}
	return out
}

// resumeCommand is the shell line that reopens a session in Claude Code; the
// directory is single-quoted when it holds anything a shell would split.
func resumeCommand(cwd, id string) string {
	if cwd == "" {
		return "claude --resume " + id
	}
	return "cd " + shellQuote(cwd) + " && claude --resume " + id
}

const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+@:,="

func shellQuote(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return !strings.ContainsRune(shellSafe, r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func header(t search.Transcript) string {
	d := t.Detail
	var b strings.Builder
	fmt.Fprintf(&b, "# Session %s\n\n", d.SessionID)
	fmt.Fprintf(&b, "- **Title:** %s\n", title(d.Title))
	fmt.Fprintf(&b, "- **Directory:** %s\n", d.CWD)
	fmt.Fprintf(&b, "- **Last modified:** %s\n", d.ModTime.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "- **Exchanges:** %d\n", len(t.Exchanges))
	if d.Automated {
		b.WriteString("- **Kind:** automated (SDK or scheduled run)\n")
	}
	return b.String()
}

// fitNewest renders exchanges in order, dropping the oldest until the total
// fits budget. The newest exchange is always kept.
func fitNewest(ex []search.Exchange, budget int) (string, int) {
	var parts []string
	total := 0
	for i := len(ex) - 1; i >= 0; i-- {
		s := exchange(ex[i])
		if len(parts) > 0 && total+len(s) > budget {
			break
		}
		parts = append(parts, s)
		total += len(s)
	}
	var b strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		b.WriteString(parts[i])
	}
	return b.String(), len(parts)
}

func exchange(e search.Exchange) string {
	return fmt.Sprintf("**[USER]** %s\n\n**[EXCHANGE %d]** %s\n\n", clip(e.Prompt), e.Seq, clip(e.Prose))
}

func sectionLen(ex []search.Exchange) int {
	n := 0
	for _, e := range ex {
		n += len(exchange(e))
	}
	return n
}

// clip cuts s to maxExchangeChars bytes on a rune boundary.
func clip(s string) string {
	if len(s) <= maxExchangeChars {
		return s
	}
	cut := maxExchangeChars
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncMark
}

func title(t string) string {
	if strings.TrimSpace(t) == "" {
		return "(untitled)"
	}
	return t
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
