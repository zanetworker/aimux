package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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
