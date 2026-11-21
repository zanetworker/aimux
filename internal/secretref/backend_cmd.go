package secretref

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// maxCmdOutput bounds a cmd reference's standard output.
const maxCmdOutput = 64 << 10

// cmd runs ref without a shell in its own process group, killed whole at
// the timeout. Only stdout is read (at most 64 KiB); stderr is discarded and
// errors carry only the program path and exit status. A success is cached
// for ref.TTL (none by default); a failure for 30 s.
func (r *Resolver) cmd(ctx context.Context, ref CmdRef) (string, error) {
	// Results are cached per command policy: the same argv with another
	// timeout or ttl is its own entry.
	key := fmt.Sprintf("%s\x00%s\x00%s", strings.Join(ref.Argv, "\x00"), ref.Timeout, ref.TTL)
	r.mu.Lock()
	// A reference without a ttl resolves at each call; it still honours a
	// cached failure, so a broken command is not run in a loop.
	if c, ok := r.cache[key]; ok && r.opt.Now().Before(c.until) && (ref.TTL > 0 || c.err != nil) {
		r.mu.Unlock()
		return c.value, c.err
	}
	r.mu.Unlock()

	v, err := runCmd(ctx, ref)
	if ctx.Err() != nil {
		// The caller gave up (cancelled, or its own deadline): not the
		// command's failure, so nothing is cached.
		return "", fmt.Errorf("cmd %s: %w", ref.Argv[0], ctx.Err())
	}
	done := r.opt.Now() // expiry counts from when the result is known
	res := cmdResult{value: v, err: err}
	switch {
	case err != nil:
		res.until = done.Add(failureCacheFor)
	case ref.TTL > 0:
		res.until = done.Add(ref.TTL)
	}
	r.mu.Lock()
	if res.until.IsZero() {
		delete(r.cache, key)
	} else {
		r.cache[key] = res
	}
	r.mu.Unlock()
	return v, err
}

func runCmd(ctx context.Context, ref CmdRef) (string, error) {
	timeout := ref.Timeout
	if timeout <= 0 {
		timeout = defaultCmdTimeout
	}
	prog := ref.Argv[0]
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, prog, ref.Argv[1:]...) //nolint:gosec // G204: the owner's configured secret command, no shell
	c.Stdin = nil
	c.Stderr = io.Discard
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	c.WaitDelay = time.Second
	var out bytes.Buffer
	c.Stdout = &limitWriter{w: &out, n: maxCmdOutput + 1}
	err := c.Run()
	if c.Process != nil {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) // anything the program left behind in its group
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("cmd %s: timed out after %s", prog, timeout)
	}
	if out.Len() > maxCmdOutput {
		return "", fmt.Errorf("cmd %s: output over 64 KiB", prog)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return "", fmt.Errorf("cmd %s: exit status %d", prog, ee.ExitCode())
	}
	if err != nil {
		return "", fmt.Errorf("cmd %s: could not run it", prog)
	}
	v := strings.TrimRight(out.String(), "\r\n")
	if v == "" {
		return "", fmt.Errorf("cmd %s: printed nothing", prog)
	}
	return v, nil
}

// limitWriter keeps at most n bytes and reports success for the rest, so a
// chatty program is not killed by a write error before the cap is checked.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	k := len(p)
	if k > l.n {
		k = l.n
	}
	_, _ = l.w.Write(p[:k])
	l.n -= k
	return len(p), nil
}
