package sandboxlaunch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/profile"
)

// Runner runs argv with stdin. The ReadCloser streams stdout; Close waits for
// the command and returns its exit error with the tail of stderr.
type Runner func(ctx context.Context, stdin io.Reader, argv ...string) (io.ReadCloser, error)

const maxStderr = 4 << 10

// closeGrace is how long Close lets a command whose output was not read to
// the end finish on its own before it is killed.
var closeGrace = 3 * time.Second

// closeDrain bounds the waits after a kill: for the output to end and, as
// cmd.WaitDelay, for Wait's pipe copies. Close returns within
// closeGrace + 2*closeDrain.
var closeDrain = time.Second

// WorkWrapper runs inside the sandbox. Stdin line 1 is the MCP config JSON
// (it holds the bearer token), the rest is the prompt. The config goes to a
// 0600 temp file that replaces the placeholder argument, so the token is on no
// command line, and the file is removed when claude exits.
const WorkWrapper = `umask 077
f=$(mktemp) || exit 1
p=
trap 'rm -f "$f"' EXIT
trap 'kill $p 2>/dev/null; rm -f "$f"; exit 129' HUP
trap 'kill $p 2>/dev/null; rm -f "$f"; exit 130' INT
trap 'kill $p 2>/dev/null; rm -f "$f"; exit 143' TERM
IFS= read -r c || exit 1
printf '%s' "$c" > "$f"
for a; do shift; [ "$a" = ` + harnessconfig.MCPConfigPlaceholder + ` ] && a=$f; set -- "$@" "$a"; done
exec 3<&0
"$@" <&3 3<&- &
p=$!
wait $p
exit $?`

var (
	envNameRE  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	envValueRE = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)
)

// envArgs is the `env NAME=VALUE...` prefix (sorted by name) that sets env
// for the command alone: the wrapper shell never has it, so a value such as
// IFS cannot change how the wrapper runs. Names and values must match
// envNameRE and envValueRE. No env is no prefix.
func envArgs(env map[string]string) ([]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	pre := []string{"env"}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if !envNameRE.MatchString(k) {
			return nil, fmt.Errorf("invalid environment variable name %q", k)
		}
		if !envValueRE.MatchString(env[k]) {
			return nil, fmt.Errorf("invalid value for environment variable %s: only letters, digits, '.', '_' and '-' are allowed", k)
		}
		pre = append(pre, k+"="+env[k])
	}
	return pre, nil
}

// ExecRunner runs commands on the host with os/exec; children inherit this
// process's environment.
func ExecRunner() Runner { return execRunner(nil) }

// minimalEnvKeys is what a host openshell child of the tool gateway worker
// gets from the worker's environment: where to find binaries and config,
// and which gateway to use. Never a credential (OPENSHELL_OIDC_CLIENT_SECRET
// stays out).
var minimalEnvKeys = []string{"PATH", "HOME", "LANG", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
	"OPENSHELL_GATEWAY", "OPENSHELL_GATEWAY_INSECURE", "OPENSHELL_LOCAL_TLS_DIR"}

// MinimalEnvRunner is ExecRunner whose children get only minimalEnvKeys
// from environ (os.Environ()), so the tool gateway worker's runner sandbox
// calls never carry the shell's other variables, cloud credentials
// included.
func MinimalEnvRunner(environ []string) Runner {
	env := []string{}
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(minimalEnvKeys, k) {
			env = append(env, kv)
		}
	}
	return execRunner(env)
}

// execRunner runs commands with env as their environment (nil: inherit).
func execRunner(env []string) Runner {
	return func(ctx context.Context, stdin io.Reader, argv ...string) (io.ReadCloser, error) {
		if len(argv) == 0 {
			return nil, errors.New("empty command")
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: argv[0] is the fixed openshell binary from cliBackend; sandbox names are validated and no shell is involved
		cmd.Env = env
		cmd.Stdin = stdin
		setProcessGroup(cmd)
		cmd.Cancel = func() error { return killProcessGroup(cmd) }
		cmd.WaitDelay = closeDrain
		p := &proc{cmd: cmd, name: strings.Join(argv[:min(len(argv), 4)], " ")}
		cmd.Stderr = &p.stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		p.out = out
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
		return p, nil
	}
}

type proc struct {
	cmd    *exec.Cmd
	name   string
	out    io.ReadCloser
	stderr tailBuffer
	once   sync.Once
	err    error
	mu     sync.Mutex
	eof    bool
}

func (p *proc) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err == io.EOF {
		p.mu.Lock()
		p.eof = true
		p.mu.Unlock()
	}
	return n, err
}

