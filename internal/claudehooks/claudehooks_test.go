package claudehooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	t0    = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	proxy = Spec{
		{Event: "PostToolUse", Matcher: "Bash", Suffix: " mcp proxy hooks added", Timeout: 120},
		{Event: "SessionStart", Suffix: " mcp proxy hooks check", Timeout: 10},
	}
	remote = Spec{
		{Event: "Notification", Matcher: "permission_prompt", Suffix: " remote hooks notify", Timeout: 10},
		{Event: "PostToolUse", Suffix: " remote hooks notify", Timeout: 10},
	}
)

func settingsFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// read is os.ReadFile for files this test made under t.TempDir.
func read(p string) ([]byte, error) {
	return os.ReadFile(p) // #nosec G304 -- a file under t.TempDir
}

func hooksOf(t *testing.T, p string) map[string][]map[string]any {
	t.Helper()
	data, err := read(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Hooks
}

func commandsIn(group map[string]any) []string {
	var out []string
	for _, h := range group["hooks"].([]any) {
		out = append(out, h.(map[string]any)["command"].(string))
	}
	return out
}

func TestInstall_TwoCommandsTwoEvents(t *testing.T) {
	p := settingsFile(t, "")
	ch, err := Install(p, proxy, "/bin/aimux", t0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ch.Added, ",") != "PostToolUse,SessionStart" {
		t.Fatalf("added %v", ch.Added)
	}
	h := hooksOf(t, p)
	post, start := h["PostToolUse"], h["SessionStart"]
	if len(post) != 1 || post[0]["matcher"] != "Bash" || strings.Join(commandsIn(post[0]), "|") != "/bin/aimux mcp proxy hooks added || true" {
		t.Fatalf("PostToolUse = %v", post)
	}
	if len(start) != 1 || start[0]["matcher"] != nil || strings.Join(commandsIn(start[0]), "|") != "/bin/aimux mcp proxy hooks check || true" {
		t.Fatalf("SessionStart = %v", start)
	}
	if post[0]["hooks"].([]any)[0].(map[string]any)["timeout"] != float64(120) {
		t.Fatalf("timeout = %v", post[0])
	}
	before, _ := read(p)
	ch, err = Install(p, proxy, "/bin/aimux", t0.Add(time.Second))
	if err != nil || len(ch.Added)+len(ch.Updated)+len(ch.Removed) != 0 {
		t.Fatalf("second install: %+v %v", ch, err)
	}
	if after, _ := read(p); string(after) != string(before) {
		t.Fatal("a second install rewrote the file")
	}
}

func TestUninstall_LeavesOtherSpecsAndOwnerHooks(t *testing.T) {
	owner := `{"type":"command","command":"my-linter"}`
	p := settingsFile(t, `{"model":"x","hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[`+owner+`]}]}}`)
	if _, err := Install(p, remote, "/bin/aimux", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(p, proxy, "/bin/aimux", t0); err != nil {
		t.Fatal(err)
	}
	// each spec gets its own group; the owner's group is never edited by install
	var post []string
	for _, g := range hooksOf(t, p)["PostToolUse"] {
		post = append(post, strings.Join(commandsIn(g), "+"))
	}
	if strings.Join(post, "|") != "my-linter|/bin/aimux remote hooks notify || true|/bin/aimux mcp proxy hooks added || true" {
		t.Fatalf("PostToolUse groups = %v", post)
	}
	ch, err := Uninstall(p, proxy, t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ch.Removed, ",") != "PostToolUse,SessionStart" {
		t.Fatalf("removed %v", ch.Removed)
	}
	h := hooksOf(t, p)
	if _, ok := h["SessionStart"]; ok {
		t.Fatal("SessionStart left behind")
	}
	if got := commandsIn(h["PostToolUse"][0]); strings.Join(got, "|") != "my-linter" {
		t.Fatalf("owner's group = %v", got)
	}
	if st, _ := Status(p, remote); len(st.Missing) != 0 {
		t.Fatalf("remote hooks touched: %+v", st)
	}
	if st, _ := Status(p, proxy); len(st.Installed) != 0 {
		t.Fatalf("proxy hooks still there: %+v", st)
	}
}

func TestInstall_BacksUpFirst(t *testing.T) {
	old := `{"model":"x"}`
	p := settingsFile(t, old)
	ch, err := Install(p, proxy, "/bin/aimux", t0)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Backup != p+".aimux-bak-20261109T100000.000000000Z" {
		t.Fatalf("backup %q", ch.Backup)
	}
	if b, _ := read(ch.Backup); string(b) != old {
		t.Fatalf("backup holds %q", b)
	}
}

func TestCommandBinary(t *testing.T) {
	for _, c := range []string{"/bin/aimux mcp proxy hooks added || true", "/bin/aimux mcp proxy hooks added"} {
		if b, ok := CommandBinary(c, " mcp proxy hooks added"); !ok || b != "/bin/aimux" {
			t.Fatalf("%q: %q %v", c, b, ok)
		}
	}
	if b, ok := CommandBinary(Command("/my dir/aimux", " x"), " x"); !ok || b != "/my dir/aimux" {
		t.Fatalf("quoted: %q %v", b, ok)
	}
	if _, ok := CommandBinary("my-linter", " x"); ok {
		t.Fatal("matched another command")
	}
}
