package terminput

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	chevron           = "❯"
	escFooter         = "Esc to cancel"
	questionPrefix    = "Do you want to"
	queuedHint        = "Press up to edit queued messages"
	placeholderPrefix = `Try "`
)

var optionLine = regexp.MustCompile(`^\s*(?:❯\s*)?(\d+)\.\s+(\S.*)$`)

// isRule reports whether a line is a horizontal rule of box-drawing dashes,
// loosely: leading spaces are ignored. Use it to reject lines, never to
// accept a box.
func isRule(line string) bool {
	line = strings.TrimSpace(line)
	return len(line) > 0 && strings.Trim(line, "─") == ""
}

// boxRule reports whether a line is a box rule: dashes from column 0, with
// only trailing spaces. An indented run of dashes inside a draft is not one.
func boxRule(line string) bool {
	return strings.HasPrefix(line, "─") && isRule(line)
}

// labeledTopRule reports a box's top rule with a label in it, as Claude Code
// draws a session's name ("────── name ─"): dashes from column 0, ending in a
// dash, mostly dashes, and exactly width wide like the rule below the box.
func labeledTopRule(line string, width int) bool {
	t := strings.TrimRight(line, " \t\r")
	if !strings.HasPrefix(t, "─") || !strings.HasSuffix(t, "─") || ruleWidth(t) != width {
		return false
	}
	return 2*strings.Count(t, "─") > width
}

func ruleWidth(line string) int {
	return utf8.RuneCountInString(strings.TrimRight(line, " \t\r"))
}

// InputBoxEmpty reports whether the screen shows Claude Code's live input box
// (a single chevron line between two rules, the bottom-most pair of rules on
// screen) with nothing typed in it. It judges only that bottom-most structure
// and never falls back to an older box higher up: anything unclear (a wrapped
// draft, a box cut at the bottom, bash mode, a prompt below) gives false.
//
// Claude Code's grey placeholders count as empty; the screen text carries no
// colour, so they are recognised by their wording. Known gap: a draft typed
// exactly in the placeholder form Try "..." is accepted as empty.
func InputBoxEmpty(screen string) bool {
	rows, ok := bottomBox(screen)
	if !ok || len(rows) != 1 {
		return false
	}
	line := strings.TrimSpace(rows[0])
	if !strings.HasPrefix(line, chevron) {
		return false
	}
	text := strings.TrimSpace(strings.TrimPrefix(line, chevron))
	return text == "" || text == queuedHint ||
		(strings.HasPrefix(text, placeholderPrefix) && strings.HasSuffix(text, `"`) && len(text) > len(placeholderPrefix))
}

// bottomBox returns the rows between the bottom-most pair of box rules of
// equal width, when nothing after the lower rule looks like an input line or
// a prompt footer.
func bottomBox(screen string) ([]string, bool) {
	lines := strings.Split(screen, "\n")
	lower := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if boxRule(lines[i]) {
			lower = i
			break
		}
	}
	if lower < 0 {
		return nil, false
	}
	for _, l := range lines[lower+1:] {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, chevron) || strings.HasPrefix(t, escFooter) {
			return nil, false // an unclosed box or a prompt sits below the rule
		}
	}
	upper := -1
	for i := lower - 1; i >= 0; i-- {
		if boxRule(lines[i]) || labeledTopRule(lines[i], ruleWidth(lines[lower])) {
			upper = i
			break
		}
	}
	if upper < 0 || lower-upper < 2 || ruleWidth(lines[upper]) != ruleWidth(lines[lower]) {
		return nil, false
	}
	return lines[upper+1 : lower], true
}

// pastedMarker is how Claude Code shows a long paste in its input box.
var pastedMarker = regexp.MustCompile(`^\[Pasted text #\d+( \+\d+ lines?)?\]$`)

// PasteText reports whether text goes in as a bracketed paste: it has a
// newline or a tab, or more than 200 characters.
func PasteText(text string) bool {
	return strings.ContainsAny(text, "\n\t") || utf8.RuneCountInString(text) > pasteOver
}

// pasteOver is the length in characters above which text is pasted.
const pasteOver = 200

// TextInBox reports whether the screen shows text, just typed, waiting in
// Claude Code's input box, so pressing Enter submits it and nothing else: no
// prompt is showing, the bottom-most box starts with one chevron line and its
// other rows are indented continuations, and the box holds text (compared
// without whitespace, since Claude Code wraps it) or, for pasted text,
// Claude Code's "[Pasted text #n]" marker. Anything else gives false.
func TextInBox(screen, text string) bool {
	box, rows, ok := boxText(screen)
	if !ok || squeeze(text) == "" {
		return false
	}
	if squeeze(box) == squeeze(text) {
		return true
	}
	return rows == 1 && PasteText(text) && pastedMarker.MatchString(box)
}

