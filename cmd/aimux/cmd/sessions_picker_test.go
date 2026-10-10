package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/sessions"
)

// pickerEnv builds a projects tree + index and points the picker helpers at it.
func pickerEnv(t *testing.T) sessions.PickerState {
	t.Helper()
	projects := t.TempDir()
	write := func(project, id string, lines ...string) {
		dir := filepath.Join(projects, project)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("-Users-me-research", "43ce13d4", `{"type":"user","entrypoint":"cli","cwd":"/Users/me/research","message":{"content":"add hypotheses to Ying's deck"}}`,
		`{"type":"custom-title","customTitle":"yingzhao-hypotheses"}`)
	write("-tmp", "99fe04f2", `{"type":"user","entrypoint":"sdk-cli","cwd":"/tmp/x","message":{"content":"YOU ARE A SESSION ANALYZER. deck"}}`)

	db := filepath.Join(t.TempDir(), "search.db")
	ix, err := search.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Update(projects, search.DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	_ = ix.Close()

	origDB := searchDBPath
	searchDBPath = func() string { return db }
	st := sessions.PickerState{Dir: t.TempDir()}
	t.Setenv(sessions.PickerStateEnv, st.Dir)
	t.Cleanup(func() { searchDBPath = origDB })
	return st
}

func runSub(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	c := newSessionsCmd(nil, nil, nil)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs(append([]string{"sessions"}, args...))
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("sessions %v: %v", args, err)
	}
	return out.String()
}

func TestPickerRows_BrowseHidesAutomatedUntilToggled(t *testing.T) {
	st := pickerEnv(t)
	if err := st.SetLive([]string{"43ce13d4"}); err != nil {
		t.Fatal(err)
	}

	out := runSub(t, "rows", "--", "")
	if strings.Contains(out, "99fe04f2") || !strings.Contains(out, "43ce13d4\t") {
		t.Errorf("browse rows should list only the human session:\n%s", out)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("want exactly one line per session:\n%s", out)
	}
	if !strings.Contains(out, "●") {
		t.Error("live session not marked")
	}

	if err := st.Toggle("automated"); err != nil {
		t.Fatal(err)
	}
	if out := runSub(t, "rows", "--", "deck"); !strings.Contains(out, "99fe04f2") {
		t.Errorf("after ^a, query rows should include the automated session:\n%s", out)
	}
}

func TestPickerRows_QueryRanks(t *testing.T) {
	pickerEnv(t)
	out := runSub(t, "rows", "--", "Ying deck")
	if !strings.HasPrefix(out, "43ce13d4\t") {
		t.Errorf("query rows: %q", out)
	}
	if out := runSub(t, "rows", "--", "zzzz"); strings.TrimSpace(out) != "" {
		t.Errorf("no match should print nothing, got %q", out)
	}
}

func TestPickerPreview(t *testing.T) {
	pickerEnv(t)
	out := runSub(t, "preview", "43ce13d4", "--", "deck")
	for _, want := range []string{"yingzhao-hypotheses", "/Users/me/research", "MATCH", "STARTED WITH"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview missing %q:\n%s", want, out)
		}
	}
	if out := runSub(t, "preview", "nope", "--", ""); !strings.Contains(out, "not in the index") {
		t.Errorf("unknown id preview: %q", out)
	}
}

func TestPickerToggle_PrintsNewHeader(t *testing.T) {
	pickerEnv(t)
	if out := runSub(t, "picker-toggle", "semantic"); !strings.Contains(out, "semantic ·") {
		t.Errorf("header after toggle: %q", out)
	}
}

