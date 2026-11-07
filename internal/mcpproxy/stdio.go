package mcpproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// ServerConfig is how to launch one stdio MCP server, as in ~/.claude.json.
type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// StdioStarter returns a Starter that launches servers from cfgs, expanding
// ${VAR} references as Claude Code does, except for the servers in
// resolved: install already expanded those, and a value may legitimately
// contain "${...}".
func StdioStarter(cfgs map[string]ServerConfig, resolved ...string) Starter {
	done := map[string]bool{}
	for _, r := range resolved {
		done[r] = true
	}
	return func(ctx context.Context, server string) (Backend, error) {
		cfg, ok := cfgs[server]
		if !ok {
			return nil, fmt.Errorf("no MCP server named %q is configured", server)
		}
		b, err := startStdio(ctx, server, cfg, !done[server])
		if err != nil {
			return nil, err // not a nil *stdioBackend inside a non-nil Backend
		}
		return b, nil
	}
}

// exitPoll is how often, in nanoseconds, a server whose stderr closed is
// checked for having exited. Atomic, as tests change it while other tests'
// watchers may still run.
var exitPoll atomic.Int64

// exitPollRunning is how often, in nanoseconds, a server whose stderr is
// still open is checked for having exited: a wrapper (npx, uv) can exit
// while a child it started keeps stderr open.
var exitPollRunning atomic.Int64

func init() {
	exitPoll.Store(int64(2 * time.Second))
	exitPollRunning.Store(int64(10 * time.Second))
}

// stderrDrainWait bounds the wait for a failed server's last stderr lines.
const stderrDrainWait = 2 * time.Second

type stdioBackend struct {
	server string
	c      *client.Client
	cmd    *exec.Cmd
	listID atomic.Int64
	stderr stderrRing
	scrub  *scrubber

	// done is closed when the server process is gone (awaitExit) or Close
	// returns; nil without a process.
	done     chan struct{}
	doneOnce sync.Once

	closeOnce sync.Once
	closeErr  error

	def     ServerConfig // the definition it was started with, before expansion
	noTools bool         // its initialize result has no tools capability
}

// lookPathIn finds an executable named name in the directories of path (a
// PATH value). A name with a slash is used as it is, if executable.
func lookPathIn(name, path string) (string, error) {
	if name == "" {
		return "", errors.New("command cannot be empty")
	}
	if strings.Contains(name, "/") {
		if isExecutable(name) {
			return name, nil
		}
		return "", fmt.Errorf("command %q is not an executable file", name)
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, name); isExecutable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("command %q is not on the proxy's PATH (%s)", name, path)
}

func isExecutable(path string) bool {
	st, err := os.Stat(path) // #nosec G703 -- a command path from the owner's own MCP config
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

// envValue is key's value in env (KEY=value entries); "" when absent.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// Definition is the definition the backend was started with, so a catalog
// refresh can tell whether that definition was replaced meanwhile.
func (b *stdioBackend) Definition() ServerConfig { return b.def }

func startStdio(ctx context.Context, server string, cfg ServerConfig, expand bool) (*stdioBackend, error) {
	return startStdioIn(ctx, server, cfg, expand, nil)
}

// startStdioIn is startStdio with the child's base environment: nil
// inherits this process's, otherwise exactly base (KEY=value) plus cfg.Env.
func startStdioIn(ctx context.Context, server string, cfg ServerConfig, expand bool, base []string) (*stdioBackend, error) {
	def := cfg
	raw := cfg.Env
	if expand {
		cfg = expandConfig(cfg)
	}
	// The child inherits this process's environment too, so its secrets are scrubbed as well.
	if base != nil {
		// exec would resolve a bare command on this process's PATH: resolve
		// it on the base PATH, the one the server runs with.
		resolved, err := lookPathIn(cfg.Command, envValue(base, "PATH"))
		if err != nil {
			return nil, fmt.Errorf("start MCP server %s: %w", server, err)
		}
		cfg.Command = resolved
	}
	b := &stdioBackend{server: server, def: def, scrub: newScrubber(raw, cfg.Env, secretEnv(os.Environ()))}
	b.stderr.scrub = b.scrub.scrub
	env := make([]string, 0, len(cfg.Env))
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	// Run each server in its own session (so its own process group) so Close
	// also stops the real server when the command is a wrapper such as npx or
	// uv, and so it cannot read from the user's terminal.
	cmdFunc := func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, command, args...) // #nosec G204 -- command comes from the user's MCP config
		if base != nil {
			cmd.Env = append(append([]string{}, base...), env...)
		} else {
			cmd.Env = append(cmd.Environ(), env...)
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // own session: no controlling terminal
		b.cmd = cmd
		return cmd, nil
	}
	c, err := client.NewStdioMCPClientWithOptions(cfg.Command, env, cfg.Args, transport.WithCommandFunc(cmdFunc))
	if err != nil {
		return nil, b.failure(fmt.Errorf("start MCP server %s (%s): %w", server, cfg.Command, err))
	}
	b.c = c
	// Read the server's stderr: the client opens the pipe but never reads it,
	// and a server that logs more than the pipe holds would block and hang.
	drained := make(chan struct{})
	if stderr, ok := client.GetStderr(c); ok {
		go func() {
			b.stderr.drain(stderr)
			close(drained)
		}()
	} else {
		close(drained)
	}
	if b.PID() != 0 {
		b.done = make(chan struct{})
		go b.awaitExit(drained)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "aimux-mcp-proxy", Version: "1"}
	res, err := c.Initialize(ctx, init)
	if err == nil {
		b.noTools = res.Capabilities.Tools == nil
	}
	if err != nil {
		// The server's last stderr lines usually say why. Close closes the
		// pipe's read end, losing what is still in it, so let stderr end on
		// its own first (briefly); not when the start already timed out.
		if ctx.Err() == nil {
			select {
			case <-drained:
			case <-time.After(stderrDrainWait):
			}
		}
		_ = b.Close()
		return nil, b.failure(fmt.Errorf("initialize MCP server %s: %w", server, err))
	}
	return b, nil
}

