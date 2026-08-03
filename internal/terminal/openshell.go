package terminal

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/zanetworker/aimux/internal/debuglog"
)

// OpenShellExecBackend implements SessionBackend for remote OpenShell sandbox
// sessions. It runs `openshell sandbox connect <name>` inside a real PTY (via
// creack/pty), giving the connect process a controlling-TTY-like environment
// so it behaves as a genuine interactive terminal. This mirrors the k8s
// KubectlExecBackend and deliberately avoids tmux: there is no detached tmux
// session to die, no capture-pane polling, and no send-keys injection. The
// sandbox itself is a gateway resource that outlives the connect process, so
// closing this backend (leaving the view) does not destroy the sandbox.
type OpenShellExecBackend struct {
	sandbox string
	ptmx    *os.File  // PTY master — Read/Write go here
	cmd     *exec.Cmd // openshell connect process — killed on Close

	mu     sync.Mutex
	closed bool
}

// openshellConnectArgs builds the argument list for `openshell sandbox connect`.
// Kept as a pure function so it can be unit-tested without a live gateway.
func openshellConnectArgs(sandbox, gatewayEndpoint string, insecure bool) []string {
	args := []string{"sandbox", "connect"}
	if sandbox != "" {
		args = append(args, sandbox)
	}
	if gatewayEndpoint != "" {
		args = append(args, "--gateway-endpoint", gatewayEndpoint)
	}
	if insecure {
		args = append(args, "--gateway-insecure")
	}
	return args
}

// NewOpenShellExec starts `openshell sandbox connect <sandbox>` in a real PTY.
// gatewayEndpoint is optional; when empty, the openshell CLI resolves its
// configured/selected gateway. cols/rows set the initial terminal size.
func NewOpenShellExec(sandbox, gatewayEndpoint string, insecure bool, cols, rows int) (*OpenShellExecBackend, error) {
	if sandbox == "" {
		return nil, fmt.Errorf("openshell exec: sandbox name is required")
	}

	args := openshellConnectArgs(sandbox, gatewayEndpoint, insecure)
	cmd := exec.Command("openshell", args...) // #nosec G204
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// Create the PTY pair manually WITHOUT Setctty. On macOS, Setctty
	// (TIOCSCTTY) steals the controlling terminal from the parent, which
	// breaks Bubble Tea's stdin reader. openshell only needs isatty()=true on
	// its stdin/stdout, not a controlling terminal. (Same rationale as
	// KubectlExecBackend.)
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("openshell exec: pty open: %w", err)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil { // #nosec G115 -- terminal size safe to convert
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, fmt.Errorf("openshell exec: pty setsize: %w", err)
	}
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // new session, but NO Setctty

	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, fmt.Errorf("openshell exec: start: %w", err)
	}
	_ = tty.Close() // child holds it; parent doesn't need it

	debuglog.Log("openshell: connect started for sandbox %s (cols=%d, rows=%d, pid=%d)",
		sandbox, cols, rows, cmd.Process.Pid)

	return &OpenShellExecBackend{
		sandbox: sandbox,
		ptmx:    ptmx,
		cmd:     cmd,
	}, nil
}

// Read reads from the PTY. Blocks until data is available or the PTY closes.
func (ob *OpenShellExecBackend) Read(buf []byte) (int, error) {
	ob.mu.Lock()
	if ob.closed {
		ob.mu.Unlock()
		return 0, io.EOF
	}
	f := ob.ptmx
	ob.mu.Unlock()

	return f.Read(buf)
}

// Write sends input to the PTY — keystrokes go directly to the remote shell.
func (ob *OpenShellExecBackend) Write(data []byte) (int, error) {
	ob.mu.Lock()
	if ob.closed {
		ob.mu.Unlock()
		return 0, io.EOF
	}
	f := ob.ptmx
	ob.mu.Unlock()

	return f.Write(data)
}

// Resize changes the PTY window size.
func (ob *OpenShellExecBackend) Resize(cols, rows int) error {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if ob.closed || ob.ptmx == nil {
		return nil
	}
	return pty.Setsize(ob.ptmx, &pty.Winsize{
		Cols: uint16(cols), // #nosec G115 -- terminal size safe to convert
		Rows: uint16(rows), // #nosec G115 -- terminal size safe to convert
	})
}

// Close kills the connect process and closes the PTY. The sandbox itself is a
// gateway resource and continues running; it can be reconnected later.
func (ob *OpenShellExecBackend) Close() error {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if ob.closed {
		return nil
	}
	ob.closed = true

	debuglog.Log("openshell: closing connect session for sandbox %s", ob.sandbox)

	if ob.cmd != nil && ob.cmd.Process != nil {
		_ = ob.cmd.Process.Kill()
		go func() { _ = ob.cmd.Wait() }()
	}
	if ob.ptmx != nil {
		return ob.ptmx.Close()
	}
	return nil
}

// Alive reports whether the connect process is still running.
func (ob *OpenShellExecBackend) Alive() bool {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	if ob.closed {
		return false
	}
	if ob.cmd != nil && ob.cmd.ProcessState != nil {
		return false
	}
	return true
}

// Sandbox returns the sandbox name for external reference.
func (ob *OpenShellExecBackend) Sandbox() string {
	return ob.sandbox
}