// TextShownInBox is TextInBox without the paste marker: the box visibly
// holds text itself (compared without whitespace).
func TextShownInBox(screen, text string) bool {
	box, _, ok := boxText(screen)
	return ok && squeeze(text) != "" && squeeze(box) == squeeze(text)
}

// WordsInBox is TextShownInBox keeping word breaks: the box holds the same
// words as text, in order, with only the spacing changed and its rows
// wrapped either at a space or inside a word (a long path or URL). A text
// that matches only with its spaces removed is someone else's.
func WordsInBox(screen, text string) bool {
	rows, ok := boxRows(screen)
	want := strings.Join(strings.Fields(text), " ")
	if !ok || want == "" {
		return false
	}
	pos := 0
	for i, r := range rows {
		r = strings.Join(strings.Fields(r), " ")
		if !strings.HasPrefix(want[pos:], r) {
			return false
		}
		pos += len(r)
		// A row ends at a space or inside a word; rows are trimmed, so a
		// space here in text can only be that break.
		if i < len(rows)-1 && pos < len(want) && want[pos] == ' ' {
			pos++
		}
	}
	return pos == len(want)
}

// BoxHasText reports an input box, with no prompt showing, that holds text
// on its chevron line: a draft, or Claude Code's grey suggestion (the screen
// text carries no colour). Bash mode and unclear screens give false.
func BoxHasText(screen string) bool {
	box, _, ok := boxText(screen)
	return ok && strings.TrimSpace(box) != ""
}

// boxText is the text in Claude Code's input box and its row count, when
// no prompt shows and the box has the form TextInBox describes.
func boxText(screen string) (string, int, bool) {
	rows, ok := boxRows(screen)
	if !ok {
		return "", 0, false
	}
	return strings.Join(rows, " "), len(rows), true
}

// boxRows is the input box's rows, trimmed, chevron removed from the first.
func boxRows(screen string) ([]string, bool) {
	if ParsePrompt(screen) != nil {
		return nil, false
	}
	rows, ok := bottomBox(screen)
	if !ok || !strings.HasPrefix(rows[0], chevron) {
		return nil, false
	}
	shown := []string{strings.TrimSpace(strings.TrimPrefix(rows[0], chevron))}
	for _, r := range rows[1:] {
		t := strings.TrimSpace(r)
		if !strings.HasPrefix(r, "  ") || t == "" || strings.HasPrefix(t, chevron) || isRule(r) {
			return nil, false
		}
		shown = append(shown, t)
	}
	return shown, true
}

// squeeze drops all whitespace.
func squeeze(s string) string { return strings.Join(strings.Fields(s), "") }

// ParsePrompt returns the live permission prompt on screen: the bottom-most
// "Do you want to" question, directly followed by options numbered 1..n and
// the "Esc to cancel" footer, with no input box or output after the footer.
// Any other screen, including a prompt scrolled partly off or already
// answered, gives nil.
func ParsePrompt(screen string) *Prompt {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		q := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(q, questionPrefix) {
			continue
		}
		opts, rest := parseOptions(lines[i+1:])
		if len(opts) < 2 || !liveAfterFooter(rest) {
			return nil
		}
		body := promptBody(lines[:i])
		return &Prompt{ID: promptID(body, q, opts), Question: q, Detail: clip(body), Options: opts}
	}
	return nil
}

const (
	detailLines = 20
	detailChars = 2000
)

