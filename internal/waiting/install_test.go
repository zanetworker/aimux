package waiting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cmdA = "/opt/bin/aimux remote hooks notify"

func settingsIn(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".claude", "settings.json")
	if content != "" {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil { //nolint:gosec // a world-readable settings file whose mode must survive
			t.Fatal(err)
		}
	}
	return p
}

type wireGroup struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	} `json:"hooks"`
}

func readHooks(t *testing.T, p string) map[string][]wireGroup {
	t.Helper()
	data, err := os.ReadFile(p) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]wireGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("settings are not JSON: %v\n%s", err, data)
	}
	return s.Hooks
}

func backups(t *testing.T, p string) []string {
	t.Helper()
	m, _ := filepath.Glob(p + ".aimux-bak-*")
	return m
}

func TestInstallCreatesMissingSettings(t *testing.T) {
	p := settingsIn(t, "")
	ch, err := Install(p, cmdA, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Added) != 1+len(ClearEvents) || ch.Backup != "" {
		t.Fatalf("change = %+v", ch)
	}
	h := readHooks(t, p)
	n := h["Notification"]
	if len(n) != 1 || n[0].Matcher != "permission_prompt" || n[0].Hooks[0].Command != cmdA || n[0].Hooks[0].Type != "command" || n[0].Hooks[0].Timeout <= 0 {
		t.Fatalf("Notification = %+v", n)
	}
	for _, ev := range ClearEvents {
		if g := h[ev]; len(g) != 1 || g[0].Hooks[0].Command != cmdA || g[0].Matcher != "" {
			t.Fatalf("%s = %+v", ev, g)
		}
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("new settings mode = %v, want 0600", fi.Mode().Perm())
	}
}

const existing = `{
  "permissions": {"allow": ["Bash(ls)"]},
  "hooks": {
    "PostToolUse": [
      {"matcher": "Write", "hooks": [{"type": "command", "command": "fmt-on-save"}]}
    ],
    "Notification": [
      {"hooks": [{"type": "command", "command": "say hello"}]}
    ]
  },
  "model": "some-model"
}`

func TestInstallKeepsEverythingElse(t *testing.T) {
	p := settingsIn(t, existing)
	ch, err := Install(p, cmdA, t0)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Backup == "" {
		t.Fatal("no backup made")
	}
	b, _ := os.ReadFile(ch.Backup) //nolint:gosec // test temp dir
	if string(b) != existing {
		t.Fatal("backup differs from the original")
	}
	h := readHooks(t, p)
	if g := h["PostToolUse"]; len(g) != 2 || g[0].Hooks[0].Command != "fmt-on-save" || g[0].Matcher != "Write" || g[1].Hooks[0].Command != cmdA {
		t.Fatalf("PostToolUse = %+v", g)
	}
	if g := h["Notification"]; len(g) != 2 || g[0].Hooks[0].Command != "say hello" {
		t.Fatalf("Notification = %+v", g)
	}
	data, _ := os.ReadFile(p) //nolint:gosec // test temp dir
	s := string(data)
	ip, ih, im := strings.Index(s, `"permissions"`), strings.Index(s, `"hooks"`), strings.Index(s, `"model"`)
	if ip < 0 || ih < ip || im < ih || !strings.Contains(s, `"Bash(ls)"`) || !strings.Contains(s, `"some-model"`) {
		t.Fatalf("top-level keys lost or reordered:\n%s", s)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed to %v", fi.Mode().Perm())
	}
}

func TestInstallTwiceChangesNothing(t *testing.T) {
	p := settingsIn(t, existing)
	if _, err := Install(p, cmdA, t0); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p) //nolint:gosec // test temp dir
	ch, err := Install(p, cmdA, t0.Add(1e9))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p) //nolint:gosec // test temp dir
	if len(ch.Added)+len(ch.Updated) != 0 || ch.Backup != "" || string(before) != string(after) {
		t.Fatalf("second install changed something: %+v", ch)
	}
	if len(backups(t, p)) != 1 {
		t.Fatalf("backups = %v, want one", backups(t, p))
	}
}