// Close waits for the command. If its output was not read to the end, the
// rest is discarded for up to closeGrace, then the command's whole process
// group is killed.
func (p *proc) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		early := !p.eof
		p.mu.Unlock()
		killed := false
		if early {
			drained := make(chan struct{})
			go func() { _, _ = io.Copy(io.Discard, p.out); close(drained) }()
			select {
			case <-drained:
			case <-time.After(closeGrace):
				_ = killProcessGroup(p.cmd)
				_ = p.out.Close() // unblocks the drain even if a writer survived
				killed = true
				select {
				case <-drained:
				case <-time.After(closeDrain):
				}
			}
		}
		err := p.cmd.Wait()
		if err != nil && !killed { // a kill is the caller's choice, not a failure
			code := -1
			var x *exec.ExitError
			if errors.As(err, &x) {
				code = x.ExitCode() // -1 when a signal ended it
			}
			p.err = &CommandError{Name: p.name, Err: err, Code: code, Stderr: strings.TrimSpace(p.stderr.String())}
		}
	})
	return p.err
}

// CommandError is a command that failed, as a Runner's Close reports it.
type CommandError struct {
	Name   string // the first words of the command line
	Err    error  // what Wait returned
	Code   int    // the exit status; -1 when the command did not exit on its own
	Stderr string // the tail of stderr, trimmed
}

func (e *CommandError) Error() string { return fmt.Sprintf("%s: %v: %s", e.Name, e.Err, e.Stderr) }

func (e *CommandError) Unwrap() error { return e.Err }

// tailBuffer keeps the last maxStderr bytes written to it.
type tailBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b.Write(b)
	if extra := t.b.Len() - maxStderr; extra > 0 {
		t.b.Next(extra)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.b.String()
}

type cliBackend struct {
	run        Runner
	dir        string
	gateway    string
	pollEvery  time.Duration
	readyPolls int
}

var (
	// terminalPhases end the Ready wait: the sandbox will not become Ready.
	terminalPhases = map[string]bool{"Error": true, "Failed": true, "Stopped": true, "Completed": true}
)

// NewCLIBackend drives the openshell CLI. policyDir holds each sandbox's
// compiled policy file and the digest it was last created or updated with.
func NewCLIBackend(run Runner, policyDir string) Backend {
	return &cliBackend{run: run, dir: policyDir, gateway: "local", pollEvery: 2 * time.Second, readyPolls: 150}
}

func (b *cliBackend) openshell(args ...string) []string {
	return append([]string{"openshell", "-g", b.gateway}, args...)
}

// output runs an openshell command to completion and returns its stdout.
func (b *cliBackend) output(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	rc, err := b.run(ctx, stdin, b.openshell(args...)...)
	if err != nil {
		return "", err
	}
	out, rerr := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		return string(out), cerr
	}
	return string(out), rerr
}

// phase returns the sandbox's phase from `sandbox list`.
func (b *cliBackend) phase(ctx context.Context, name string) (string, bool, error) {
	out, err := b.output(ctx, nil, "sandbox", "list")
	if err != nil {
		return "", false, fmt.Errorf("list sandboxes: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == name {
			return f[len(f)-1], true, nil
		}
	}
	return "", false, nil
}

