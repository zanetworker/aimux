package secretref

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// redacted replaces a secret value in text.
const redacted = "[redacted]"

// minWholeLen is the length from which a value is replaced anywhere it
// appears; shorter values are replaced only as a whole token.
const minWholeLen = 8

// Scrubber hides registered secret values, and their URL-escaped,
// JSON-escaped and decoded Basic forms, in text. It is defense in depth:
// callers still never put a secret or child output into an error.
type Scrubber struct {
	mu     sync.Mutex
	long   []string // replaced anywhere, longest first
	tokens []string // short values, replaced as whole tokens
	seen   map[string]bool
}

// NewScrubber returns an empty scrubber.
func NewScrubber() *Scrubber { return &Scrubber{seen: map[string]bool{}} }

// Add registers v and its encoded forms.
func (s *Scrubber) Add(v string) {
	if v == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	plain, raw := jsonInner(v, true), jsonInner(v, false)
	forms := []string{v, url.QueryEscape(v), url.PathEscape(v), plain, strings.ReplaceAll(plain, "/", `\/`), raw, strings.ReplaceAll(raw, "/", `\/`)}
	if b, ok := strings.CutPrefix(v, "Basic "); ok {
		if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b)); err == nil {
			cred := string(dec)
			forms = append(forms, cred, b)
			if _, pass, ok := strings.Cut(cred, ":"); ok {
				forms = append(forms, pass)
			}
		}
	}
	for _, f := range forms {
		if f == "" || s.seen[f] {
			continue
		}
		s.seen[f] = true
		if len(f) >= minWholeLen {
			s.long = append(s.long, f)
		} else {
			s.tokens = append(s.tokens, f)
		}
	}
	sort.Slice(s.long, func(i, j int) bool { return len(s.long[i]) > len(s.long[j]) })
}

// Scrub returns text with every registered form replaced.
func (s *Scrubber) Scrub(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	text = redactSpans(text, s.long)
	for _, tok := range s.tokens {
		text = replaceWholeToken(text, tok)
	}
	return text
}

// replaceWholeToken replaces each occurrence of tok that is not part of a
// longer word; a delimiter between two occurrences serves both.
func replaceWholeToken(text, tok string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		j := strings.Index(text[i:], tok)
		if j < 0 {
			b.WriteString(text[i:])
			break
		}
		start, end := i+j, i+j+len(tok)
		if (start == 0 || !wordByte(text[start-1])) && (end == len(text) || !wordByte(text[end])) {
			b.WriteString(text[i:start])
			b.WriteString(redacted)
		} else {
			b.WriteString(text[i:end])
		}
		i = end
	}
	return b.String()
}

func wordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// jsonInner is v as it appears inside a JSON string, with or without HTML
// escaping (json.Marshal escapes <, > and &; other encoders do not).
func jsonInner(v string, escapeHTML bool) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if len(out) < 2 {
		return ""
	}
	return out[1 : len(out)-1]
}

// redactSpans finds every occurrence of every value in the original text,
// merges overlapping spans, then replaces each merged span once, so one
// replacement never breaks the match of an overlapping value.
func redactSpans(text string, values []string) string {
	type span struct{ start, end int }
	var spans []span
	for _, v := range values {
		for i := 0; ; {
			j := strings.Index(text[i:], v)
			if j < 0 {
				break
			}
			spans = append(spans, span{i + j, i + j + len(v)})
			i += j + 1
		}
	}
	if len(spans) == 0 {
		return text
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	var b strings.Builder
	pos, cur := 0, spans[0]
	flush := func(sp span) {
		b.WriteString(text[pos:sp.start])
		b.WriteString(redacted)
		pos = sp.end
	}
	for _, sp := range spans[1:] {
		if sp.start <= cur.end {
			if sp.end > cur.end {
				cur.end = sp.end
			}
			continue
		}
		flush(cur)
		cur = sp
	}
	flush(cur)
	b.WriteString(text[pos:])
	return b.String()
}