func TestInstallUpdatesAMovedBinary(t *testing.T) {
	p := settingsIn(t, "")
	_, _ = Install(p, "/old/aimux remote hooks notify", t0)
	ch, err := Install(p, cmdA, t0.Add(1e9))
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Updated) != 1+len(ClearEvents) || len(ch.Added) != 0 {
		t.Fatalf("change = %+v", ch)
	}
	for ev, g := range readHooks(t, p) {
		if len(g) != 1 || g[0].Hooks[0].Command != cmdA {
			t.Fatalf("%s = %+v", ev, g)
		}
	}
}

func TestInstallRefusesBrokenSettings(t *testing.T) {
	for _, content := range []string{"{not json", `["a list"]`, `{"hooks": "not an object"}`, `{"hooks": {"Stop": "not a list"}}`} {
		p := settingsIn(t, content)
		if _, err := Install(p, cmdA, t0); err == nil {
			t.Errorf("Install over %q = nil, want an error", content)
		}
		got, _ := os.ReadFile(p) //nolint:gosec // test temp dir
		if string(got) != content || len(backups(t, p)) != 0 {
			t.Errorf("broken settings %q were touched", content)
		}
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	p := settingsIn(t, existing)
	_, _ = Install(p, cmdA, t0)
	ch, err := Uninstall(p, t0.Add(1e9))
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Removed) != 1+len(ClearEvents) || ch.Backup == "" {
		t.Fatalf("change = %+v", ch)
	}
	h := readHooks(t, p)
	if len(h) != 2 || len(h["PostToolUse"]) != 1 || len(h["Notification"]) != 1 {
		t.Fatalf("hooks after uninstall = %+v", h)
	}
	ch, err = Uninstall(p, t0.Add(2e9))
	if err != nil || len(ch.Removed) != 0 || ch.Backup != "" {
		t.Fatalf("second uninstall = %+v, %v", ch, err)
	}
}

func TestUninstallDropsEmptyHooks(t *testing.T) {
	p := settingsIn(t, `{"model": "m"}`)
	_, _ = Install(p, cmdA, t0)
	if _, err := Uninstall(p, t0.Add(1e9)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p) //nolint:gosec // test temp dir
	var s map[string]any
	_ = json.Unmarshal(data, &s)
	if _, ok := s["hooks"]; ok || s["model"] != "m" {
		t.Fatalf("settings after uninstall = %s", data)
	}
}