// ListToolsRaw pages through tools/list and returns each tool's JSON as sent.
func (b *stdioBackend) ListToolsRaw(ctx context.Context) ([]json.RawMessage, error) {
	var tools []json.RawMessage
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		// String IDs cannot collide with the client's own numeric IDs.
		id := mcp.NewRequestId(fmt.Sprintf("aimux-list-%d", b.listID.Add(1)))
		resp, err := b.c.GetTransport().SendRequest(ctx, transport.JSONRPCRequest{
			JSONRPC: mcp.JSONRPC_VERSION, ID: id, Method: string(mcp.MethodToolsList), Params: params,
		})
		if err != nil {
			return nil, b.scrubbed(fmt.Errorf("list tools of %s: %w", b.server, err))
		}
		if resp.Error != nil {
			// The server answered: it is alive, whatever the error says.
			return nil, &RequestError{Code: resp.Error.Code, Err: b.scrubbed(fmt.Errorf("list tools of %s: %s", b.server, resp.Error.Message))}
		}
		var page struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(resp.Result, &page); err != nil {
			// An answer it could not parse; the transport is fine.
			return nil, &RequestError{Err: b.scrubbed(fmt.Errorf("list tools of %s: %w", b.server, err))}
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			return tools, nil
		}
		cursor = page.NextCursor
	}
}

func (b *stdioBackend) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	res, err := b.c.CallTool(ctx, req)
	var terr *transport.Error
	if err != nil && !errors.As(err, &terr) {
		// The server answered with a JSON-RPC error: it is alive and well.
		return nil, &RequestError{Err: err}
	}
	if err != nil {
		return nil, b.scrubbed(err)
	}
	return res, nil
}

// scrubbed is err with secrets replaced in its message.
func (b *stdioBackend) scrubbed(err error) error {
	return &scrubbedError{msg: b.scrub.scrub(err.Error()), err: err}
}

// Done is closed once the server process is gone: it exited on its own or
// Close stopped it. Nil when no process was started.
func (b *stdioBackend) Done() <-chan struct{} {
	if b.done == nil {
		return nil
	}
	return b.done
}

