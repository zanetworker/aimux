package runcheck

import (
	"bufio"
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ReasonPrivateName refuses a query that names something the private inputs
// name (a customer, a person, a codename) and no public-names phrase covers.
const ReasonPrivateName = "private_name"

// builtinPublicNames is the shipped list of public names, one phrase per
// line; the owner's own file extends it (LoadPublicNames).
//
//go:embed public-names.txt
var builtinPublicNames string

// neverNames are capitalized words that are never names: months, weekdays
// and the pronoun I.
var neverNames = map[string]bool{
	"i": true, "january": true, "february": true, "march": true, "april": true, "may": true, "june": true,
	"july": true, "august": true, "september": true, "october": true, "november": true, "december": true,
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": true, "sunday": true,
}

// functionWords never count as a labeled name ("project the plan").
var functionWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "was": true, "to": true, "of": true, "for": true,
	"in": true, "on": true, "with": true, "and": true, "or": true, "that": true, "this": true, "it": true, "its": true,
	"be": true, "as": true, "by": true, "at": true, "from": true, "has": true, "have": true, "will": true, "we": true,
	"our": true, "their": true, "team": true, "name": true, "names": true, "list": true,
}

// labeledRE finds a name given by a label with a separator, in any case and
// any quoting: "codename: bluefin", {"codename":"bluefin"}, project = "x".
// Only the one word after the label counts; a label word in prose with no
// separator ("customer adoption") marks nothing.
var labeledRE = regexp.MustCompile(`(?i)\b(?:customer|client|account|codename|code name|project|contact|owner|partner)s?["']?\s*[:=]\s*["']?([\p{L}\p{N}]+)`)

// sentenceBreak reports whether r, the last rune before a word that is
// neither a space nor inline formatting, starts a new sentence or field, so
// a capital there says nothing.
func sentenceBreak(r rune) bool {
	return strings.ContainsRune(".!?:;{}[]<>|•\n\r\t=", r)
}

// inlineMark is formatting or quoting around a word ("**Globex**",
// "\"Globex\"", "(Globex"), skipped when looking back for a sentence break.
func inlineMark(r rune) bool {
	return r == ' ' || strings.ContainsRune("*_`~\"'()#", r)
}

// nameCandidates is every word the private texts use as a name, lower-cased:
// a word capitalized mid-sentence that never appears in lower case in them
// (nor is a month, weekday or I), every mixed-case word ("OpenShell"), and
// every labeled one ("codename: bluefin").
func nameCandidates(texts []string) map[string]bool {
	lower := map[string]bool{}
	capMid := map[string]bool{}
	out := map[string]bool{}
	for _, s := range texts {
		s = norm.NFKC.String(s)
		for _, loc := range wordRE.FindAllStringIndex(s, -1) {
			w := s[loc[0]:loc[1]]
			lw := strings.ToLower(w)
			if len([]rune(w)) < 2 || isDigits(w) {
				continue
			}
			switch {
			case w == lw:
				lower[lw] = true
			case mixedCase(w):
				out[lw] = true
			case !afterBreak(s, loc[0]):
				capMid[lw] = true
			}
		}
		for _, m := range labeledRE.FindAllStringSubmatch(s, -1) {
			if lw := strings.ToLower(m[1]); !functionWords[lw] && !isDigits(lw) && len([]rune(lw)) > 1 {
				out[lw] = true
			}
		}
	}
	for w := range capMid {
		if !lower[w] && !neverNames[w] {
			out[w] = true
		}
	}
	return out
}

// afterBreak reports whether the word at byte i starts its text, a line, a
// sentence or a field.
func afterBreak(s string, i int) bool {
	for i > 0 {
		r := rune(s[i-1])
		if s[i-1] >= 0x80 { // step back over a multi-byte rune
			j := i - 1
			for j > 0 && s[j]&0xC0 == 0x80 {
				j--
			}
			r = []rune(s[j:i])[0]
			i = j
		} else {
			i--
		}
		if inlineMark(r) {
			continue
		}
		return sentenceBreak(r)
	}
	return true
}

// mixedCase reports a word with an upper-case letter after its first rune
// and a lower-case one anywhere ("OpenShell", "vLLM"), not an all-caps one.
func mixedCase(w string) bool {
	rs := []rune(w)
	hasLower, upperAfterFirst := false, false
	for i, r := range rs {
		if unicode.IsLower(r) {
			hasLower = true
		}
		if i > 0 && unicode.IsUpper(r) {
			upperAfterFirst = true
		}
	}
	return hasLower && upperAfterFirst
}

func isDigits(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// phrases tokenizes each public name into its lower-cased words.
func phrases(names []string) [][]string {
	var out [][]string
	for _, n := range names {
		if ws := words(n); len(ws) > 0 {
			out = append(out, ws)
		}
	}
	return out
}

// builtinPhrases is the shipped list, tokenized once.
var builtinPhrases = phrases(strings.Split(builtinPublicNames, "\n"))

// namesPrivate reports whether q, once every public phrase in it is blanked
// out (whole phrases only), still has a word among cands.
func namesPrivate(q string, cands map[string]bool, public [][]string) bool {
	if len(cands) == 0 {
		return false
	}
	w := words(q)
	masked := make([]bool, len(w))
	for _, p := range public {
		for i := 0; i+len(p) <= len(w); i++ {
			match := true
			for j := range p {
				if w[i+j] != p[j] {
					match = false
					break
				}
			}
			if match {
				for j := range p {
					masked[i+j] = true
				}
			}
		}
	}
	for i, x := range w {
		if !masked[i] && cands[x] {
			return true
		}
	}
	return false
}

// privateTexts is every private input of p.
func privateTexts(p Private) []string {
	out := []string{p.Question, p.PageText, p.Notes}
	out = append(out, p.Prior...)
	out = append(out, p.ToolInputs...)
	return append(out, p.ToolOutputs...)
}

// LoadPublicNames reads the owner's public names, one phrase per line;
// blank lines and lines starting with # are skipped. A missing file is no
// names and no error.
func LoadPublicNames(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the owner's own config file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err // a list read in part is no list: the shipped one alone applies
	}
	return out, nil
}