func (b *cliBackend) Ensure(ctx context.Context, c profile.Compiled) error {
	if !profile.ValidSandboxName(c.Sandbox) {
		return fmt.Errorf("invalid sandbox name %q", c.Sandbox)
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	policy := filepath.Join(b.dir, c.Sandbox+".policy.yaml")
	digestFile := filepath.Join(b.dir, c.Sandbox+".digest")
	if err := writeFile0600(policy, c.PolicyYAML); err != nil {
		return err
	}
	_, exists, err := b.phase(ctx, c.Sandbox)
	if err != nil {
		return err
	}
	providersFile := filepath.Join(b.dir, c.Sandbox+".providers")
	if !exists {
		args := []string{"sandbox", "create", "--name", c.Sandbox, "--from", c.Image, "--policy", policy}
		for _, p := range c.Providers {
			args = append(args, "--provider", p)
		}
		args = append(args, "--no-auto-providers", "--no-tty", "--detach", "--", "sleep", "infinity")
		if _, err := b.output(ctx, nil, args...); err != nil {
			return fmt.Errorf("create sandbox %s: %w", c.Sandbox, err)
		}
		if err := writeProviders(providersFile, c.Providers); err != nil {
			return err
		}
	}
	if err := b.waitReady(ctx, c.Sandbox); err != nil {
		return err
	}
	if exists {
		if recorded, err := os.ReadFile(digestFile); err == nil && string(recorded) == c.PolicyDigest { //nolint:gosec // G304: digestFile is under the backend's policyDir, named from a validated sandbox name
			return nil
		}
		set := func() error {
			_, err := b.output(ctx, nil, "policy", "set", "--policy", policy, "--yes", "--wait", c.Sandbox)
			return err
		}
		err := set()
		if isConflict(err) { // another writer won the race; one retry on the new version
			err = set()
		}
		if err != nil {
			return fmt.Errorf("update policy of %s: %w", c.Sandbox, err)
		}
		if err := b.syncProviders(ctx, c.Sandbox, providersFile, c.Providers); err != nil {
			return err
		}
	}
	return writeFile0600(digestFile, []byte(c.PolicyDigest))
}

// syncProviders attaches the providers want adds to the ones recorded in
// file and detaches the ones it drops, recording the list after each
// change. With no recorded list the sandbox is taken to have want (what
// Ensure created it with before lists were recorded).
func (b *cliBackend) syncProviders(ctx context.Context, sandbox, file string, want []string) error {
	data, err := os.ReadFile(file) //nolint:gosec // G304: file is under the backend's policyDir, named from a validated sandbox name
	if errors.Is(err, os.ErrNotExist) {
		return writeProviders(file, want)
	}
	if err != nil {
		return err
	}
	have := strings.Fields(string(data))
	for _, p := range want {
		if slices.Contains(have, p) {
			continue
		}
		if _, err := b.output(ctx, nil, "sandbox", "provider", "attach", "--wait", sandbox, p); err != nil {
			return fmt.Errorf("attach provider %s to %s: %w", p, sandbox, err)
		}
		have = append(have, p)
		if err := writeProviders(file, have); err != nil {
			return err
		}
	}
	for _, p := range slices.Clone(have) {
		if slices.Contains(want, p) {
			continue
		}
		if _, err := b.output(ctx, nil, "sandbox", "provider", "detach", "--wait", sandbox, p); err != nil {
			return fmt.Errorf("detach provider %s from %s: %w", p, sandbox, err)
		}
		have = slices.DeleteFunc(have, func(h string) bool { return h == p })
		if err := writeProviders(file, have); err != nil {
			return err
		}
	}
	return nil
}

// writeProviders records a sandbox's providers, sorted, one per line.
func writeProviders(file string, providers []string) error {
	var sb strings.Builder
	for _, p := range slices.Sorted(slices.Values(providers)) {
		sb.WriteString(p + "\n")
	}
	return writeFile0600(file, []byte(sb.String()))
}

// isConflict reports the gateway's optimistic-concurrency refusal as the CLI
// prints it.
func isConflict(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "Conflict:") || strings.Contains(err.Error(), "concurrent modification"))
}

