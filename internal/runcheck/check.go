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
)

// Private is everything the planner saw that must not reach the web: the
// question, the page text, its notes, and its tool calls' inputs and outputs.
type Private struct {
	Question, PageText, Notes string
	ToolInputs, ToolOutputs   []string
}

// Rejection is one refused query: its position in the planner's list and why.
type Rejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

func (r Rejection) String() string { return fmt.Sprintf("query %d: %s", r.Index+1, r.Reason) }

var (
	urlRE        = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://|\bwww\.`)
	emailRE      = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[^\s@]+`)
	ipv4RE       = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)
	ipv6RE       = regexp.MustCompile(`(?i)\b[0-9a-f]{0,4}(:[0-9a-f]{0,4}){2,7}\b`)
	hostnameRE   = regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+([a-z]{2,24})\b`)
	issueKeyRE   = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)
	credentialRE = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|auth|client[_-]?secret)\s*[=:]\s*\S+`)
	uuidRE       = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	jwtRE        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	runRE        = regexp.MustCompile(`[A-Za-z0-9+/=_-]{24,}`)
	wordRE       = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

// hostTLDs are the top-level labels that make a dotted word a host name;
// internal suffixes are in it too. A dotted word with another last label
// (node.js, v1.2) is not taken for a host.
var hostTLDs = map[string]bool{
	"com": true, "net": true, "org": true, "io": true, "dev": true, "ai": true, "app": true, "cloud": true,
	"co": true, "edu": true, "gov": true, "info": true, "biz": true, "me": true, "us": true, "uk": true,
	"de": true, "fr": true, "eu": true, "ca": true, "au": true, "in": true, "cn": true, "jp": true,
	"sh": true, "xyz": true, "tech": true, "site": true, "online": true, "page": true,
	"internal": true, "corp": true, "local": true, "lan": true, "intranet": true, "home": true,
}

// Check returns the queries that may be sent, normalized, and why each
// other one may not. Rules apply in order: normalization; control
// characters; empty and length; patterns on the text and on it
// percent-decoded once; overlap with the private inputs; then the count and
// byte budget, so a rejected query never uses up the budget.
func Check(queries []string, p Private) (accepted []string, rejected []Rejection) {
	private := shingles(p)
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
			if dec, err := url.QueryUnescape(q); err == nil && dec != q {
				reason = patternReason(normalize(dec))
			}
			if reason == "" {
				reason = patternReason(q)
			}
			if reason == "" && overlaps(q, private) {
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

// normalize is NFKC with whitespace collapsed and trimmed.
func normalize(s string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(s)), " ")
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
	case isHost(q):
		return ReasonHostname
	case issueKeyRE.MatchString(q):
		return ReasonIssueKey
	case credentialRE.MatchString(q):
		return ReasonCredential
	case uuidRE.MatchString(q):
		return ReasonUUID
	case jwtRE.MatchString(q), mcpproxy.ScrubTokens(q) != q:
		return ReasonToken
	case highEntropy(q):
		return ReasonHighEntropy
	}
	return ""
}

func isHost(q string) bool {
	for _, m := range hostnameRE.FindAllStringSubmatch(q, -1) {
		if hostTLDs[strings.ToLower(m[1])] {
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