func TestRunPickerHelper_FastPath(t *testing.T) {
	st := pickerEnv(t)
	var out bytes.Buffer
	handled, err := RunPickerHelper([]string{"sessions", "picker-toggle", "automated"}, &out)
	if !handled || err != nil || !strings.Contains(out.String(), "automated shown") || !st.IncludeAutomated() {
		t.Errorf("handled=%v err=%v out=%q", handled, err, out.String())
	}
	out.Reset()
	if handled, _ := RunPickerHelper([]string{"sessions", "rows", "--", "Ying"}, &out); !handled || !strings.HasPrefix(out.String(), "43ce13d4\t") {
		t.Errorf("rows fast path: handled=%v out=%q", handled, out.String())
	}
	for _, args := range [][]string{{"agents"}, {"sessions"}, {"sessions", "index"}, {}} {
		if handled, _ := RunPickerHelper(args, &out); handled {
			t.Errorf("%v must take the normal path", args)
		}
	}
}

func TestQuietCancel(t *testing.T) {
	if err := quietCancel(sessions.ErrCancelled); err != nil {
		t.Errorf("leaving the picker should not be an error, got %v", err)
	}
	boom := errors.New("fzf missing")
	if err := quietCancel(boom); err != boom {
		t.Errorf("real errors must pass through, got %v", err)
	}
	if quietCancel(nil) != nil {
		t.Error("nil stays nil")
	}
}

func TestPickerRows_RespectScope(t *testing.T) {
	st := pickerEnv(t)
	if err := st.SetScope("/Users/me/elsewhere"); err != nil {
		t.Fatal(err)
	}
	if out := runSub(t, "rows", "--", ""); strings.Contains(out, "43ce13d4") {
		t.Errorf("browse ignored the --dir scope:\n%s", out)
	}
	if out := runSub(t, "rows", "--", "deck"); strings.Contains(out, "43ce13d4") {
		t.Errorf("query ignored the --dir scope:\n%s", out)
	}
	if err := st.SetScope("/Users/me/research"); err != nil {
		t.Fatal(err)
	}
	if out := runSub(t, "rows", "--", "deck"); !strings.Contains(out, "43ce13d4") {
		t.Errorf("in-scope session missing:\n%s", out)
	}
}

func TestPickerStateFromFlags(t *testing.T) {
	st := sessions.PickerState{Dir: t.TempDir()}
	if err := applyPickerFlags(st, "keyword", true, true, "/Users/me/research", true, false); err != nil {
		t.Fatal(err)
	}
	if st.Mode() != "keyword" || !st.IncludeAutomated() || st.Scope() != "/Users/me/research" {
		t.Errorf("flags not applied: mode=%s automated=%v scope=%q", st.Mode(), st.IncludeAutomated(), st.Scope())
	}
	// an explicit --mode semantic stays semantic-only in the picker
	st2 := sessions.PickerState{Dir: t.TempDir()}
	if err := applyPickerFlags(st2, "semantic", true, false, "", true, false); err != nil || st2.Mode() != "semantic" {
		t.Errorf("semantic flag: mode=%s err=%v", st2.Mode(), err)
	}
	// unset --mode: hybrid with embeddings, keyword without
	st3 := sessions.PickerState{Dir: t.TempDir()}
	if err := applyPickerFlags(st3, "hybrid", false, false, "", false, false); err != nil || st3.Mode() != "keyword" {
		t.Errorf("default without embedder: mode=%s err=%v", st3.Mode(), err)
	}
}

func TestPickerRows_SemanticModeFallsBackToKeywordWithoutKey(t *testing.T) {
	st := pickerEnv(t)
	t.Setenv("OPENAI_API_KEY", "")
	if err := st.SetMode("semantic"); err != nil {
		t.Fatal(err)
	}
	if out := runSub(t, "rows", "--", "Ying deck"); !strings.HasPrefix(out, "43ce13d4\t") {
		t.Errorf("semantic rows without a key should still answer (keyword): %q", out)
	}
}