func (b *cliBackend) waitReady(ctx context.Context, name string) error {
	last := ""
	for i := 0; i < b.readyPolls; i++ {
		p, ok, err := b.phase(ctx, name)
		if err != nil {
			return err
		}
		last = p
		if ok && p == "Ready" {
			return nil
		}
		if ok && terminalPhases[p] {
			return fmt.Errorf("sandbox %s is in phase %s; see `openshell -g %s sandbox get %s`", name, p, b.gateway, name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(b.pollEvery):
		}
	}
	return fmt.Errorf("sandbox %s not Ready (last phase %q)", name, last)
}

// writeFile0600 replaces path atomically with a 0600 file.
func writeFile0600(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // the earlier error is the one to report
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close() // the earlier error is the one to report
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Exec passes mcpConfig on stdin line 1 to WorkWrapper, which writes it to a
// 0600 file in the sandbox, swaps it in for the placeholder and removes it
// when the command exits. The rest of stdin is the prompt (plain text, or for
// a turn with an image one stream-json user message line), which the
// wrapper hands to the command as its stdin unchanged. env goes to the command alone, through
// an `env` prefix (envArgs).
func (b *cliBackend) Exec(ctx context.Context, c profile.Compiled, mcpConfig, prompt string, env map[string]string, args []string) (io.ReadCloser, error) {
	if !profile.ValidSandboxName(c.Sandbox) {
		return nil, fmt.Errorf("invalid sandbox name %q", c.Sandbox)
	}
	if strings.ContainsAny(mcpConfig, "\r\n") {
		return nil, errors.New("MCP config must be one line")
	}
	if !slices.Contains(args, harnessconfig.MCPConfigPlaceholder) {
		return nil, fmt.Errorf("args have no %s placeholder for the MCP config", harnessconfig.MCPConfigPlaceholder)
	}
	pre, err := envArgs(env)
	if err != nil {
		return nil, err
	}
	argv := b.openshell("sandbox", "exec", "--no-login-shell", "--no-tty", c.Sandbox, "--", "sh", "-c", WorkWrapper, "sh")
	argv = append(append(argv, pre...), args...)
	return b.run(ctx, strings.NewReader(mcpConfig+"\n"+prompt), argv...)
}

const (
	// runToolTimeout bounds a tool command whose context has no deadline.
	runToolTimeout = 30 * time.Second
	// maxToolOutput is how much of a tool's stdout RunTool keeps; the
	// caller cuts it shorter.
	maxToolOutput = 1 << 20
)

// toolEnv is set for every tool command: the runner's filesystem is
// read-only except /tmp, and gh writes its config and cache. It holds no
// secrets: credentials reach the sandbox only through its providers.
var toolEnv = []string{"GH_CONFIG_DIR=/tmp/gh", "XDG_CACHE_HOME=/tmp/cache"}

// RunTool runs argv (an absolute binary and its arguments, never a shell)
// in sandbox with `sandbox exec --no-login-shell --timeout`, the timeout
// taken from ctx's deadline (runToolTimeout without one). A command that
// exits returns its stdout (at most maxToolOutput), the tail of its stderr
// and its exit status with a nil error; err is for a command that could
// not run or did not exit on its own.
func (b *cliBackend) RunTool(ctx context.Context, sandbox string, argv []string) (stdout, stderr []byte, exit int, err error) {
	if !profile.ValidSandboxName(sandbox) {
		return nil, nil, 0, fmt.Errorf("invalid sandbox name %q", sandbox)
	}
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, nil, 0, errors.New("a tool command starts with an absolute binary path")
	}
	timeout := runToolTimeout
	if d, ok := ctx.Deadline(); ok {
		timeout = max(time.Until(d), time.Second)
	}
	secs := int64((timeout + time.Second - 1) / time.Second)
	args := []string{"sandbox", "exec", "--no-login-shell", "--no-tty", "--timeout", strconv.FormatInt(secs, 10)}
	for _, e := range toolEnv {
		args = append(args, "--env", e)
	}
	args = append(append(args, sandbox, "--"), argv...)
	rc, err := b.run(ctx, nil, b.openshell(args...)...)
	if err != nil {
		return nil, nil, 0, err
	}
	out, rerr := io.ReadAll(io.LimitReader(rc, maxToolOutput))
	if rerr == nil {
		// Drain the rest so the command exits on its own and keeps its exit
		// status; --timeout bounds one that never stops writing.
		_, rerr = io.Copy(io.Discard, rc)
	}
	cerr := rc.Close()
	if ctx.Err() != nil {
		return nil, nil, 0, ctx.Err()
	}
	if cerr == nil && rerr == nil {
		return out, nil, 0, nil
	}
	// openshell exits 1 for a sandbox that is gone or not Ready, as a tool
	// may: a failure on a runner that is not Ready is the runner's.
	if phase, ok, perr := b.phase(ctx, sandbox); perr == nil && (!ok || phase != "Ready") {
		return nil, nil, 0, fmt.Errorf("%s (phase %q): %w", sandbox, phase, mcpproxy.ErrRunnerNotReady)
	}
	var ce *CommandError
	if errors.As(cerr, &ce) && ce.Code >= 0 {
		return out, []byte(ce.Stderr), ce.Code, nil
	}
	if cerr != nil {
		return nil, nil, 0, cerr
	}
	return nil, nil, 0, rerr
}

// Download reads remotePath with `cat` in the sandbox; localPath is written
// only when the whole read succeeded.
func (b *cliBackend) Download(ctx context.Context, sandbox, remotePath, localPath string) error {
	if !profile.ValidSandboxName(sandbox) {
		return fmt.Errorf("invalid sandbox name %q", sandbox)
	}
	if !filepath.IsAbs(remotePath) || strings.ContainsAny(remotePath, "\r\n") {
		return fmt.Errorf("remote path %q must be absolute", remotePath)
	}
	out, err := b.output(ctx, nil, "sandbox", "exec", "--no-login-shell", "--no-tty", sandbox, "--", "cat", "--", remotePath)
	if err != nil {
		return fmt.Errorf("download %s from %s: %w", remotePath, sandbox, err)
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		return err
	}
	return writeFile0600(localPath, []byte(out))
}
