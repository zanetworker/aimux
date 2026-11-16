package sandboxlaunch

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/zanetworker/aimux/internal/harnessconfig"
)

// The wrapper really swaps the placeholder for a 0600 file holding line 1,
// feeds the rest to the command, and removes the file afterwards.
func TestWorkWrapperRunsInSh(t *testing.T) {
	eachShell(t, testWorkWrapperRuns)
}

// eachShell runs fn under /bin/sh and, when installed, dash (the sandbox image's sh).
func eachShell(t *testing.T, fn func(t *testing.T, shell string)) {
	shells := []string{"sh"}
	if p, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, p)
	}
	for _, sh := range shells {
		t.Run(sh, func(t *testing.T) { fn(t, sh) })
	}
}

func testWorkWrapperRuns(t *testing.T, shell string) {
	cmd := exec.Command(shell, "-c", WorkWrapper, "sh", "sh", "-c", //nolint:gosec // G204: shell is a fixed sh/bash from the test table
		`m=$(stat -f %Lp "$1" 2>/dev/null || stat -c %a "$1"); echo "mode=$m"; echo "cfg=$(cat "$1")"; echo "prompt=$(cat)"; echo "$1"`,
		"x", harnessconfig.MCPConfigPlaceholder)
	cmd.Stdin = strings.NewReader("{\"tok\":1}\nthe prompt\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if lines[0] != "mode=600" || lines[1] != `cfg={"tok":1}` || lines[2] != "prompt=the prompt" {
		t.Fatalf("out = %q", lines)
	}
	if _, err := exec.Command("test", "-e", lines[3]).CombinedOutput(); err == nil { //nolint:gosec // G204: lines[3] is the temp path the wrapper just printed
		t.Fatal("temp file not removed")
	}
}

func TestWorkWrapperSignalCleanup(t *testing.T) {
	eachShell(t, testWrapperSignals)
}

func testWrapperSignals(t *testing.T, shell string) {
	for sig, code := range map[syscall.Signal]int{syscall.SIGTERM: 143, syscall.SIGINT: 130, syscall.SIGHUP: 129} {
		cmd := exec.Command(shell, "-c", WorkWrapper, "sh", "sh", "-c", `echo "$1"; exec sleep 30`, "x", harnessconfig.MCPConfigPlaceholder) //nolint:gosec // G204: shell is a fixed sh/bash from the test table
		cmd.Stdin = strings.NewReader("{}\nprompt\n")
		out, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		path, _ := bufio.NewReader(out).ReadString('\n')
		path = strings.TrimSpace(path)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("config file missing before signal: %v", err)
		}
		_ = cmd.Process.Signal(sig)
		err := cmd.Wait()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != code {
			t.Fatalf("%v: exit = %v, want %d", sig, err, code)
		}
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%v: temp file not removed", sig)
		}
	}
}

// Runs the wrapper in the real sandbox image (Ubuntu, sh is dash).
func TestWorkWrapperInSandboxImage(t *testing.T) {
	const image = "localhost/wingman-sandbox:2.1.319"
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not installed")
	}
	if err := exec.Command("podman", "image", "exists", image).Run(); err != nil {
		t.Skip("image not available: " + image)
	}
	inner := `sh -c "$W" sh sh -c 'cat; echo "$1" > /tmp/p' x ` + harnessconfig.MCPConfigPlaceholder + `; test ! -e "$(cat /tmp/p)" && echo FILE_GONE`
	cmd := exec.Command("podman", "run", "--rm", "-i", "-e", "W="+WorkWrapper, "--entrypoint", "sh", image, "-c", inner) //nolint:gosec // G204: image is a fixed test image name
	cmd.Stdin = strings.NewReader("{\"tok\":1}\nthe prompt\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.Contains(string(out), "the prompt") || !strings.Contains(string(out), "FILE_GONE") {
		t.Fatalf("out = %q", out)
	}
}

// envRun runs the real wrapper as Exec does: the env prefix, then cmd.
func envRun(t *testing.T, shell string, env map[string]string, stdin string, cmd ...string) (string, error) {
	t.Helper()
	pre, err := envArgs(env)
	if err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"-c", WorkWrapper, "sh"}, append(pre, cmd...)...)
	c := exec.Command(shell, argv...) //nolint:gosec // G204: shell is a fixed sh/bash from the test table
	c.Env = []string{"PATH=" + os.Getenv("PATH")}
	c.Stdin = strings.NewReader(stdin)
	out, err := c.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Each env entry reaches the command's environment, and the stdin contract