func TestPickerRows_LiveOnly(t *testing.T) {
	st := pickerEnv(t)
	if err := st.Toggle("live"); err != nil {
		t.Fatal(err)
	}
	// nothing is live yet (discovery still running): show nothing rather than everything
	if out := runSub(t, "rows", "--", ""); strings.TrimSpace(out) != "" {
		t.Errorf("live-only with no live sessions should be empty:\n%s", out)
	}
	if err := st.SetLive([]string{"43ce13d4"}); err != nil {
		t.Fatal(err)
	}
	if out := runSub(t, "rows", "--", ""); !strings.HasPrefix(out, "43ce13d4\t") {
		t.Errorf("live session missing from live-only browse:\n%s", out)
	}
	if out := runSub(t, "rows", "--", "deck"); !strings.HasPrefix(out, "43ce13d4\t") {
		t.Errorf("live session missing from live-only search:\n%s", out)
	}
	if err := st.SetLive([]string{"someone-else"}); err != nil {
		t.Fatal(err)
	}
	if out := runSub(t, "rows", "--", "deck"); strings.Contains(out, "43ce13d4") {
		t.Errorf("non-live session shown in live-only search:\n%s", out)
	}
}

func TestPickerFlags_Live(t *testing.T) {
	st := sessions.PickerState{Dir: t.TempDir()}
	if err := applyPickerFlags(st, "hybrid", false, false, "", false, true); err != nil {
		t.Fatal(err)
	}
	if !st.LiveOnly() {
		t.Error("--live should start the picker in live-only mode")
	}
}

func TestCopyResume_CopiesCommandWithFullPath(t *testing.T) {
	pickerEnv(t)
	var copied string
	orig := copyToClipboard
	copyToClipboard = func(s string) error { copied = s; return nil }
	defer func() { copyToClipboard = orig }()

	out := runSub(t, "copy-resume", "43ce13d4")
	want := "cd '/Users/me/research' && claude --resume '43ce13d4'"
	if copied != want {
		t.Errorf("copied %q, want %q", copied, want)
	}
	if !strings.Contains(out, "copied") {
		t.Errorf("header should confirm the copy, got %q", out)
	}

	copied = ""
	out = runSub(t, "copy-resume", "nope")
	if copied != "" || !strings.Contains(out, "not in the index") {
		t.Errorf("unknown session: copied=%q out=%q", copied, out)
	}
}

func TestRunPickerHelper_CopyResumeIsFast(t *testing.T) {
	pickerEnv(t)
	orig := copyToClipboard
	copyToClipboard = func(string) error { return nil }
	defer func() { copyToClipboard = orig }()
	var out bytes.Buffer
	if handled, err := RunPickerHelper([]string{"sessions", "copy-resume", "43ce13d4"}, &out); !handled || err != nil {
		t.Errorf("copy-resume should use the fast path: handled=%v err=%v", handled, err)
	}
}

func TestPickerRows_SizedToTerminal(t *testing.T) {
	pickerEnv(t)
	width := func(cols string) int {
		t.Setenv("FZF_COLUMNS", cols)
		out := runSub(t, "rows", "--", "")
		line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
		plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(line, "")
		return len([]rune(strings.TrimRight(plain[strings.Index(plain, "\t")+1:], " ")))
	}
	narrow, wide := width("150"), width("240")
	if narrow > sessions.ListPaneWidth(150)-3 {
		t.Errorf("row %d wide overflows the list pane at 150 cols", narrow)
	}
	if wide <= narrow {
		t.Errorf("rows should widen with the terminal: 150→%d, 240→%d", narrow, wide)
	}
	if width("garbage") == 0 {
		t.Error("a bad FZF_COLUMNS should fall back to the default layout")
	}
}

func TestCopyResume_NoteOnItsOwnLine(t *testing.T) {
	pickerEnv(t)
	orig := copyToClipboard
	copyToClipboard = func(string) error { return nil }
	defer func() { copyToClipboard = orig }()
	lines := strings.Split(strings.TrimSpace(runSub(t, "copy-resume", "43ce13d4")), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "✓ copied: ") {
		t.Errorf("copy note should be its own header line, got %q", lines)
	}
}
