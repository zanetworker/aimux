package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/profile"
)

func testStore(t *testing.T) *profile.Store {
	t.Helper()
	return profile.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
}

// runProfileCmd builds an isolated root+profile command tree and executes it.
// This avoids global rootCmd state leaking between tests.
func runProfileCmd(t *testing.T, store *profile.Store, args []string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	root := &cobra.Command{Use: "aimux", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
	root.AddCommand(newProfileCmd(store))
	root.SetOut(&stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), err
}

func TestProfileSaveCmd(t *testing.T) {
	store := testStore(t)
	jsonOutput = true
	defer func() { jsonOutput = false }()

	out, err := runProfileCmd(t, store, []string{"profile", "save", "work", "--provider", "claude", "--model", "opus", "--json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got profile.Profile
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, out)
	}
	if got.Name != "work" {
		t.Errorf("Name=%q, want %q", got.Name, "work")
	}
	if got.Provider != "claude" {
		t.Errorf("Provider=%q, want %q", got.Provider, "claude")
	}
	if got.Model != "opus" {
		t.Errorf("Model=%q, want %q", got.Model, "opus")
	}
}

func TestProfileSaveCmd_Text(t *testing.T) {
	store := testStore(t)
	jsonOutput = false

	out, err := runProfileCmd(t, store, []string{"profile", "save", "dev", "--provider", "codex"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"dev" saved`) {
		t.Errorf("expected save confirmation, got: %s", out)
	}
}

// runProfileCmdErr is runProfileCmd that also returns stderr.
func runProfileCmdErr(t *testing.T, store *profile.Store, args []string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := &cobra.Command{Use: "aimux", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
	root.AddCommand(newProfileCmd(store))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

// profileDirEnv points the v2 profile dir at a fresh temp dir.
func profileDirEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AIMUX_PROFILE_DIR", dir)
	return dir
}

func writeProfileFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProfileList_ShowsBuiltinsWithDigest(t *testing.T) {
	profileDirEnv(t)
	jsonOutput = false

	out, _, err := runProfileCmdErr(t, testStore(t), []string{"profile", "list"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, name := range []string{"page", "work-readonly", "research", "research-with-work", "coding", "base"} {
		if !strings.Contains(out, name) {
			t.Errorf("list missing builtin %q:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "sha256:") {
		t.Errorf("list should show a short digest:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "research" && !strings.Contains(line, "false") {
			t.Errorf("research should be unavailable: %q", line)
		}
		if len(f) > 0 && f[0] == "page" && !strings.Contains(line, "true") {
			t.Errorf("page should be available: %q", line)
		}
	}
}

func TestProfileList_JSON(t *testing.T) {
	dir := profileDirEnv(t)
	writeProfileFile(t, dir, "mine.yaml", "name: mine\nextends: builtin:page\n")
	jsonOutput = true
	defer func() { jsonOutput = false }()

	out, _, err := runProfileCmdErr(t, testStore(t), []string{"profile", "list", "--json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Profiles []struct {
			Name      string `json:"name"`
			Runs      string `json:"runs"`
			Available bool   `json:"available"`
			Digest    string `json:"digest"`
		} `json:"profiles"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if result.Count != 7 || len(result.Profiles) != 7 {
		t.Fatalf("count=%d len=%d, want 7 (6 builtins + mine)", result.Count, len(result.Profiles))
	}
	found := false
	for _, p := range result.Profiles {
		if p.Name == "mine" {
			found = true
			if !p.Available || !strings.HasPrefix(p.Digest, "sha256:") {
				t.Errorf("mine entry wrong: %+v", p)
			}
		}
	}
	if !found {
		t.Error("user profile missing from list")
	}
}

func TestProfileList_BadFileReportedGoodStillListed(t *testing.T) {
	dir := profileDirEnv(t)
	writeProfileFile(t, dir, "bad.yaml", "name: bad\nbogus: 1\nextends: builtin:page\n")
	writeProfileFile(t, dir, "good.yaml", "name: good\nextends: builtin:page\n")
	jsonOutput = false

	out, errOut, err := runProfileCmdErr(t, testStore(t), []string{"profile", "list"})
	if err != nil {
		t.Fatalf("list must still succeed: %v", err)
	}
	if !strings.Contains(out, "good") {
		t.Errorf("good profile missing:\n%s", out)
	}
	if !strings.Contains(errOut, "bad.yaml") {
		t.Errorf("stderr should name bad.yaml, got: %q", errOut)
	}
}

func TestProfileGetCmd(t *testing.T) {
	store := testStore(t)
	_ = store.Save(profile.Profile{Name: "dev", Provider: "codex", Dir: "/tmp"})
	jsonOutput = true
	defer func() { jsonOutput = false }()

	out, err := runProfileCmd(t, store, []string{"profile", "get", "dev", "--json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got profile.Profile
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, out)
	}
	if got.Provider != "codex" {
		t.Errorf("Provider=%q, want %q", got.Provider, "codex")
	}
}

func TestProfileGetCmd_NotFound(t *testing.T) {
	store := testStore(t)

	_, err := runProfileCmd(t, store, []string{"profile", "get", "nope"})
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found, got: %s", err.Error())
	}
}

func TestProfileDeleteCmd(t *testing.T) {
	store := testStore(t)
	_ = store.Save(profile.Profile{Name: "old"})
	jsonOutput = true
	defer func() { jsonOutput = false }()

	out, err := runProfileCmd(t, store, []string{"profile", "delete", "old", "--json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, out)
	}
	if result["deleted"] != "old" {
		t.Errorf("deleted=%v, want %q", result["deleted"], "old")
	}
	if _, ok := store.Get("old"); ok {
		t.Error("profile should be deleted")
	}
}

func TestProfileDeleteCmd_NotFound(t *testing.T) {
	store := testStore(t)

	_, err := runProfileCmd(t, store, []string{"profile", "delete", "nope"})
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestProfileShow_PrintsResolvedAndDigest(t *testing.T) {
	dir := profileDirEnv(t)
	writeProfileFile(t, dir, "mine.yaml", "name: mine\nextends: builtin:page\ndir: /work\n")
	jsonOutput = false

	out, _, err := runProfileCmdErr(t, testStore(t), []string{"profile", "show", "mine"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want, _ := profile.Resolve("mine", map[string]profile.Manifest{"mine": {Name: "mine", Extends: "builtin:page", Dir: "/work"}})
	if !strings.Contains(out, "digest: "+want.Digest()) {
		t.Errorf("output should end with full digest %s:\n%s", want.Digest(), out)
	}
	if !strings.Contains(out, "name: mine") || !strings.Contains(out, "dir: /work") || !strings.Contains(out, "harness: claude-code") {
		t.Errorf("resolved manifest YAML missing fields:\n%s", out)
	}
}

func TestProfileShow_JSON(t *testing.T) {
	profileDirEnv(t)
	jsonOutput = true
	defer func() { jsonOutput = false }()

	out, _, err := runProfileCmdErr(t, testStore(t), []string{"profile", "show", "page", "--json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if got["name"] != "page" {
		t.Errorf("name=%v, want page", got["name"])
	}
	if d, _ := got["digest"].(string); !strings.HasPrefix(d, "sha256:") {
		t.Errorf("digest=%v", got["digest"])
	}
}

func TestProfileShow_Unknown(t *testing.T) {
	profileDirEnv(t)
	jsonOutput = false
	_, _, err := runProfileCmdErr(t, testStore(t), []string{"profile", "show", "nope"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want error naming nope, got %v", err)
	}
}

func TestProfileCheck_ExitsNonZeroOnWidening(t *testing.T) {
	dir := profileDirEnv(t)
	writeProfileFile(t, dir, "wide.yaml", "name: wide\nextends: builtin:page\ntools: [Bash]\n")
	jsonOutput = false

	_, _, err := runProfileCmdErr(t, testStore(t), []string{"profile", "check", "wide"})
	if err == nil {
		t.Fatal("check must fail for a profile that widens its parent")
	}
	if !errors.Is(err, profile.ErrWidens) {
		t.Errorf("error should wrap ErrWidens, got: %v", err)
	}
}

func TestProfileCheck_OKPrintsDigest(t *testing.T) {
	dir := profileDirEnv(t)
	writeProfileFile(t, dir, "ok.yaml", "name: ok\nextends: builtin:page\n")
	jsonOutput = false

	out, _, err := runProfileCmdErr(t, testStore(t), []string{"profile", "check", "ok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "ok") || !strings.Contains(out, "sha256:") {
		t.Errorf("expected ok line with digest, got %q", out)
	}
}

func TestProfileCheck_UnrelatedBadFileDoesNotChangeExit(t *testing.T) {
	dir := profileDirEnv(t)
	writeProfileFile(t, dir, "ok.yaml", "name: ok\nextends: builtin:page\n")
	writeProfileFile(t, dir, "bad.yaml", "name: bad\nbogus: 1\nextends: builtin:page\n")
	jsonOutput = false

	_, errOut, err := runProfileCmdErr(t, testStore(t), []string{"profile", "check", "ok"})
	if err != nil {
		t.Fatalf("unrelated bad file must not fail check: %v", err)
	}
	if !strings.Contains(errOut, "bad.yaml") {
		t.Errorf("bad file should still be printed to stderr, got %q", errOut)
	}
}

func TestProfileCheck_BadFileItselfFails(t *testing.T) {
	dir := profileDirEnv(t)
	writeProfileFile(t, dir, "bad.yaml", "name: bad\nbogus: 1\nextends: builtin:page\n")
	jsonOutput = false

	_, _, err := runProfileCmdErr(t, testStore(t), []string{"profile", "check", "bad"})
	if err == nil {
		t.Fatal("check of a profile whose file failed to load must fail")
	}
}

func TestProfileMigrate_WritesOnceNeverOverwrites(t *testing.T) {
	dir := profileDirEnv(t)
	store := testStore(t)
	_ = store.Save(profile.Profile{Name: "dev", Provider: "claude", Model: "opus", Dir: "/w", Mode: "plan"})
	_ = store.Save(profile.Profile{Name: "kept", Provider: "codex"})
	writeProfileFile(t, dir, "kept.yaml", "name: kept\nextends: builtin:page\n")
	jsonOutput = false

	out, _, err := runProfileCmdErr(t, store, []string{"profile", "migrate"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dev.yaml")) //nolint:gosec // G304: path is a test temp or fixed testdata path
	if err != nil {
		t.Fatalf("dev.yaml not written: %v", err)
	}
	for _, want := range []string{"name: dev", "extends: builtin:base", "provider: claude", "name: opus", "dir: /w"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("dev.yaml missing %q:\n%s", want, data)
		}
	}
	kept, _ := os.ReadFile(filepath.Join(dir, "kept.yaml")) //nolint:gosec // G304: path is a test temp or fixed testdata path
	if !strings.Contains(string(kept), "builtin:page") {
		t.Errorf("existing kept.yaml was overwritten:\n%s", kept)
	}
	if !strings.Contains(out, "kept") || !strings.Contains(out, "skip") {
		t.Errorf("expected a skip message for kept, got:\n%s", out)
	}

	// Second run: nothing new written, dev.yaml untouched.
	before, _ := os.Stat(filepath.Join(dir, "dev.yaml"))
	out2, _, err := runProfileCmdErr(t, store, []string{"profile", "migrate"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	after, _ := os.Stat(filepath.Join(dir, "dev.yaml"))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("second run rewrote dev.yaml")
	}
	if strings.Contains(out2, "wrote") {
		t.Errorf("second run should write nothing:\n%s", out2)
	}

	// The migrated profile must load and resolve.
	user, lerr := profile.LoadDir(dir)
	if lerr != nil {
		t.Fatalf("LoadDir: %v", lerr)
	}
	if _, err := profile.Resolve("dev", user); err != nil {
		t.Errorf("migrated profile does not resolve: %v", err)
	}
}

func TestProfileMigrate_ReportsBuiltinRenames(t *testing.T) {
	dir := profileDirEnv(t)
	store := testStore(t)
	_ = store.Save(profile.Profile{Name: "page", Provider: "claude"})
	jsonOutput = false

	out, _, err := runProfileCmdErr(t, store, []string{"profile", "migrate"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "renamed page to page-v1: page is a builtin profile") {
		t.Errorf("rename not reported:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "page-v1.yaml")); err != nil {
		t.Errorf("page-v1.yaml not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "page.yaml")); err == nil {
		t.Error("page.yaml must not be written (would shadow builtin)")
	}
}

func TestProfileMigrate_RenamedTargetExistsIsSkipped(t *testing.T) {
	dir := profileDirEnv(t)
	store := testStore(t)
	_ = store.Save(profile.Profile{Name: "page", Provider: "claude"})
	writeProfileFile(t, dir, "page-v1.yaml", "name: page-v1\nextends: builtin:page\n")
	jsonOutput = false

	out, _, err := runProfileCmdErr(t, store, []string{"profile", "migrate"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "page-v1.yaml")) //nolint:gosec // G304: path is a test temp or fixed testdata path
	if !strings.Contains(string(data), "builtin:page") {
		t.Errorf("existing page-v1.yaml overwritten:\n%s", data)
	}
	if !strings.Contains(out, "skip") {
		t.Errorf("expected skip message:\n%s", out)
	}
}

func TestProfileSave_PrintsMigrationNote(t *testing.T) {
	profileDirEnv(t)
	store := testStore(t)
	jsonOutput = false
	note := "note: v1 profiles are replaced by ~/.config/aimux/profiles; run aimux profile migrate"

	cases := []struct {
		name string
		args []string
	}{
		{"save", []string{"profile", "save", "dev", "--provider", "claude"}},
		{"get", []string{"profile", "get", "dev"}},
		{"delete", []string{"profile", "delete", "dev"}},
	}
	for _, c := range cases {
		stdout, stderr, err := runProfileCmdErr(t, store, c.args)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n := strings.Count(stderr, note); n != 1 {
			t.Errorf("%s: note printed %d times on stderr, want 1: %q", c.name, n, stderr)
		}
		if strings.Contains(stdout, "note:") {
			t.Errorf("%s: note must not pollute stdout: %q", c.name, stdout)
		}
	}
}

func TestProfileList_NoMigrationNote(t *testing.T) {
	profileDirEnv(t)
	jsonOutput = false
	_, stderr, err := runProfileCmdErr(t, testStore(t), []string{"profile", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "note:") {
		t.Errorf("list should not print the v1 note: %q", stderr)
	}
}

// A v1 mode has no v2 field: migrate says so per profile (on stderr, so
// --json output stays clean) instead of dropping it silently.
func TestProfileMigrate_WarnsWhenModeIsDropped(t *testing.T) {
	profileDirEnv(t)
	store := testStore(t)
	_ = store.Save(profile.Profile{Name: "dev", Provider: "claude", Mode: "plan"})
	_ = store.Save(profile.Profile{Name: "plain", Provider: "claude"})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	out, errOut, err := runProfileCmdErr(t, store, []string{"profile", "migrate"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `dev: v1 mode "plan" is not carried; profiles v2 have no mode field yet`; !strings.Contains(errOut, want) {
		t.Fatalf("stderr = %q, want %q", errOut, want)
	}
	if strings.Contains(errOut, "plain") || strings.Contains(out, "mode") {
		t.Fatalf("warning for a profile without a mode, or in stdout: stderr %q stdout %q", errOut, out)
	}
}