// holds: line 1 is the config, the rest the prompt.
func TestWrapper_ExportsEnv(t *testing.T) {
	eachShell(t, func(t *testing.T, shell string) {
		out, err := envRun(t, shell, map[string]string{"DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1", "AIMUX_X": "a.b_c-9"},
			"{\"mcpServers\":{}}\nthe prompt\n",
			"sh", "-c", `echo "t=$DISABLE_TELEMETRY e=$DISABLE_ERROR_REPORTING x=$AIMUX_X"; echo "cfg=$(cat "$1")"; echo "prompt=$(cat)"`,
			"x", harnessconfig.MCPConfigPlaceholder)
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if want := "t=1 e=1 x=a.b_c-9\ncfg={\"mcpServers\":{}}\nprompt=the prompt"; out != want {
			t.Fatalf("out = %q, want %q", out, want)
		}
	})
}

// Env reaches only the command, never the wrapper shell: an IFS of digits
// would break the wrapper's own `wait $p` and `exit $?` (the command's exit
// status would be lost), but here the status and config come through.
func TestWrapper_EnvDoesNotReachWrapperShell(t *testing.T) {
	eachShell(t, func(t *testing.T, shell string) {
		out, err := envRun(t, shell, map[string]string{"IFS": "0123456789", "AIMUX_X": "v"}, "{\"tok\":10}\nq\n",
			"sh", "-c", `echo "x=$AIMUX_X cfg=$(cat "$1")"; exit 3`, "x", harnessconfig.MCPConfigPlaceholder)
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 3 {
			t.Fatalf("exit = %v, want the command's 3: %s", err, out)
		}
		if out != `x=v cfg={"tok":10}` {
			t.Fatalf("out = %q", out)
		}
	})
}

// No env adds nothing in front of the command.
func TestWrapper_NoEnvAddsNothing(t *testing.T) {
	for _, env := range []map[string]string{nil, {}} {
		if pre, err := envArgs(env); err != nil || len(pre) != 0 {
			t.Fatalf("envArgs(%v) = %q, %v; want none", env, pre, err)
		}
	}
}

// A name or value outside the safe sets fails the launch: nothing runs.
func TestWrapper_RejectsBadEnv(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"lower-case name":    {"disable": "1"},
		"leading digit":      {"1X": "1"},
		"dash in name":       {"A-B": "1"},
		"empty name":         {"": "1"},
		"space in value":     {"A": "a b"},
		"substitution":       {"A": "$(id)"},
		"quote in value":     {"A": "a'b"},
		"newline in value":   {"A": "a\nb"},
		"semicolon in value": {"A": "1;rm"},
	} {
		if _, err := envArgs(env); err == nil {
			t.Errorf("%s: accepted", name)
		}
		f := &fakeRunner{respond: func([]string) (string, error) { return "", nil }}
		b := newTestCLI(f, t.TempDir())
		if _, err := b.Exec(context.Background(), compiled("d"), "{}", "q", env, []string{"claude", "--mcp-config", harnessconfig.MCPConfigPlaceholder}); err == nil {
			t.Errorf("%s: Exec accepted", name)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: ran %q", name, f.calls)
		}
	}
}

// Exec runs the plain wrapper with `env NAME=VALUE...` (sorted) in front of
// the command; stdin is unchanged.
func TestCLIExec_EnvInWrapper(t *testing.T) {
	f := &fakeRunner{respond: func([]string) (string, error) { return "", nil }}
	b := newTestCLI(f, t.TempDir())
	args := []string{"claude", "--mcp-config", harnessconfig.MCPConfigPlaceholder}
	rc, err := b.Exec(context.Background(), compiled("d"), "{}", "q", map[string]string{"DISABLE_TELEMETRY": "1", "A": "2"}, args)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	want := append([]string{"openshell", "-g", "local", "sandbox", "exec", "--no-login-shell", "--no-tty",
		"aimux-work-readonly", "--", "sh", "-c", WorkWrapper, "sh", "env", "A=2", "DISABLE_TELEMETRY=1"}, args...)
	if !slices.Equal(f.calls[0], want) {
		t.Fatalf("argv = %q\nwant   %q", f.calls[0], want)
	}
	if f.stdins[0] != "{}\nq" {
		t.Fatalf("stdin = %q", f.stdins[0])
	}
}
