package terminal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/zanetworker/aimux/internal/debuglog"
)

// KubectlExecBackend implements SessionBackend for remote K8s pod sessions.
// It uses `kubectl exec -it` with a real PTY (via creack/pty) to provide
// full interactive terminal access to a tmux session inside a pod.
type KubectlExecBackend struct {
	podName   string
	namespace string
	container string
	ptmx      *os.File   // PTY master — Read/Write go here
	cmd       *exec.Cmd  // kubectl process — killed on Close

	mu     sync.Mutex
	closed bool
}

// NewKubectlExec starts `kubectl exec -it` with a real PTY, attaching to
// (or creating) a tmux session named "main" inside the pod.
func NewKubectlExec(podName, namespace, container string, cols, rows int) (*KubectlExecBackend, error) {
	if podName == "" {
		return nil, fmt.Errorf("kubectl exec: pod name is required")
	}
	if namespace == "" {
		namespace = "default"
	}

	// Default to "session" container to avoid kubectl's
	// "Defaulted container" stderr message on multi-container pods.
	if container == "" {
		container = "session"
	}
	args := []string{"exec", "-it", podName, "-n", namespace, "--container", container}
	// Exec into a shell directly. Tmux is started later via Write() from
	// openK8sSession, which gives the PTY time to initialize properly.
	// This avoids "open terminal failed" errors from tmux when the exec
	// command's TTY allocation hasn't completed yet.
	args = append(args, "--", "bash", "-l")

	cmd := exec.Command("kubectl", args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// Create PTY pair manually WITHOUT Setctty. On macOS, Setctty (TIOCSCTTY)
	// steals the controlling terminal from the parent process, which breaks
	// Bubble Tea's stdin reader. kubectl only needs isatty()=true on its
	// stdin/stdout, not a controlling terminal.
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("kubectl exec: pty open: %w", err)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		ptmx.Close()
		tty.Close()
		return nil, fmt.Errorf("kubectl exec: pty setsize: %w", err)
	}
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // new session, but NO Setctty

	if err := cmd.Start(); err != nil {
		ptmx.Close()
		tty.Close()
		return nil, fmt.Errorf("kubectl exec: start: %w", err)
	}
	tty.Close() // child has it, we don't need it

	debuglog.Log("k8s: kubectl exec -it started for pod %s (cols=%d, rows=%d, pid=%d)", podName, cols, rows, cmd.Process.Pid)

	return &KubectlExecBackend{
		podName:   podName,
		namespace: namespace,
		container: container,
		ptmx:      ptmx,
		cmd:       cmd,
	}, nil
}

// Read reads from the PTY. Blocks until data is available or the PTY closes.
// This is called from a Bubble Tea command goroutine, so blocking is fine —
// Bubble Tea handles key events on a separate goroutine.
func (kb *KubectlExecBackend) Read(buf []byte) (int, error) {
	kb.mu.Lock()
	if kb.closed {
		kb.mu.Unlock()
		return 0, io.EOF
	}
	f := kb.ptmx
	kb.mu.Unlock()

	n, err := f.Read(buf)
	if n > 0 {
		debuglog.Log("k8s: read %d bytes from PTY", n)
	}
	return n, err
}

// Write sends input to the PTY — keystrokes go directly to the remote session.
func (kb *KubectlExecBackend) Write(data []byte) (int, error) {
	kb.mu.Lock()
	if kb.closed {
		kb.mu.Unlock()
		return 0, io.EOF
	}
	f := kb.ptmx
	kb.mu.Unlock()

	debuglog.Log("k8s: writing %d bytes to PTY: %q", len(data), string(data))
	return f.Write(data)
}

// Resize changes the PTY and remote tmux window size.
func (kb *KubectlExecBackend) Resize(cols, rows int) error {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	if kb.closed || kb.ptmx == nil {
		return nil
	}

	return pty.Setsize(kb.ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

// Close kills the kubectl process and closes the PTY. The tmux session
// inside the pod continues running and can be reattached later.
func (kb *KubectlExecBackend) Close() error {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	if kb.closed {
		return nil
	}
	kb.closed = true

	debuglog.Log("k8s: closing kubectl session for pod %s", kb.podName)

	// Kill the kubectl process first, then close the PTY.
	if kb.cmd != nil && kb.cmd.Process != nil {
		_ = kb.cmd.Process.Kill()
		go func() { _ = kb.cmd.Wait() }()
	}

	if kb.ptmx != nil {
		return kb.ptmx.Close()
	}
	return nil
}

// Alive checks if the kubectl process is still running.
func (kb *KubectlExecBackend) Alive() bool {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	if kb.closed {
		return false
	}
	// Check if kubectl process has exited.
	if kb.cmd != nil && kb.cmd.ProcessState != nil {
		return false
	}
	return true
}

// podPhase queries the pod's status.phase via kubectl.
func podPhase(podName, namespace string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kubectl", "get", "pod", podName,
		"-n", namespace,
		"-o", "jsonpath={.status.phase}").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// PodName returns the pod name for external reference.
func (kb *KubectlExecBackend) PodName() string {
	return kb.podName
}

// Namespace returns the namespace for external reference.
func (kb *KubectlExecBackend) Namespace() string {
	return kb.namespace
}
