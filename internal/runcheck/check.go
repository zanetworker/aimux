// Package runcheck decides which web search queries a research-with-work
// planner proposed may leave the machine. The check is deterministic and
// fails closed: a query that looks like it carries private content (a URL,
// an address, an issue key, a credential, a token, or a span copied from
// what the planner read) is rejected, with a reason that never repeats the
// query. No model approves a query.
package runcheck

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// Limits on what a run may send to the web.
const (
	MaxQueries     = 5
	MaxQueryRunes  = 200
	MaxTotalBytes  = 600
	overlapWords   = 8  // a shared run of this many words is a copied span
	minEntropyRun  = 24 // shortest run checked for high entropy
	entropyBitsMin = 3.5
)

// Rejection reasons, a fixed set: a reason never includes the query text.
const (
	ReasonEmpty        = "empty"
	ReasonTooLong      = "too_long"
	ReasonBudget       = "budget"
	ReasonTooMany      = "too_many"
	ReasonControlChars = "control_chars"
	ReasonURL          = "url"
	ReasonEmail        = "email"
	ReasonIP           = "ip"
	ReasonHostname     = "hostname"
	ReasonIssueKey     = "issue_key"
	ReasonToken        = "token"
	ReasonCredential   = "credential"
	ReasonUUID         = "uuid"
	ReasonHighEntropy  = "high_entropy"
	ReasonOverlap      = "overlap"
	ReasonEncoding     = "encoding"
)

// Private is everything the planner saw that must not reach the web: the
// question, the page text, its notes, and its tool calls' inputs and outputs.
type Private struct {
	Question, PageText, Notes string
	ToolInputs, ToolOutputs   []string
	Prior                     []string // the page's earlier questions, and a run follow-up's earlier answers
}

// Rejection is one refused query: its position in the planner's list and why.
type Rejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

func (r Rejection) String() string { return fmt.Sprintf("query %d: %s", r.Index+1, r.Reason) }

var (
	urlRE          = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://|\bwww\.`)
	emailRE        = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[^\s@]+`)
	ipv4RE         = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)
	ipv6RE         = regexp.MustCompile(`(?i)\b[0-9a-f]{0,4}(:[0-9a-f]{0,4}){2,7}\b`)
	dottedRE       = regexp.MustCompile(`[\p{L}\p{N}_-]+(?:\.[\p{L}\p{N}_-]+)+`)
	versionRE      = regexp.MustCompile(`(?i)^v?\d+(?:\.\d+)+$`)
	issueKeyRE     = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)          // an upper-case key: always refused
	anySpacedKeyRE = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]{1,9}) (\d+)\b`)  // a spaced key in any case, matched only against keys the planner saw
	anyKeyRE       = regexp.MustCompile(`(?i)\b[a-z][a-z0-9]{1,9}-(\d+)\b`)    // any case: refused with 4+ digits or when the planner saw it
	issueKeySpRE   = regexp.MustCompile(`\b([A-Z][A-Z0-9]{1,9}) (\d+)(\.\d)?`) // an upper-case key, a space, a number
	credentialRE   = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|auth|client[_-]?secret)\s*[=:]\s*\S+|\b(password|passwd|passphrase|secret|token|api[ _-]?key|access[ _-]?key)\s+is\s+\S+`)
	awsKeyRE       = regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA|ANVA|AIPA)[A-Z0-9]{16}\b`)
	uuidRE         = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	jwtRE          = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	runRE          = regexp.MustCompile(`[A-Za-z0-9+/=_-]{24,}`)
	wordRE         = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