func TestUninstallMissingSettings(t *testing.T) {
	p := settingsIn(t, "")
	ch, err := Uninstall(p, t0)
	if err != nil || len(ch.Removed) != 0 {
		t.Fatalf("Uninstall = %+v, %v", ch, err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("uninstall created the settings file")
	}
}

func TestStatus(t *testing.T) {
	p := settingsIn(t, existing)
	st, err := Status(p)
	if err != nil || len(st.Installed) != 0 || len(st.Missing) != 1+len(ClearEvents) {
		t.Fatalf("before = %+v, %v", st, err)
	}
	_, _ = Install(p, cmdA, t0)
	st, _ = Status(p)
	if len(st.Missing) != 0 || len(st.Commands) != 1 || st.Commands[0] != cmdA {
		t.Fatalf("after = %+v", st)
	}
	st, err = Status(settingsIn(t, ""))
	if err != nil || len(st.Missing) != 1+len(ClearEvents) {
		t.Fatalf("missing file = %+v, %v", st, err)
	}
}

func TestHookCommandQuotesPathsAndNeverFails(t *testing.T) {
	if got := HookCommand("/opt/bin/aimux"); got != cmdA+" || true" {
		t.Fatalf("HookCommand = %q", got)
	}
	if got := HookCommand("/Apps/My Tools/aimux"); got != `'/Apps/My Tools/aimux' remote hooks notify || true` {
		t.Fatalf("HookCommand = %q", got)
	}
	if got := HookCommand("/a/it's/aimux"); got != `'/a/it'\''s/aimux' remote hooks notify || true` {
		t.Fatalf("HookCommand = %q", got)
	}
}

func TestCommandBinary(t *testing.T) {
	for cmd, want := range map[string]string{
		"/opt/bin/aimux remote hooks notify":                 "/opt/bin/aimux",
		"/opt/bin/aimux remote hooks notify || true":         "/opt/bin/aimux",
		`'/Apps/My Tools/aimux' remote hooks notify || true`: "/Apps/My Tools/aimux",
		`'/a/it'\''s/aimux' remote hooks notify || true`:     "/a/it's/aimux",
		"echo hi": "",
	} {
		if got, _ := CommandBinary(cmd); got != want {
			t.Errorf("CommandBinary(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestInstallMovesTheOldFormToTheNewOne(t *testing.T) {
	p := settingsIn(t, "")
	_, _ = Install(p, cmdA, t0) // the form before "|| true"
	st, _ := Status(p)
	if len(st.Missing) != 0 {
		t.Fatalf("old form not recognised: %+v", st)
	}
	ch, err := Install(p, HookCommand("/opt/bin/aimux"), t0.Add(1e9))
	if err != nil || len(ch.Updated) != len(Events()) || len(ch.Added) != 0 {
		t.Fatalf("change = %+v, %v", ch, err)
	}
}

const mixed = `{"hooks": {"Stop": [{"hooks": [
  {"type": "command", "command": "say done"},
  {"type": "command", "command": "/old/aimux remote hooks notify || true", "timeout": 10}
]}]}}`

func TestOursInAMixedGroup(t *testing.T) {
	p := settingsIn(t, mixed)
	st, _ := Status(p)
	if len(st.Installed) != 1 || st.Installed[0] != "Stop" {
		t.Fatalf("status = %+v", st)
	}
	ch, err := Install(p, cmdA, t0)
	if err != nil || len(ch.Updated) != 1 || ch.Updated[0] != "Stop" || len(ch.Added) != len(Events())-1 {
		t.Fatalf("install = %+v, %v", ch, err)
	}
	g := readHooks(t, p)["Stop"]
	if len(g) != 1 || len(g[0].Hooks) != 2 || g[0].Hooks[0].Command != "say done" || g[0].Hooks[1].Command != cmdA {
		t.Fatalf("Stop = %+v", g)
	}
	if _, err := Uninstall(p, t0.Add(1e9)); err != nil {
		t.Fatal(err)
	}
	g = readHooks(t, p)["Stop"]
	if len(g) != 1 || len(g[0].Hooks) != 1 || g[0].Hooks[0].Command != "say done" {
		t.Fatalf("Stop after uninstall = %+v", g)
	}
}

func TestInstallKeepsShellCharactersReadable(t *testing.T) {
	p := settingsIn(t, `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "a && b > c"}]}]}}`)
	if _, err := Install(p, cmdA, t0); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p) //nolint:gosec // test temp dir
	if !strings.Contains(string(data), `"a && b > c"`) {
		t.Fatalf("command was escaped:\n%s", data)
	}
}

func TestInstallKeepsOtherKeysByteIdentical(t *testing.T) {
	// Irregular spacing, HTML characters and an exponent: a re-encode would change all three.
	const orig = "{\n  \"permissions\": {\"allow\":   [\"Bash(ls && pwd)\"]},\n  \"env\": {\"A\": \"<&>\", \"N\": 1e5},\n  \"model\": \"m\"\n}\n"
	p := settingsIn(t, orig)
	if _, err := Install(p, cmdA, t0); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p) //nolint:gosec // test temp dir
	head := strings.TrimSuffix(orig, "\n}\n") + ",\n  \"hooks\": {"
	if s := string(data); !strings.HasPrefix(s, head) || !strings.HasSuffix(s, "\n  }\n}\n") {
		t.Fatalf("other keys changed:\n%s", s)
	}
	if _, err := Uninstall(p, t0.Add(1e9)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != orig { //nolint:gosec // test temp dir
		t.Fatalf("install then uninstall did not restore the original bytes:\n%s", data)
	}
}

func TestInstallKeepsKeysAroundExistingHooks(t *testing.T) {
	const before, after = "{\"a\" : 1 ,\"hooks\":", ", \"z\":[1, 2]}"
	p := settingsIn(t, before+`{"Stop":[{"hooks":[{"type":"command","command":"say hi"}]}]}`+after)
	if _, err := Install(p, cmdA, t0); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p) //nolint:gosec // test temp dir
	if s := string(data); !strings.HasPrefix(s, before) || !strings.HasSuffix(s, after) {
		t.Fatalf("keys around hooks changed:\n%s", s)
	}
	if g := readHooks(t, p)["Stop"]; len(g) != 2 || g[0].Hooks[0].Command != "say hi" {
		t.Fatalf("Stop = %+v", g)
	}
}
