package sdk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// SubprocessManager manages the lifecycle and communication with an SDK
// subprocess. Communication uses JSON lines over stdin (send) and stdout (receive).
type SubprocessManager struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	stderr bytes.Buffer
	done   chan error
}

// StartSubprocess launches the binary with the given args, wiring stdin/stdout
// for JSON-line communication and capturing stderr. The provided context
// controls process lifetime: cancelling it sends SIGKILL.
func StartSubprocess(ctx context.Context, binary string, args ...string) (*SubprocessManager, error) {
	cmd := exec.CommandContext(ctx, binary, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sdk: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("sdk: stdout pipe: %w", err)
	}

	s := &SubprocessManager{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewScanner(stdout),
		done:   make(chan error, 1),
	}
	cmd.Stderr = &s.stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("sdk: start %s: %w", binary, err)
	}

	go func() {
		s.done <- cmd.Wait()
	}()

	return s, nil
}

// Send marshals the event as JSON and writes it as a single line to the
// subprocess stdin.
func (s *SubprocessManager) Send(event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("sdk: marshal event: %w", err)
	}
	data = append(data, '\n')
	_, err = s.stdin.Write(data)
	if err != nil {
		return fmt.Errorf("sdk: write to stdin: %w", err)
	}
	return nil
}

// Receive reads one JSON line from the subprocess stdout and unmarshals it.
// Returns io.EOF when the subprocess closes stdout.
func (s *SubprocessManager) Receive() (Event, error) {
	if !s.stdout.Scan() {
		if err := s.stdout.Err(); err != nil {
			return Event{}, fmt.Errorf("sdk: read stdout: %w", err)
		}
		return Event{}, io.EOF
	}
	var event Event
	if err := json.Unmarshal(s.stdout.Bytes(), &event); err != nil {
		return Event{}, fmt.Errorf("sdk: unmarshal event: %w (line: %s)", err, s.stdout.Text())
	}
	return event, nil
}

// Stop gracefully shuts down the subprocess. It closes stdin, sends SIGTERM,
// waits up to 3 seconds, then sends SIGKILL if the process hasn't exited.
// A clean SIGTERM exit is not treated as an error.
func (s *SubprocessManager) Stop() error {
	s.stdin.Close()

	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case err := <-s.done:
		return ignoreSignalExit(err)
	case <-time.After(3 * time.Second):
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		return ignoreSignalExit(<-s.done)
	}
}

// Wait blocks until the subprocess exits and returns its exit error.
func (s *SubprocessManager) Wait() error {
	return <-s.done
}

// Stderr returns the captured stderr output from the subprocess.
func (s *SubprocessManager) Stderr() string {
	return s.stderr.String()
}

// ignoreSignalExit returns nil for process exits caused by signals
// (SIGTERM, SIGKILL), since Stop() intentionally sends these.
func ignoreSignalExit(err error) error {
	if err == nil {
		return nil
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return err
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if ok && status.Signaled() {
		return nil
	}
	return err
}
