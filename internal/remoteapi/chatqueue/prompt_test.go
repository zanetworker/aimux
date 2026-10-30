package chatqueue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSamePromptFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "api", "testdata", "clients", "terminal", "saved-prompt-match.json")
	raw, err := os.ReadFile(path) //nolint:gosec // fixed fixture path in the repo
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name, Saved, Pending string
			Match                bool
		}
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(fx.Cases) == 0 {
		t.Fatalf("%s has no cases", path)
	}
	for _, c := range fx.Cases {
		if got := SamePrompt(c.Saved, c.Pending); got != c.Match {
			t.Errorf("%s: SamePrompt = %v, want %v", c.Name, got, c.Match)
		}
	}
}

func TestSamePromptFoldAndEllipsis(t *testing.T) {
	cases := []struct {
		saved, text string
		want        bool
	}{
		{"fix  the\n bug", "fix the bug", true},         // white space folded both sides
		{"fix the…", "fix the import in main.go", true}, // history cut short
		{"fix the import", "fix the", false},            // history longer than the text
		{"", "anything", false},                         // nothing saved never matches
		{"   ", "anything", false},                      // only white space
		{"…", "anything", false},                        // only the ellipsis
		{"Fix the bug", "fix the bug", false},           // case matters
		{"run tests", "run tests and lint", true},       // a prefix matches, as in the clients
		{"<pasted_content id=\"2\">\nx\n</pasted_content>", "x", true},
	}
	for _, c := range cases {
		if got := SamePrompt(c.saved, c.text); got != c.want {
			t.Errorf("SamePrompt(%q, %q) = %v, want %v", c.saved, c.text, got, c.want)
		}
	}
}

func TestAsTyped(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no tags", "plain words", "plain words"},
		{"command with args", "<command-name>/tidy</command-name>\n<command-message>tidy</command-message>\n<command-args> docs </command-args>", "/tidy docs"},
		{"unclosed tag stays", "see <command-name>/tidy", "see <command-name>/tidy"},
		{"output dropped, ends the run", "a<local-command-stdout>out</local-command-stdout>b", "a\nb"},
		{"args after output are text", "<command-name>/x</command-name><bash-stdout>o</bash-stdout><command-args>y</command-args>", "/x\ny"},
		{"caveat only", "<local-command-caveat>c</local-command-caveat>", ""},
		{"bash", "<bash-input> ls </bash-input>", "!ls"},
	}
	for _, c := range cases {
		if got := asTyped(c.in); got != c.want {
			t.Errorf("%s: asTyped = %q, want %q", c.name, got, c.want)
		}
	}
}
