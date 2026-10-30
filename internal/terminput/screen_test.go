package terminput

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "screens", name)) //nolint:gosec // fixed test fixture names
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func TestScreenFixtures(t *testing.T) {
	yesNo := []Option{{"1", "Yes"}, {"2", "Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)"}, {"3", "No"}}
	tests := []struct {
		file      string
		empty     bool
		question  string
		options   []Option
		detail    string
		hasPrompt bool
		working   bool
	}{
		{file: "idle-empty.txt", empty: true},
		{file: "idle-placeholder.txt", empty: true},
		{file: "busy-empty.txt", empty: true, working: true},
		{file: "busy-queued-hint.txt", empty: true, working: true},
		{file: "working-todos.txt", empty: true, working: true},
		{file: "idle-after-turn.txt", empty: true},
		{file: "idle-prose-bullet.txt", empty: true},
		{file: "idle-reply-quotes-hint.txt", empty: true},
		{file: "draft-in-box.txt"},
		{file: "prompt-create-file.txt", hasPrompt: true, question: "Do you want to create made.txt?", options: yesNo,
			detail: "Create file\nmade.txt\n" + strings.Repeat("╌", 80) + "\n 1 hi\n" + strings.Repeat("╌", 80)},
		{file: "prompt-bash.txt", hasPrompt: true, question: "Do you want to proceed?", detail: "Bash command\n\n  ls -la\n  List files in the current directory", options: []Option{
			{"1", "Yes"},
			{"2", "Yes, and don't ask again for ls commands in /home/demo/project"},
			{"3", "No, and tell Claude what to do differently (esc)"},
		}},
		{file: "prompt-wrapped-label.txt", hasPrompt: true, question: "Do you want to make this edit to app.go?",
			detail: "Edit file\napp.go\n" + strings.Repeat("╌", 80) + "\n 3 -old line\n 3 +new line\n" + strings.Repeat("╌", 80), options: []Option{
				{"1", "Yes"},
				{"2", "Yes, allow all edits during this session and also auto-approve common shell commands that only read files in this project (shift+tab)"},
				{"3", "No, and tell Claude what to do differently (esc)"},
			}},
		{file: "prompt-half-scrolled.txt"},
		{file: "shell-prompt.txt"},
		{file: "plan-approval.txt"},
		{file: "stale-box-wrapped-draft.txt"},
		{file: "stale-box-cut-bottom.txt"},
		{file: "stale-box-over-prompt.txt", hasPrompt: true, question: "Do you want to proceed?", detail: "Bash command\n\n  ls -la", options: []Option{{"1", "Yes"}, {"2", "No"}}},
		{file: "stale-box-bash-mode.txt"},
		{file: "answered-prompt-in-scrollback.txt", empty: true},
		{file: "prose-then-prompt.txt"},
		{file: "draft-with-indented-rule.txt"},
		{file: "answered-prompt-tool-output.txt"},
		{file: "answered-prompt-spinner.txt", working: true},
		{file: "prompt-wrapped-numbered-continuation.txt", hasPrompt: true, question: "Do you want to proceed?", options: []Option{
			{"1", "Yes"},
			{"2", "Yes, and allow the following steps for this session: 3. the build and the lint run"},
			{"3", "No"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			screen := fixture(t, tt.file)
			if got := InputBoxEmpty(screen); got != tt.empty {
				t.Errorf("InputBoxEmpty = %v, want %v", got, tt.empty)
			}
			if got := Working(screen); got != tt.working {
				t.Errorf("Working = %v, want %v", got, tt.working)
			}
			p := ParsePrompt(screen)
			if !tt.hasPrompt {
				if p != nil {
					t.Fatalf("ParsePrompt = %+v, want nil", p)
				}
				return
			}
			if p == nil {
				t.Fatal("ParsePrompt = nil, want a prompt")
			}
			if p.Question != tt.question {
				t.Errorf("Question = %q, want %q", p.Question, tt.question)
			}
			if p.Detail != tt.detail {
				t.Errorf("Detail = %q, want %q", p.Detail, tt.detail)
			}
			if !reflect.DeepEqual(p.Options, tt.options) {
				t.Errorf("Options = %+v, want %+v", p.Options, tt.options)
			}
			if len(p.ID) != 16 {
				t.Errorf("ID = %q, want 16 hex chars", p.ID)
			}
		})
	}
}

func TestPromptIDStability(t *testing.T) {
	a := ParsePrompt(fixture(t, "prompt-create-file.txt"))
	b := ParsePrompt(fixture(t, "prompt-create-file.txt"))
	if a == nil || b == nil || a.ID != b.ID {
		t.Fatalf("same screen gave different ids: %+v %+v", a, b)
	}
	changed := strings.Replace(fixture(t, "prompt-create-file.txt"), "3. No", "3. Nope", 1)
	c := ParsePrompt(changed)
	if c == nil || c.ID == a.ID {
		t.Errorf("changed label kept id: %+v", c)
	}
	other := ParsePrompt(fixture(t, "prompt-bash.txt"))
	if other == nil || other.ID == a.ID {
		t.Errorf("different prompt shares id: %+v", other)
	}
}

func TestInputBoxEmptyEdges(t *testing.T) {
	rule := strings.Repeat("─", 40)
	box := func(line string) string { return rule + "\n" + line + "\n" + rule + "\n" }
	tests := []struct {
		name, screen string
		want         bool
	}{
		{"bare chevron", box("❯"), true},
		{"chevron and spaces", box("❯      "), true},
		{"unclosed Try quote is a draft", box(`❯ Try "how`), false},
		{"text starting with Try but not placeholder", box(`❯ Try it now`), false},
		{"empty screen", "", false},
		{"chevron without rules", "❯ \n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InputBoxEmpty(tt.screen); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsePromptNeedsEscLine(t *testing.T) {
	screen := " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n"
	if p := ParsePrompt(screen); p != nil {
		t.Errorf("prompt without footer = %+v, want nil", p)
	}
}

func TestParsePromptRejectsMoreThanNineOptions(t *testing.T) {
	var b strings.Builder
	b.WriteString(" Do you want to proceed?\n")
	for i := 1; i <= 10; i++ {
		b.WriteString(" " + strconv.Itoa(i) + ". Option\n")
	}
	b.WriteString(" Esc to cancel\n")
	if p := ParsePrompt(b.String()); p != nil {
		t.Errorf("prompt with 10 options = %+v, want nil", p)
	}
}

func TestParsePromptWrappedSelectedOption(t *testing.T) {
	// "❯" is 3 bytes but one column: indentation is compared in columns.
	for _, indent := range []string{"    ", "      "} {
		screen := " Do you want to proceed?\n ❯ 1. Yes, and a label long enough\n" + indent + "to wrap\n   2. No\n\n Esc to cancel\n"
		p := ParsePrompt(screen)
		if p == nil || len(p.Options) != 2 || p.Options[0].Label != "Yes, and a label long enough to wrap" {
			t.Errorf("indent %d: got %+v", len(indent), p)
		}
	}
}

func TestTextInBox(t *testing.T) {
	rule := strings.Repeat("─", 40)
	box := func(rows ...string) string {
		return "⏺ Done.\n\n" + rule + "\n" + strings.Join(rows, "\n") + "\n" + rule + "\n  [Model] │ demo\n"
	}
	long := strings.Repeat("word ", 20)
	tests := []struct {
		name, screen, text string
		want               bool
	}{
		{"typed text", box("❯ hello there"), "hello there", true},
		{"wrapped by Claude", box("❯ word word word word word word word word word word word word word", "  word word word word word word word"), long, true},
		{"paste marker", box("❯ [Pasted text #1 +3 lines]"), "a\nb\nc\nd", true},
		{"tab shown as spaces", box("❯ a    b"), "a\tb", true},
		{"box still empty", box("❯"), "hello", false},
		{"placeholder only", box(`❯ Try "how does <filepath> work?"`), "hello", false},
		{"other text", box("❯ hello world"), "hello", false},
		{"prompt below", box("❯ hello") + " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n", "hello", false},
		{"prompt instead of box", fixture(t, "prompt-bash.txt"), "hello", false},
		{"bash mode", box("! hello"), "hello", false},
		{"second chevron row", box("❯ hello", "❯ hello"), "hello hello", false},
		{"unindented continuation", box("❯ hello", "there"), "hello there", false},
		{"empty text", box("❯"), "", false},
		{"no box", "hello\n", "hello", false},
		{"paste marker for short text", box("❯ [Pasted text #1 +3 lines]"), "short", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TextInBox(tt.screen, tt.text); got != tt.want {
				t.Errorf("TextInBox = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPasteText(t *testing.T) {
	for text, want := range map[string]bool{
		"short":                  false,
		"two\nlines":             true,
		"a\tb":                   true,
		strings.Repeat("a", 200): false,
		strings.Repeat("a", 201): true,
		strings.Repeat("س", 200): false,
	} {
		if got := PasteText(text); got != want {
			t.Errorf("PasteText(%d chars) = %v, want %v", len([]rune(text)), got, want)
		}
	}
}

func TestPromptIDCoversDetail(t *testing.T) {
	bash := fixture(t, "prompt-bash.txt")
	a := ParsePrompt(bash)
	b := ParsePrompt(strings.Replace(bash, "   ls -la\n", "   rm -rf build\n", 1))
	if a == nil || b == nil {
		t.Fatal("no prompt")
	}
	if a.Question != b.Question || !reflect.DeepEqual(a.Options, b.Options) {
		t.Fatal("fixtures should differ only in the command")
	}
	if a.ID == b.ID {
		t.Fatalf("same question and labels, different commands, same id %s", a.ID)
	}
	if !strings.Contains(b.Detail, "rm -rf build") {
		t.Fatalf("detail %q", b.Detail)
	}
}

func TestPromptDetailLimits(t *testing.T) {
	rule := strings.Repeat("─", 40)
	tail := " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n"
	var body strings.Builder
	for i := 1; i <= 30; i++ {
		body.WriteString(" line " + strconv.Itoa(i) + "\n")
	}
	p := ParsePrompt(rule + "\n" + body.String() + tail)
	if p == nil {
		t.Fatal("no prompt")
	}
	if lines := strings.Split(p.Detail, "\n"); len(lines) != 20 || lines[19] != "line 20" {
		t.Fatalf("detail has %d lines, last %q, want the first 20", len(lines), lines[len(lines)-1])
	}
	cut := ParsePrompt(strings.Replace(rule+"\n"+body.String()+tail, " line 30\n", " line 31\n", 1))
	if cut.ID == p.ID {
		t.Fatal("a change past line 20 kept the id: the hash must cover the whole body")
	}
	long := ParsePrompt(rule + "\n " + strings.Repeat("ab", 1500) + "\n" + tail)
	if n := utf8.RuneCountInString(long.Detail); n != 2000 {
		t.Fatalf("detail %d chars, want 2000", n)
	}
	wide := ParsePrompt(rule + "\n " + strings.Repeat("س", 2500) + "\n" + tail)
	if n := utf8.RuneCountInString(wide.Detail); n != 2000 || !utf8.ValidString(wide.Detail) {
		t.Fatalf("detail %d chars, valid %v", n, utf8.ValidString(wide.Detail))
	}
	if none := ParsePrompt(tail); none == nil || none.Detail != "" {
		t.Fatalf("no rule above: %+v, want an empty detail", none)
	}
}

func TestWorkingEdges(t *testing.T) {
	rule := strings.Repeat("─", 40)
	box := "\n" + rule + "\n❯\n" + rule + "\n"
	tests := []struct {
		name   string
		screen string
		want   bool
	}{
		{"empty screen", "", false},
		{"each spinner glyph", "· Thinking…" + box, true},
		{"ascii ellipsis", "✢ Thinking... (3s)" + box, true},
		{"star glyph", "* Brewing… (1s · ↑ 3 tokens)" + box, true},
		{"esc to interrupt without a spinner", "  Compacting (esc to interrupt)" + box, true},
		{"spinner above the last message is old", "✻ Thinking…\n⏺ Done." + box, false},
		{"spinner too far above the box", "✻ Thinking…\n" + strings.Repeat("  ⎿ line\n", 20) + box, false},
		{"no box: spinner at the bottom", "⏺ Reading\n✶ Ionizing… (2s)\n", true},
		{"no ellipsis is a finished turn", "✻ Cooked for 1m 2s" + box, false},
		{"hint after a middle dot", "  Compacting · esc to interrupt" + box, true},
		{"hint quoted in prose is not the hint", "  then type esc to interrupt it" + box, false},
		{"hint in the footer below the box", "⏺ Done." + box + "  esc to interrupt\n", true},
	}
	for _, tt := range tests {
		if got := Working(tt.screen); got != tt.want {
			t.Errorf("%s: Working = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestWordsInBox(t *testing.T) {
	rule := strings.Repeat("─", 40)
	wrapped := "⏺ Done.\n\n" + rule + "\n❯ give me an example\n  and use it\n" + rule + "\n  [Model] │ demo\n"
	for _, c := range []struct {
		name, text string
		want       bool
	}{
		{"wrapped at a space", "give me an example and use it", true},
		{"extra spaces typed", "give me  an example\nand use it", true},
		{"same letters, other words", "giveme an example anduse it", false},
		{"other text", "give me an example", false},
		{"no text", "   ", false},
	} {
		if got := WordsInBox(wrapped, c.text); got != c.want {
			t.Errorf("%s: WordsInBox = %v, want %v", c.name, got, c.want)
		}
	}
	// A long path wrapped mid-word still matches; split differently it does not.
	path := "⏺ Done.\n\n" + rule + "\n❯ open internal/terminput/scre\n  en.go now\n" + rule + "\n  [Model] │ demo\n"
	for _, c := range []struct {
		name, text string
		want       bool
	}{
		{"wrapped inside a word", "open internal/terminput/screen.go now", true},
		{"a space where the word broke", "open internal/terminput/scre en.go now", true},
		{"spaces dropped elsewhere", "openinternal/terminput/screen.go now", false},
	} {
		if got := WordsInBox(path, c.text); got != c.want {
			t.Errorf("%s: WordsInBox = %v, want %v", c.name, got, c.want)
		}
	}
}
