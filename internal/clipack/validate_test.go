package clipack

import (
	"reflect"
	"strings"
	"testing"
)

func gh(t *testing.T) Pack {
	t.Helper()
	p, err := Builtin("gh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArgv_Happy(t *testing.T) {
	got, err := gh(t).Argv("issue_list", map[string]any{"repo": "cli/cli", "state": "open", "limit": float64(5)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/gh", "issue", "list", "--limit", "5", "--repo", "cli/cli", "--state", "open", "--json", "number,title,state,url"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv\n got %q\nwant %q", got, want)
	}
}

func TestArgv_PositionalAfterDoubleDash(t *testing.T) {
	got, err := gh(t).Argv("issue_view", map[string]any{"repo": "cli/cli", "number": 1})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got); n < 2 || got[n-2] != "--" || got[n-1] != "1" {
		t.Errorf("argv %q: want positional 1 after --", got)
	}
	got, err = gh(t).Argv("repo_view", map[string]any{"repo": "cli/cli"})
	if err != nil {
		t.Fatal(err)
	}
	if got[len(got)-1] != "cli/cli" || got[len(got)-2] != "--" {
		t.Errorf("argv %q", got)
	}
}

func TestArgv_BoolFlag(t *testing.T) {
	on, err := gh(t).Argv("release_list", map[string]any{"repo": "cli/cli", "exclude-drafts": true})
	if err != nil {
		t.Fatal(err)
	}
	off, err := gh(t).Argv("release_list", map[string]any{"repo": "cli/cli", "exclude-drafts": false})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(on, "--exclude-drafts") || contains(off, "--exclude-drafts") || contains(on, "true") {
		t.Errorf("on %q off %q", on, off)
	}
	if _, err := gh(t).Argv("release_list", map[string]any{"repo": "cli/cli", "exclude-drafts": "true"}); err == nil {
		t.Error("string for bool accepted")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func refused(t *testing.T, cmd string, in map[string]any, wantErr string) {
	t.Helper()
	got, err := gh(t).Argv(cmd, in)
	if err == nil {
		t.Errorf("%s %v accepted: %q", cmd, in, got)
		return
	}
	if wantErr != "" && !strings.Contains(err.Error(), wantErr) {
		t.Errorf("%s %v: err %q, want it to contain %q", cmd, in, err, wantErr)
	}
}

func TestValidate_UnknownFlag(t *testing.T) {
	refused(t, "issue_list", map[string]any{"repo": "cli/cli", "web": true}, `unknown flag "web"`)
	refused(t, "issue_list", map[string]any{"repo": "cli/cli", "jq": ".[]"}, `unknown flag "jq"`)
}

func TestValidate_ValueLooksLikeFlag(t *testing.T) {
	for _, v := range []string{"--help", "-x"} {
		refused(t, "issue_list", map[string]any{"repo": v}, "")
		refused(t, "repo_view", map[string]any{"repo": v}, "")
	}
}

func TestValidate_ShellMetacharacters(t *testing.T) {
	for _, v := range []string{"cli/cli;rm -rf /", "$(id)", "a\nb", "a\x00b", "cli/cli\n", "cli/cli`id`"} {
		refused(t, "issue_list", map[string]any{"repo": v}, "")
	}
	// A pattern-free control check also stands on its own.
	if err := checkString("ok\x7f"); err == nil {
		t.Error("DEL accepted")
	}
	if err := checkString("ok\u202e"); err == nil {
		t.Error("bidi override accepted")
	}
}

func TestValidate_IntBounds(t *testing.T) {
	for _, v := range []any{0, 101, "5", 1.5, float64(-1), true, nil} {
		refused(t, "issue_list", map[string]any{"repo": "cli/cli", "limit": v}, "")
	}
	for _, v := range []any{1, 100, float64(50), int64(7)} {
		if _, err := gh(t).Argv("issue_list", map[string]any{"repo": "cli/cli", "limit": v}); err != nil {
			t.Errorf("limit %v: %v", v, err)
		}
	}
	refused(t, "issue_view", map[string]any{"repo": "cli/cli", "number": 0}, "")
	refused(t, "issue_view", map[string]any{"repo": "cli/cli", "number": 10000001}, "")
	if _, err := gh(t).Argv("issue_view", map[string]any{"repo": "cli/cli", "number": 10000000}); err != nil {
		t.Errorf("number at max: %v", err)
	}
}

func TestValidate_EnumAndRequired(t *testing.T) {
	refused(t, "issue_list", map[string]any{"repo": "cli/cli", "state": "merged"}, "state")
	refused(t, "issue_list", map[string]any{"repo": "cli/cli", "state": 1}, "state")
	refused(t, "issue_list", map[string]any{"state": "open"}, "repo")
	refused(t, "issue_view", map[string]any{"repo": "cli/cli"}, "number")
	if _, err := gh(t).Argv("pr_list", map[string]any{"repo": "cli/cli", "state": "merged"}); err != nil {
		t.Errorf("pr_list merged: %v", err)
	}
}

func TestValidate_UnknownCommand(t *testing.T) {
	refused(t, "issue_create", map[string]any{"repo": "cli/cli"}, `unknown command "issue_create"`)
	refused(t, "", nil, "unknown command")
}

func TestValidate_RepoSegmentsStartAlphanumeric(t *testing.T) {
	for _, v := range []string{"../..", ".x/y", "x/.y", "_x/y", "x/-y"} {
		refused(t, "issue_list", map[string]any{"repo": v}, "")
		refused(t, "repo_view", map[string]any{"repo": v}, "")
	}
	for _, v := range []string{"cli/cli", "a/b.c", "a_b/c-d"} {
		if _, err := gh(t).Argv("issue_list", map[string]any{"repo": v}); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
}
