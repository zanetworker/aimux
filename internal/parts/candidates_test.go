package parts

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func candidatesOf(t *testing.T, config string) Candidates {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config.yaml")
	if config != "" {
		if err := os.WriteFile(file, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return EditCandidates(file)
}

// The names each edit would take are the ones it would not refuse.
func TestEditCandidates(t *testing.T) {
	const engines = "    engines:\n      anthropic:\n        enabled: true\n      enmaas:\n        enabled: true\n        type: messages\n        url: https://gateway.example\n      spare:\n        enabled: false\n        type: messages\n        url: https://spare.example\n"
	got := candidatesOf(t, "parts:\n  inference-proxy:\n    enabled: true\n"+engines+
		"    models:\n      fast: codex:gpt-6.2-sol\n      wide: enmaas:big\n      deep: codex:default\n    roles:\n      subagent: fast\n")
	want := Candidates{
		Enable:      []string{"spare"},
		Disable:     []string{"anthropic"}, // enmaas is on, and an entry of models still names it; codex is never switched
		Remove:      []string{"spare"},     // anthropic is built in; enmaas is still named
		Entries:     []string{"deep", "fast", "wide"},
		FreeEntries: []string{"deep", "wide"}, // fast is what the subagent role uses
		Roles:       []string{RoleSubagent},
		SetRoles:    []string{RoleSubagent},
	}
	for name, pair := range map[string][2][]string{
		"Enable": {got.Enable, want.Enable}, "Disable": {got.Disable, want.Disable}, "Remove": {got.Remove, want.Remove},
		"Entries": {got.Entries, want.Entries}, "FreeEntries": {got.FreeEntries, want.FreeEntries},
		"Roles": {got.Roles, want.Roles}, "SetRoles": {got.SetRoles, want.SetRoles},
	} {
		if !slices.Equal(pair[0], pair[1]) {
			t.Errorf("%s: %v, want %v", name, pair[0], pair[1])
		}
	}

	// With the proxy turned off, the edits that need it on take nothing; taking things out still works.
	off := candidatesOf(t, "parts:\n  inference-proxy:\n    enabled: false\n"+engines+"    models:\n      fast: codex:gpt-6.2-sol\n    roles:\n      subagent: fast\n")
	if len(off.Enable)+len(off.Disable)+len(off.Entries)+len(off.Roles) != 0 {
		t.Errorf("the proxy is off, and these would be refused: %+v", off)
	}
	if !slices.Equal(off.Remove, []string{"enmaas", "spare"}) || !slices.Equal(off.SetRoles, []string{RoleSubagent}) {
		t.Errorf("taking out, with the proxy off: %+v", off)
	}

	// No file yet: Anthropic, which is built in, can be switched on, and a role can be named; nothing else.
	none := candidatesOf(t, "")
	if len(none.Disable)+len(none.Remove)+len(none.Entries)+len(none.FreeEntries)+len(none.SetRoles) != 0 || !slices.Equal(none.Enable, []string{EngineAnthropic}) || !slices.Equal(none.Roles, []string{RoleSubagent}) {
		t.Errorf("no file: %+v", none)
	}

	// A file every edit would refuse offers nothing at all: one that cannot be read, and one that reads and has a
	// problem (a role naming an entry that is not there).
	for _, broken := range []string{"parts: [\n", "parts:\n  inference-proxy:\n    enabled: true\n    roles:\n      subagent: missing\n"} {
		if c := candidatesOf(t, broken); len(c.Enable)+len(c.Disable)+len(c.Remove)+len(c.Entries)+len(c.FreeEntries)+len(c.Roles)+len(c.SetRoles) != 0 {
			t.Errorf("%q: %+v", broken, c)
		}
	}
}
