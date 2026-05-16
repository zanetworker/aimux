package cmd

import (
	"bytes"
	"encoding/json"
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

	out, err := runProfileCmd(t, store, []string{"profile", "save", "dev", "--provider", "gemini"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"dev" saved`) {
		t.Errorf("expected save confirmation, got: %s", out)
	}
}

func TestProfileListCmd(t *testing.T) {
	store := testStore(t)
	_ = store.Save(profile.Profile{Name: "a", Provider: "claude"})
	_ = store.Save(profile.Profile{Name: "b", Provider: "codex"})
	jsonOutput = true
	defer func() { jsonOutput = false }()

	out, err := runProfileCmd(t, store, []string{"profile", "list", "--json"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result struct {
		Profiles []profile.Profile `json:"profiles"`
		Count    int               `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("not valid JSON: %v\noutput: %s", err, out)
	}
	if result.Count != 2 {
		t.Errorf("count=%d, want 2", result.Count)
	}
}

func TestProfileListCmd_Empty(t *testing.T) {
	store := testStore(t)
	jsonOutput = false

	out, err := runProfileCmd(t, store, []string{"profile", "list"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No profiles saved") {
		t.Errorf("expected empty message, got: %s", out)
	}
}

func TestProfileGetCmd(t *testing.T) {
	store := testStore(t)
	_ = store.Save(profile.Profile{Name: "dev", Provider: "gemini", Dir: "/tmp"})
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
	if got.Provider != "gemini" {
		t.Errorf("Provider=%q, want %q", got.Provider, "gemini")
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