// Check returns the queries that may be sent, normalized, and why each
// other one may not. Rules apply in order: normalization; control
// characters; empty and length; patterns on the text and on it
// percent-decoded once; overlap with the private inputs; then the count and
// byte budget, so a rejected query never uses up the budget.
func Check(queries []string, p Private) (accepted []string, rejected []Rejection) {
	private, keys := shingles(p), privateKeys(p)
	total := 0
	for i, raw := range queries {
		reason := ""
		q := normalize(raw)
		switch {
		case hasControl(raw):
			reason = ReasonControlChars
		case q == "":
			reason = ReasonEmpty
		case len([]rune(q)) > MaxQueryRunes:
			reason = ReasonTooLong
		default:
			// The decoded form first: it shows what an encoded query hides.
			// One that does not decode is refused, since its decoded form
			// cannot be checked.
			dec, err := url.QueryUnescape(q)
			if err != nil {
				reason = ReasonEncoding
				break
			}
			dec = normalize(dec)
			if dec != q {
				reason = patternReason(dec)
			}
			if reason == "" {
				reason = patternReason(q)
			}
			if reason == "" && (seenKey(q, keys) || seenKey(dec, keys)) {
				reason = ReasonIssueKey
			}
			if reason == "" && (overlaps(q, private) || overlaps(dec, private)) {
				reason = ReasonOverlap
			}
		}
		if reason == "" {
			switch {
			case len(accepted) == MaxQueries:
				reason = ReasonTooMany
			case total+len(q) > MaxTotalBytes:
				reason = ReasonBudget
			}
		}
		if reason != "" {
			rejected = append(rejected, Rejection{Index: i, Reason: reason})
			continue
		}
		accepted = append(accepted, q)
		total += len(q)
	}
	return accepted, rejected
}

// normalize is NFKC with look-alike dots and dashes folded to ASCII and
// whitespace collapsed and trimmed, so an address or key written with them
// reads as one.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.Map(fold, norm.NFKC.String(s))), " ")
}

// fold maps the dots NFKC leaves (ideographic and halfwidth full stops,
// small and fullwidth full stops, one-dot leader) to "." and every Unicode
// dash and the minus sign to "-".
func fold(r rune) rune {
	switch {
	case r == '\u3002' || r == '\uff61' || r == '\ufe52' || r == '\uff0e' || r == '\u2024' || r == '\ufe12':
		return '.'
	case r == '\u2212' || r == '\ufe63' || r == '\uff0d' || (r != '-' && unicode.Is(unicode.Pd, r)):
		return '-'
	}
	return r
}

// hasControl reports control, bidi and zero-width characters other than
// plain whitespace, which could hide or reorder what a query says.
func hasControl(s string) bool {
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r), unicode.In(r, unicode.Cf):
			return true
		}
	}
	return false
}

// patternReason is the first private-looking pattern in q, "" when none.
func patternReason(q string) string {
	switch {
	case urlRE.MatchString(q):
		return ReasonURL
	case emailRE.MatchString(q):
		return ReasonEmail
	case ipv4RE.MatchString(q), ipv6RE.MatchString(q):
		return ReasonIP
	case issueKeyRE.MatchString(q), spacedKey(q), longKey(q):
		return ReasonIssueKey
	case credentialRE.MatchString(q):
		return ReasonCredential
	case uuidRE.MatchString(q):
		return ReasonUUID
	case jwtRE.MatchString(q), awsKeyRE.MatchString(q), mcpproxy.ScrubTokens(q) != q:
		return ReasonToken
	case isHost(q):
		return ReasonHostname
	case highEntropy(q):
		return ReasonHighEntropy
	}
	return ""
}

// spacedKey reports "RHAIENG 1234": an upper-case key of 4 or more
// characters and a number of 4 or more digits that is not a version. A
// short or public-looking one ("TLS 1.3", "HTTP 2", "RHEL 10", "ISO
// 27001") passes unless the planner saw it (seenKey).
func spacedKey(q string) bool {
	for _, m := range issueKeySpRE.FindAllStringSubmatch(q, -1) {
		if m[3] == "" && len(m[1]) >= 4 && len(m[2]) >= 4 {
			return true
		}
	}
	return false
}