// promptBody returns the lines between the bottom-most box rule in above and
// its end, dedented and without leading or trailing blank lines; "" when
// above has no box rule.
func promptBody(above []string) string {
	start := -1
	for i := len(above) - 1; i >= 0; i-- {
		if boxRule(above[i]) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	var rows []string
	for _, l := range above[start:] {
		rows = append(rows, strings.TrimRight(l, " \t\r"))
	}
	for len(rows) > 0 && rows[0] == "" {
		rows = rows[1:]
	}
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	indent := -1
	for _, r := range rows {
		if r == "" || dashes(r) {
			continue
		}
		n := len(r) - len(strings.TrimLeft(r, " "))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	for i, r := range rows {
		if dashes(r) {
			rows[i] = strings.TrimSpace(r)
		} else if len(r) >= indent && indent > 0 {
			rows[i] = r[indent:]
		}
	}
	return strings.Join(rows, "\n")
}

// dashes reports whether a line is only a dashed or solid rule, which
// Claude Code draws around file previews without the box's indent.
func dashes(line string) bool {
	t := strings.TrimSpace(line)
	return t != "" && strings.Trim(t, "╌─") == ""
}

// clip keeps the first detailLines lines and detailChars characters.
func clip(body string) string {
	if lines := strings.Split(body, "\n"); len(lines) > detailLines {
		body = strings.Join(lines[:detailLines], "\n")
	}
	if utf8.RuneCountInString(body) > detailChars {
		body = string([]rune(body)[:detailChars])
	}
	return body
}

// liveAfterFooter reports whether nothing but blank lines follows a prompt
// footer. Tool output, a spinner, an input box or a diff rule after it mean
// the prompt was already answered.
func liveAfterFooter(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// parseOptions reads options numbered 1..n (n <= 9) up to the footer. Blank
// lines may separate options; a wrapped label line is indented deeper than the
// option's number and is joined with one space. It returns the lines after the
// footer, or nil options when the block is malformed or the footer is missing.
func parseOptions(lines []string) ([]Option, []string) {
	var opts []Option
	numberCol := 0
	blank := true // blank lines are allowed before the first option
	for n, raw := range lines {
		text := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(text, escFooter):
			return opts, lines[n+1:]
		case text == "":
			blank = true
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if len(opts) > 0 && !blank && indent > numberCol && !isRule(raw) && !strings.HasPrefix(text, "⏺") {
			opts[len(opts)-1].Label += " " + text
			continue
		}
		m := optionLine.FindStringSubmatch(raw)
		if m == nil || m[1] != strconv.Itoa(len(opts)+1) || len(opts) >= 9 {
			return nil, nil
		}
		numberCol = utf8.RuneCountInString(raw[:strings.Index(raw, m[1])])
		opts = append(opts, Option{Key: Key(m[1]), Label: strings.TrimSpace(m[2])})
		blank = false
	}
	return nil, nil
}

// promptID hashes the whole body (not the clipped Detail), the question and
// the labels, so two prompts asking the same about different commands differ.
func promptID(body, question string, opts []Option) string {
	parts := []string{body, question}
	for _, o := range opts {
		parts = append(parts, o.Label)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

// spinnerLine is Claude Code's working line: a spinner glyph at the left
// edge, a verb ending in an ellipsis and an optional "(12s · ↓ 76 tokens)".
// A finished turn's line ("✻ Worked for 12s") has no ellipsis.
var spinnerLine = regexp.MustCompile(`^ ?[·✢✳✶✻✽*] \S.*(…|\.\.\.)( \(.*\))?\s*$`)

const (
	interruptHint = "esc to interrupt"
	// workingScan is how many lines above the input box may hold the
	// spinner: it sits below the last message, above any todo list.
	workingScan = 12
)

// Working reports whether the screen shows Claude Code working on a turn:
// its spinner line, or its "(esc to interrupt" / "· esc to interrupt" hint,
// in the lines just above the bottom-most input box (or the screen's last
// lines when no box shows), below the last message; or "esc to interrupt"
// in the footer below the box. A false positive only refuses a send.
func Working(screen string) bool {
	lines := strings.Split(screen, "\n")
	end, footer := len(lines), lines[len(lines):]
	if upper, lower, ok := bottomRules(lines); ok {
		end, footer = upper, lines[lower+1:]
	}
	for _, l := range footer {
		if strings.Contains(strings.ToLower(l), interruptHint) {
			return true
		}
	}
	for i := end - 1; i >= 0 && i >= end-workingScan; i-- {
		l := lines[i]
		if strings.HasPrefix(strings.TrimSpace(l), "⏺") {
			return false
		}
		if spinnerLine.MatchString(l) || interruptShown(l) {
			return true
		}
	}
	return false
}

// interruptShown reports whether a line above the box carries Claude Code's
// own interrupt hint, "(esc to interrupt" or "· esc to interrupt". The bare
// phrase in a reply's prose is not the hint.
func interruptShown(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "("+interruptHint) || strings.Contains(l, "· "+interruptHint)
}

// bottomRules returns the indexes of the bottom-most pair of box rules of
// equal width with at least one row between them.
func bottomRules(lines []string) (upper, lower int, ok bool) {
	lower = -1
	for i := len(lines) - 1; i >= 0; i-- {
		if boxRule(lines[i]) {
			lower = i
			break
		}
	}
	for i := lower - 1; lower >= 0 && i >= 0; i-- {
		if boxRule(lines[i]) {
			if lower-i < 2 || ruleWidth(lines[i]) != ruleWidth(lines[lower]) {
				return 0, 0, false
			}
			return i, lower, true
		}
	}
	return 0, 0, false
}
