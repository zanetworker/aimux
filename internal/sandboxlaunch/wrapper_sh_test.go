package sandboxlaunch

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
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