// longKey reports a key in any case whose number has 4 or more digits
// (rhaieng-1234); a short one (gpt-4, sha-256) passes unless the planner
// saw it (privateKeys).
func longKey(q string) bool {
	for _, m := range anyKeyRE.FindAllStringSubmatch(q, -1) {
		if len(m[1]) >= 4 {
			return true
		}
	}
	return false
}

// privateKeys is every issue key in the private inputs, as KEY-NUMBER in
// upper case: dashed in any case, or spaced with an upper-case key ("OSD
// 12"; a lower-case word and a number, "november 2026", is not a key).
func privateKeys(p Private) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		s = normalize(s)
		for _, k := range anyKeyRE.FindAllString(s, -1) {
			out[strings.ToUpper(k)] = true
		}
		for _, m := range issueKeySpRE.FindAllStringSubmatch(s, -1) {
			out[m[1]+"-"+m[2]] = true
		}
	}
	for _, s := range append([]string{p.Question, p.PageText, p.Notes}, append(append(p.Prior, p.ToolInputs...), p.ToolOutputs...)...) {
		add(s)
	}
	return out
}

// seenKey reports a key in q, any case, dashed or spaced, that the
// planner saw.
func seenKey(q string, keys map[string]bool) bool {
	for _, k := range anyKeyRE.FindAllString(q, -1) {
		if keys[strings.ToUpper(k)] {
			return true
		}
	}
	for _, m := range anySpacedKeyRE.FindAllStringSubmatch(q, -1) {
		if keys[strings.ToUpper(m[1])+"-"+m[2]] {
			return true
		}
	}
	return false
}

// isHost reports any dotted name of two or more labels, whatever its last
// label: an internal suffix (.redhat, .nl, .corp) is as private as a known
// one, so no list of suffixes decides. A version number (v1.2.3) is not a
// name; a dotted product name (node.js) is refused with the hosts.
func isHost(q string) bool {
	for _, m := range dottedRE.FindAllString(q, -1) {
		if !versionRE.MatchString(m) && strings.IndexFunc(m, unicode.IsLetter) >= 0 {
			return true
		}
	}
	return false
}

// highEntropy reports a long run that mixes letters and digits with the
// spread of an encoded secret, not of a word or an identifier.
func highEntropy(q string) bool {
	for _, run := range runRE.FindAllString(q, -1) {
		if len(run) < minEntropyRun || !strings.ContainsAny(run, "0123456789") || strings.IndexFunc(run, unicode.IsLetter) < 0 {
			continue
		}
		if entropy(run) >= entropyBitsMin {
			return true
		}
	}
	return false
}

// entropy is the Shannon entropy of s in bits per character.
func entropy(s string) float64 {
	counts := map[rune]float64{}
	for _, r := range s {
		counts[r]++
	}
	n := float64(len([]rune(s)))
	h := 0.0
	for _, c := range counts {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

// words is s normalized and lowercased, split into letter and digit runs.
func words(s string) []string {
	return wordRE.FindAllString(strings.ToLower(norm.NFKC.String(s)), -1)
}

// shingles is every run of overlapWords consecutive words in the private
// inputs.
func shingles(p Private) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		w := words(s)
		for i := 0; i+overlapWords <= len(w); i++ {
			out[strings.Join(w[i:i+overlapWords], " ")] = true
		}
	}
	add(p.Question)
	add(p.PageText)
	add(p.Notes)
	for _, s := range p.Prior {
		add(s)
	}
	for _, s := range p.ToolInputs {
		add(s)
	}
	for _, s := range p.ToolOutputs {
		add(s)
	}
	return out
}

// overlaps reports whether q repeats a run of overlapWords private words.
func overlaps(q string, private map[string]bool) bool {
	w := words(q)
	for i := 0; i+overlapWords <= len(w); i++ {
		if private[strings.Join(w[i:i+overlapWords], " ")] {
			return true
		}
	}
	return false
}
