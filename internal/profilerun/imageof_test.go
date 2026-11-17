package profilerun

import (
	"strings"
	"testing"
)

func TestImageOf(t *testing.T) {
	first := &AskRequest{URL: "https://e.example/a", Title: "A", Page: "page text"}
	cases := []struct {
		name string
		user string
		want bool
	}{
		{"page prompt with image", Prompt(AskRequest{Question: "what is this?", Image: true}), true},
		{"page prompt with page text and image", Prompt(AskRequest{Page: "p", URL: "u", Question: "q", Image: true, Prior: []string{"a"}}), true},
		{"page prompt without image", Prompt(AskRequest{Page: "p", Question: "q"}), false},
		{"sandbox prompt with image", sandboxTurnPrompt(first, []string{"a"}, true, "q"), true},
		{"sandbox prompt without image", sandboxTurnPrompt(first, nil, false, "q"), false},
		{"framing in the question", Prompt(AskRequest{Question: imageFraming + "q"}), false},
		{"framing in the page text", Prompt(AskRequest{Page: imageFraming, Question: "q"}), false},
		{"framing in the recap", Prompt(AskRequest{Recap: imageFraming, Question: "q"}), false},
		{"framing in a sandbox page", sandboxTurnPrompt(&AskRequest{Page: imageFraming}, nil, false, "q"), false},
		{"plain text with framing", imageFraming + "q", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := ImageOf(c.user); got != c.want {
			t.Errorf("%s: ImageOf = %v, want %v", c.name, got, c.want)
		}
		// As session history stores it: trimmed lines, blank lines dropped.
		var kept []string
		for _, l := range strings.Split(c.user, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				kept = append(kept, l)
			}
		}
		if got := ImageOf(strings.Join(kept, "\n")); got != c.want {
			t.Errorf("%s, as stored: ImageOf = %v, want %v", c.name, got, c.want)
		}
	}
}
