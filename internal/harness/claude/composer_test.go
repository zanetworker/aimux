package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/harness"
)

func TestClaudeComposerRead(t *testing.T) {
	r := Composer{}
	idle := fixture(t, "idle-empty.txt")
	if got := r.Read(idle); !got.InputEmpty || got.BoxHasText || got.Working || got.Prompt != nil || got.PasteMarker != "" {
		t.Errorf("idle: %+v", got)
	}
	draft := r.Read(fixture(t, "draft-in-box.txt"))
	if draft.InputEmpty || !draft.BoxHasText || draft.BoxRows < 1 {
		t.Errorf("draft: %+v", draft)
	}
	if p := r.Read(fixture(t, "prompt-bash.txt")).Prompt; p == nil || p.Question != "Do you want to proceed?" {
		t.Errorf("prompt: %+v", p)
	}
	pasted := strings.Replace(idle, "❯\n", "❯ [Pasted text #3 +2 lines]\n", 1)
	if got := r.Read(pasted); got.PasteMarker != "[Pasted text #3 +2 lines]" || got.BoxRows != 1 {
		t.Errorf("paste marker: %+v", got)
	}
	notMarker := strings.Replace(idle, "❯\n", "❯ [Pasted text]\n", 1)
	if got := r.Read(notMarker).PasteMarker; got != "" {
		t.Errorf("not a marker read as %q", got)
	}
	if got := r.Read(""); got != (harness.Screen{}) {
		t.Errorf("empty screen: %+v", got)
	}
}

func TestClaudeComposerBoxShows(t *testing.T) {
	r := Composer{}
	scr := strings.Replace(fixture(t, "idle-empty.txt"), "❯\n", "❯ hello world\n", 1)
	if got := r.BoxShows(scr, "hello world"); got != (harness.Match{Words: true, Shown: true, Typed: true}) {
		t.Errorf("same text: %+v", got)
	}
	if got := r.BoxShows(scr, "helloworld"); got.Words || !got.Shown {
		t.Errorf("spaces removed: %+v", got)
	}
	if got := r.BoxShows(scr, "other"); got != (harness.Match{}) {
		t.Errorf("other text: %+v", got)
	}
}

func TestClaudeComposerIsAgentProcess(t *testing.T) {
	r := Composer{}
	for comm, want := range map[string]bool{
		"claude": true, "node": true, "/usr/local/bin/node": true, " claude\n": true,
		"zsh": false, "": false, "claude-code": false,
	} {
		if got := r.IsAgentProcess(comm); got != want {
			t.Errorf("IsAgentProcess(%q) = %v, want %v", comm, got, want)
		}
	}
}

func TestClaudeComposerClearKeys(t *testing.T) {
	r := Composer{}
	for rows, n := range map[int]int{0: 2, 1: 2, 3: 6} {
		want := make([]harness.Key, n)
		for i := range want {
			want[i] = harness.KeyClear
		}
		if got := r.ClearKeys(rows); !reflect.DeepEqual(got, want) {
			t.Errorf("ClearKeys(%d) = %v", rows, got)
		}
	}
}

// screens is every fixture plus synthesized boxes: a typed line, a paste
// marker, a wrapped draft.
func screens(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", "screens"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{"": ""}
	for _, e := range entries {
		out[e.Name()] = fixture(t, e.Name())
	}
	idle := fixture(t, "idle-empty.txt")
	out["typed"] = strings.Replace(idle, "❯\n", "❯ hello world\n", 1)
	out["marker"] = strings.Replace(idle, "❯\n", "❯ [Pasted text #3 +2 lines]\n", 1)
	out["wrapped"] = strings.Replace(idle, "❯\n", "❯ open internal/harness\n  /claude now\n", 1)
	return out
}

// TestComposerReadMatchesTheParsers pins that the one-pass Read answers
// exactly what each exported parser answers on its own.
func TestComposerReadMatchesTheParsers(t *testing.T) {
	for name, scr := range screens(t) {
		got := Composer{}.Read(scr)
		want := harness.Screen{Prompt: ParsePrompt(scr), Working: Working(scr), InputEmpty: InputBoxEmpty(scr), BoxHasText: BoxHasText(scr)}
		if want.Working {
			want.WorkingSeconds, _ = WorkingSeconds(scr)
		}
		rows, _ := boxRows(scr)
		want.BoxRows = len(rows)
		if box, n, ok := boxText(boxRows(scr)); ok && n == 1 && pastedMarker.MatchString(box) {
			want.PasteMarker = box
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Read = %+v, want %+v", name, got, want)
		}
	}
}

func TestComposerBoxShowsMatchesTheParsers(t *testing.T) {
	texts := []string{"", "hello world", "helloworld", "hello", "x\ny", strings.Repeat("a", 201), "open internal/harness/claude now"}
	for name, scr := range screens(t) {
		for _, text := range texts {
			got := Composer{}.BoxShows(scr, text)
			want := harness.Match{Words: WordsInBox(scr, text), Shown: TextShownInBox(scr, text), Typed: TextInBox(scr, text)}
			if got != want {
				t.Errorf("%s %q: BoxShows = %+v, want %+v", name, text, got, want)
			}
		}
	}
}

func TestComposerRefuseText(t *testing.T) {
	for _, c := range []struct {
		text  string
		allow bool
		want  bool
	}{
		{"hi", false, false},
		{"  !ls", true, true},
		{"!", false, true},
		{"x!", false, false},
		{"/clear", false, true},
		{" \t/clear", false, true},
		{"/clear", true, false},
		{"a/b", false, false},
		{"", false, false},
	} {
		if got := (Composer{}).RefuseText(c.text, c.allow); got != c.want {
			t.Errorf("RefuseText(%q, %v) = %v, want %v", c.text, c.allow, got, c.want)
		}
	}
}

func TestComposerRoomKeys(t *testing.T) {
	if got := (Composer{}).RoomKeys(); got != (harness.RoomKeys{Probe: harness.KeySpace, Unprobe: harness.KeyBackspace}) {
		t.Errorf("RoomKeys = %+v", got)
	}
}
