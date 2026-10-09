package search

import (
	"strings"
	"unicode"
)

// makeSnippet returns about width words of text around the first word that
// matches one of the (lowercase) prefixes, with matches marked as [word].
// Building it here from text the query already read avoids a second FTS5
// pass just for highlighting, which cost ~25% of a search.
func makeSnippet(text string, prefixes []string, width int) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	first := -1
	marked := make([]string, len(words))
	for i, w := range words {
		marked[i] = w
		if matchesAny(w, prefixes) {
			marked[i] = markWord(w)
			if first < 0 {
				first = i
			}
		}
	}
	start := 0
	if first > 3 {
		start = first - 3 // a little leading context
	}
	end := min(start+width, len(words))
	out := strings.Join(marked[start:end], " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(words) {
		out += "…"
	}
	return out
}

// matchesAny reports whether any letter/digit run inside w starts with one
// of the prefixes ("agent-ops" matches "ops").
func matchesAny(w string, prefixes []string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(w), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		for _, p := range prefixes {
			if p != "" && strings.HasPrefix(part, p) {
				return true
			}
		}
	}
	return false
}

// markWord brackets the word, leaving surrounding punctuation outside.
func markWord(w string) string {
	isWordRune := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }
	r := []rune(w)
	lo, hi := 0, len(r)
	for lo < hi && !isWordRune(r[lo]) {
		lo++
	}
	for hi > lo && !isWordRune(r[hi-1]) {
		hi--
	}
	if lo == hi {
		return w
	}
	return string(r[:lo]) + "[" + string(r[lo:hi]) + "]" + string(r[hi:])
}
