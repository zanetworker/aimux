package remoteapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileRuns_BuiltinsAndUserProfiles(t *testing.T) {
	dir := t.TempDir()
	user := "name: my-sandbox\nextends: builtin:work-readonly\n"
	if err := os.WriteFile(filepath.Join(dir, "my-sandbox.yaml"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	// One bad file must not hide the good ones.
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("nonsense: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs := profileRuns(dir)
	for name, want := range map[string]string{"page": "acp", "work-readonly": "sandbox", "my-sandbox": "sandbox", "coding": "host"} {
		if got, err := runs(name); err != nil || got != want {
			t.Errorf("%s: runs %q %v, want %q", name, got, err, want)
		}
	}
	if _, err := runs("nope"); err == nil {
		t.Error("unknown profile resolved")
	}
}

func TestProfileDir_EnvOverridesHome(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got := profileDir("/h", getenv); got != filepath.Join("/h", ".config", "aimux", "profiles") {
		t.Fatalf("default dir %q", got)
	}
	env["AIMUX_PROFILE_DIR"] = "/elsewhere"
	if got := profileDir("/h", getenv); got != "/elsewhere" {
		t.Fatalf("env dir %q", got)
	}
}

// The catalog the API serves is resolved: builtins and user profiles, a user
// child of coding coming back with Runs host (the Wingman host rule reads
// it), and a bad file logged once per read, never hiding the rest.
func TestProfileCatalog_ResolvedBuiltinsAndUserProfiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"my-coding.yaml":  "name: my-coding\nextends: builtin:coding\n",
		"my-sandbox.yaml": "name: my-sandbox\nextends: builtin:work-readonly\n",
		"bad.yaml":        "nonsense: [\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var logged strings.Builder
	all, err := profileCatalog(dir, &logged)()
	if err != nil {
		t.Fatal(err)
	}
	for name, runs := range map[string]string{"page": "acp", "coding": "host", "base": "", "my-coding": "host", "my-sandbox": "sandbox"} {
		m, ok := all[name]
		if !ok {
			t.Errorf("%s missing from %d profiles", name, len(all))
			continue
		}
		if m.Name != name || (runs != "" && m.Runs != runs) {
			t.Errorf("%s: name %q runs %q, want runs %q", name, m.Name, m.Runs, runs)
		}
	}
	if all["my-sandbox"].Network.Default == "" {
		t.Error("my-sandbox not resolved (network inherited from work-readonly is empty)")
	}
	if !strings.Contains(logged.String(), "bad.yaml") {
		t.Errorf("bad file not logged: %q", logged.String())
	}
}
