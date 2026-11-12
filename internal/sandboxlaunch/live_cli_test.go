//go:build openshell_live

package sandboxlaunch

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/profile"
)

// The chosen CLI backend end to end on the owner's gateway, in the spike
// sandbox only, which is always deleted:
//
//	go test -tags openshell_live ./internal/sandboxlaunch/ -run LiveCLI -v
func TestLiveCLIBackend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	g := Gateway{Name: "local"}
	b, err := connect(ctx, g, ExecRunner(), t.TempDir())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() {
		out, err := exec.Command("openshell", "-g", "local", "sandbox", "delete", spikeSandbox).CombinedOutput()
		t.Logf("delete %s: err=%v %s", spikeSandbox, err, strings.TrimSpace(string(out)))
	}()
	golden, err := os.ReadFile("../profile/testdata/compile/work-readonly.policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c := profile.Compiled{Profile: "work-readonly", Digest: "sha256:live-1", PolicyDigest: "sha256:live-1", Sandbox: spikeSandbox,
		Image: profile.SandboxImage, Provider: profile.SandboxProvider, PolicyYAML: golden}
	if err := b.Ensure(ctx, c); err != nil {
		t.Fatalf("Ensure create: %v", err)
	}
	t.Log("PASS Ensure created and Ready")
	c.PolicyDigest = "sha256:live-2"
	c.PolicyYAML = []byte(strings.Replace(string(golden), "any: [history]", "any: [history, search]", 1))
	if err := b.Ensure(ctx, c); err != nil {
		t.Fatalf("Ensure update: %v", err)
	}
	t.Log("PASS Ensure updated policy on digest change")
	a, _ := harnessconfig.For("claude-code")
	spec, err := a.Config(harnessconfig.Intent{Mode: "print"})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := b.Exec(ctx, c, `{"mcpServers":{}}`, "say ok", spec.Argv)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	sid := readResult(t, rc)
	if err := rc.Close(); err != nil {
		t.Fatalf("Exec close: %v", err)
	}
	dir := t.TempDir()
	if err := Archive(ctx, b, spikeSandbox, sid, dir); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	st, err := os.Stat(filepath.Join(dir, "openshell-"+spikeSandbox, sid+".jsonl"))
	if err != nil || st.Mode().Perm() != 0o600 || st.Size() == 0 {
		t.Fatalf("archived file: %v %v", st, err)
	}
	t.Logf("PASS archived %d bytes, mode %o", st.Size(), st.Mode().Perm())
	out, _ := exec.Command("openshell", "-g", "local", "sandbox", "exec", "--no-login-shell", "--no-tty", spikeSandbox, "--", "sh", "-c", "ls -la /tmp; for f in /tmp/tmp.*; do [ -f \"$f\" ] && echo \"FILE $f $(stat -c %a \"$f\")\"; done; true").CombinedOutput()
	t.Logf("/tmp after the run: %s", strings.TrimSpace(string(out)))
	if strings.Contains(string(out), "FILE /tmp/tmp.") {
		t.Fatal("the MCP config file was left in the sandbox")
	}
}

// spikeSandbox is the only sandbox live tests may create; it is always deleted.
const spikeSandbox = "aimux-sdk-spike"

// readResult logs the result line (never the config) and returns the session id.
func readResult(t *testing.T, r io.Reader) string {
	t.Helper()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var m struct {
			Type, Result string
			SessionID    string `json:"session_id"`
			IsError      bool   `json:"is_error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "result" {
			continue
		}
		if m.IsError {
			t.Fatalf("FAIL result is_error: %s", m.Result)
		}
		t.Logf("PASS result=%q session=%s", m.Result, m.SessionID)
		return m.SessionID
	}
	t.Fatalf("FAIL no result line (scan err %v)", sc.Err())
	return ""
}