// awaitExit watches the server process for as long as it runs. mcp-go
// v0.57.0's stdio transport exposes neither stdout EOF nor a
// connection-lost hook, so the process itself is checked (ps, never a
// second Wait): every exitPollRunning while stderr is open, since a wrapper
// such as npx or uv can exit while a child keeps stderr open, and every
// exitPoll once stderr has ended (drained), which a server may also do by
// closing stderr and serving on. Done is closed once the process is gone,
// or when Close runs. The client reaps the process only in Close, so an
// exited server stays a zombie until then and its pid cannot be reused
// while this watches it.
func (b *stdioBackend) awaitExit(drained <-chan struct{}) {
	pid := b.PID()
	for {
		if pid == 0 || processGone(pid) {
			b.markDone()
			return
		}
		every := time.Duration(exitPollRunning.Load())
		if drained == nil {
			every = time.Duration(exitPoll.Load())
		}
		select {
		case <-b.done: // Close ran
			return
		case <-drained: // stderr ended: check now, then poll faster
			drained = nil
		case <-time.After(every):
		}
	}
}

// processGone reports whether pid has exited: ps finds no such process, or
// only its zombie. If ps cannot run, the process counts as alive.
func processGone(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output() // #nosec G204 -- fixed command, numeric pid
	stat := strings.TrimSpace(string(out))
	if err != nil {
		var exitErr *exec.ExitError
		return errors.As(err, &exitErr) && stat == "" // ps ran and found no such process
	}
	return stat == "" || strings.HasPrefix(stat, "Z")
}

func (b *stdioBackend) markDone() {
	if b.done != nil {
		b.doneOnce.Do(func() { close(b.done) })
	}
}

// failure is a failed start's error, scrubbed, with the stderr tail.
func (b *stdioBackend) failure(err error) error {
	return &scrubbedError{msg: b.scrub.scrub(err.Error()), err: err, tail: b.stderr.Lines()}
}

// Close shuts the client down, then kills anything left in the server's
// process group. Only the first call does anything: a later one could kill
// a new process group that reused the id.
func (b *stdioBackend) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.c.Close()
		if b.cmd != nil && b.cmd.Process != nil {
			_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL) // group may already be gone
		}
		// Release Done's watchers even if a grandchild still holds stderr.
		b.markDone()
	})
	return b.closeErr
}

// stderrLineMax is how much of one stderr line is kept.
const stderrLineMax = 512

// stderrScanMax is how much of a line is read before scrubbing; the line is
// cut to stderrLineMax only after, so a secret across the cut is redacted whole.
const stderrScanMax = 4096

// stderrRing keeps the last stderrTailLines non-blank lines of a server's
// stderr in memory, each cut to stderrLineMax bytes and scrubbed of
// secrets. It is never logged or written anywhere else.
type stderrRing struct {
	scrub func(string) string // nil keeps lines as they are
	mu    sync.Mutex
	lines []string
}

// drain reads rd to the end, keeping the last lines. It reads everything,
// however long a line is, so the server never blocks on a full pipe.
func (r *stderrRing) drain(rd io.Reader) {
	br := bufio.NewReaderSize(rd, stderrScanMax)
	var cur []byte
	for {
		chunk, err := br.ReadSlice('\n')
		full := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		if full {
			chunk = chunk[:len(chunk)-1]
		}
		if room := stderrScanMax - len(cur); room > 0 {
			cur = append(cur, chunk[:min(room, len(chunk))]...)
		}
		if full {
			r.add(cur)
			cur = cur[:0]
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			r.add(cur)
			return
		}
	}
}

func (r *stderrRing) add(line []byte) {
	text := strings.TrimRight(string(line), "\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	if r.scrub != nil {
		text = r.scrub(text)
	}
	if len(text) > stderrLineMax {
		text = strings.ToValidUTF8(text[:stderrLineMax], "")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, text)
	if len(r.lines) > stderrTailLines {
		r.lines = append([]string(nil), r.lines[len(r.lines)-stderrTailLines:]...)
	}
}

// Lines returns the kept lines, oldest first.
func (r *stderrRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// scrubbedError is a backend error whose message has secrets replaced. A
// failed start also carries the last lines the server wrote to stderr,
// kept apart from the message so they are shown only on request.
type scrubbedError struct {
	msg  string
	err  error
	tail []string
}

func (e *scrubbedError) Error() string        { return e.msg }
func (e *scrubbedError) Unwrap() error        { return e.err }
func (e *scrubbedError) StderrTail() []string { return e.tail }

// PID is the server process's id, 0 if it never started.
func (b *stdioBackend) PID() int {
	if b.cmd == nil || b.cmd.Process == nil {
		return 0
	}
	return b.cmd.Process.Pid
}

// StderrTail is the last lines the server wrote to stderr.
func (b *stdioBackend) StderrTail() []string { return b.stderr.Lines() }
