package mcpproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/errcode"
	"github.com/zanetworker/aimux/internal/secretref"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// Managed server states.
const (
	ManagedStopped   = "stopped"
	ManagedStarting  = "starting"
	ManagedReady     = "ready"
	ManagedBackoff   = "backoff"
	ManagedCrashLoop = "crash_loop"
	ManagedPortInUse = "port_in_use"
	ManagedAdopted   = "adopted"
)

const (
	readyDeadline  = 30 * time.Second
	readyEvery     = 250 * time.Millisecond
	stopGrace      = 5 * time.Second
	crashWindow    = 5 * time.Minute
	crashLoopAfter = 5
	maxBackoff     = time.Minute
	logCap         = 10 << 20
)

// ManagedState is what status shows of one managed server. LastError is
// scrubbed and never holds the server's own output (its log has that).
type ManagedState struct {
	State     string
	PID       int
	Restarts  int
	LastError string
	LogPath   string
	// Identity is toolreg.Server.Identity of the definition the running
	// process was started from (empty when stopped, adopted or when it
	// cannot be computed): what ties a listing to an approved identity.
	Identity string
}

// SupervisorOptions configures a Supervisor. Zero fields get defaults:
// LogDir ~/.aimux/logs/servers, Now time.Now, After time.After, Base PATH,
// HOME, LANG and TMPDIR from this process.
type SupervisorOptions struct {
	Resolver *secretref.Resolver
	LogDir   string
	Now      func() time.Time
	After    func(time.Duration) <-chan time.Time
	// Base is the environment every child starts from; only PATH, HOME,
	// LANG and TMPDIR of it are used.
	Base map[string]string
}

// Supervisor starts and watches the managed HTTP servers (command and url
// in servers.yaml): one process group each, a readiness probe, restart
// with backoff, crash_loop after 5 crashes in 5 minutes. It never touches
// a process it did not start.
type Supervisor struct {
	opts SupervisorOptions
	mu   sync.Mutex
	srv  map[string]*managedProc
	ctx  context.Context // restarts run under it; StopAll ends it
	end  context.CancelFunc
}

type managedProc struct {
	start    sync.Mutex // one start at a time
	def      toolreg.Server
	state    ManagedState
	cmd      *exec.Cmd
	exited   chan struct{} // closed when cmd has been waited for
	stopping bool
	crashes  []time.Time
	streak   int // crashes since the last run that stayed up
	inFlight int
	log      io.Closer
	headers  map[string]string // resolved at the last start, for the pool's client
}

// NewSupervisor is a Supervisor with opts' defaults filled in.
func NewSupervisor(opts SupervisorOptions) *Supervisor {
	if opts.Resolver == nil {
		opts.Resolver = secretref.NewResolver(secretref.Options{})
	}
	if opts.LogDir == "" {
		home, _ := os.UserHomeDir()
		opts.LogDir = filepath.Join(home, ".aimux", "logs", "servers")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.After == nil {
		opts.After = time.After
	}
	if opts.Base == nil {
		opts.Base = map[string]string{}
		for _, k := range childBaseKeys {
			if v, ok := os.LookupEnv(k); ok {
				opts.Base[k] = v
			}
		}
	}
	ctx, end := context.WithCancel(context.Background())
	return &Supervisor{opts: opts, srv: map[string]*managedProc{}, ctx: ctx, end: end}
}

var childBaseKeys = []string{"PATH", "HOME", "LANG", "TMPDIR"}

// ChildEnv is a managed or stdio child's whole environment: PATH, HOME,
// LANG and TMPDIR from base, then the server's literal env, then its
// resolved secret env. Nothing else of aimux's environment is inherited.
func ChildEnv(base, literal, resolved map[string]string) []string {
	env := map[string]string{}
	for _, k := range childBaseKeys {
		if v, ok := base[k]; ok {
			env[k] = v
		}
	}
	for k, v := range literal {
		env[k] = v
	}
	for k, v := range resolved {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func (s *Supervisor) proc(name string) *managedProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.srv[name]
	if p == nil {
		p = &managedProc{state: ManagedState{State: ManagedStopped}}
		s.srv[name] = p
	}
	return p
}

// Status is every server the supervisor has been asked about.
func (s *Supervisor) Status() map[string]ManagedState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]ManagedState, len(s.srv))
	for n, p := range s.srv {
		out[n] = p.state
	}
	return out
}

func (s *Supervisor) set(p *managedProc, fn func(*ManagedState)) {
	s.mu.Lock()
	fn(&p.state)
	s.mu.Unlock()
}

// Ensure starts name if it is not running and waits until it is ready,
// returning its url. A port taken by another process fails (unless
// adopt_existing and it answers MCP initialize there); a crash_loop server
// fails until aimux servers restart.
func (s *Supervisor) Ensure(ctx context.Context, name string, def toolreg.Server) (string, error) {
	if def.Transport() != "managed-http" {
		return "", fmt.Errorf("server %s is not managed (needs both command and url)", name)
	}
	p := s.proc(name)
	p.start.Lock()
	defer p.start.Unlock()
	s.mu.Lock()
	st := p.state.State
	s.mu.Unlock()
	switch st {
	case ManagedReady, ManagedAdopted:
		return def.URL, nil
	case ManagedCrashLoop:
		return "", &errcode.Error{Code: errcode.ServerCrashLoop,
			Message: fmt.Sprintf("server %s crashed %d times in %s and is not restarted; see %s, then run aimux servers restart %s", name, crashLoopAfter, crashWindow, p.state.LogPath, name)}
	case ManagedBackoff:
		return "", fmt.Errorf("server %s is restarting after a crash; see aimux servers status", name)
	}
	return def.URL, s.start(ctx, name, p, def)
}

// start runs def once and waits for readiness. p.start is held.
func (s *Supervisor) start(ctx context.Context, name string, p *managedProc, def toolreg.Server) error {
	u, err := url.Parse(def.URL)
	if err != nil {
		return fmt.Errorf("server %s: bad url", name)
	}
	hostport := u.Host
	if u.Port() == "" {
		hostport = net.JoinHostPort(u.Hostname(), map[bool]string{true: "443", false: "80"}[strings.EqualFold(u.Scheme, "https")])
	}
	logPath := filepath.Join(s.opts.LogDir, name+".log")
	s.set(p, func(st *ManagedState) { st.LogPath, st.PID = logPath, 0 })
	vals, err := s.opts.Resolver.Resolve(ctx, name, def.Secrets) // registered with the scrubber before any start
	if err != nil {
		return s.fail(p, ManagedStopped, fmt.Errorf("server %s: %w", name, err))
	}
	if c, err := net.DialTimeout("tcp", hostport, time.Second); err == nil {
		_ = c.Close()
		if def.AdoptExisting && probeMCP(ctx, def.URL, vals.Headers) == nil {
			s.mu.Lock()
			p.def, p.headers = def, vals.Headers
			s.mu.Unlock()
			s.set(p, func(st *ManagedState) { st.State, st.LastError = ManagedAdopted, "" })
			return nil
		}
		_, port, _ := net.SplitHostPort(hostport)
		return s.fail(p, ManagedPortInUse, &errcode.Error{Code: errcode.ServerPortInUse,
			Message: fmt.Sprintf("port %s is in use (another %s started by hand?); stop it, or set adopt_existing: true", port, name)})
	}
	exe, err := lookPathIn(def.Command[0], childPath(s.opts.Base, def.Env))
	if err != nil {
		return s.fail(p, ManagedStopped, fmt.Errorf("server %s: %w", name, err))
	}
	if err := os.MkdirAll(s.opts.LogDir, 0o700); err != nil {
		return s.fail(p, ManagedStopped, fmt.Errorf("server %s: log dir: %w", name, err))
	}
	logw, err := openRotatingLog(logPath, logCap)
	if err != nil {
		return s.fail(p, ManagedStopped, fmt.Errorf("server %s: log: %w", name, err))
	}
	cmd := exec.Command(exe, def.Command[1:]...) //nolint:gosec // G204: the user's own servers.yaml
	cmd.Dir = def.Cwd
	cmd.Env = ChildEnv(s.opts.Base, def.Env, vals.Env)
	cmd.Stdout, cmd.Stderr = logw, logw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own group: stop reaches wrappers' children
	s.set(p, func(st *ManagedState) { st.State = ManagedStarting })
	if err := cmd.Start(); err != nil {
		_ = logw.Close()
		return s.fail(p, ManagedStopped, fmt.Errorf("server %s: start %s: %w", name, exe, err))
	}
	exited := make(chan struct{})
	s.mu.Lock()
	p.def, p.cmd, p.exited, p.stopping, p.log, p.headers = def, cmd, exited, false, logw, vals.Headers
	p.state.PID = cmd.Process.Pid
	p.state.Identity = ""
	s.mu.Unlock()
	if id, err := def.Identity(toolreg.HashFile); err == nil { // hashed after the start: the file that runs now
		s.set(p, func(st *ManagedState) { st.Identity = id })
	}
	started := s.opts.Now()
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		_ = logw.Close()
		close(exited)
	}()

	timeout := readyDeadline
	if def.Ready != nil && def.Ready.Timeout > 0 {
		timeout = def.Ready.Timeout
	}
	probe := func(c context.Context) error { return probeMCP(c, def.URL, vals.Headers) }
	if def.Ready != nil && def.Ready.Type == "tcp" {
		probe = func(context.Context) error { return probeTCP(hostport) }
	}
	deadline := time.Now().Add(timeout)
	var lastProbe error
	for {
		if lastProbe = probe(ctx); lastProbe == nil {
			break
		}
		select {
		case <-exited:
			s.crashed(name, p, started, fmt.Errorf("server %s exited before it was ready (%v); see %s", name, waitErr, logPath))
			s.mu.Lock()
			last := p.state.LastError
			s.mu.Unlock()
			return errors.New(last)
		case <-ctx.Done():
			lastProbe = ctx.Err()
		case <-time.After(readyEvery):
			if time.Now().Before(deadline) {
				continue
			}
		}
		s.kill(p)
		<-exited
		return s.fail(p, ManagedStopped, fmt.Errorf("server %s did not answer at %s within %s: %v; see %s", name, def.URL, timeout, lastProbe, logPath)) // fail scrubs
	}
	s.set(p, func(st *ManagedState) { st.State, st.LastError = ManagedReady, "" })
	go s.watch(name, p, cmd, exited, started, &waitErr)
	return nil
}

// fail records err (scrubbed) as name's last error in state and returns
// it scrubbed, keeping a coded error's code.
func (s *Supervisor) fail(p *managedProc, state string, err error) error {
	msg := s.opts.Resolver.Scrubber().Scrub(err.Error())
	s.set(p, func(st *ManagedState) { st.State, st.LastError, st.PID, st.Identity = state, msg, 0, "" })
	if e, ok := errcode.Of(err); ok {
		return &errcode.Error{Code: e.Code, Message: msg}
	}
	return errors.New(msg)
}

// watch waits for a ready server to exit; an exit Stop did not ask for is
// a crash.
func (s *Supervisor) watch(name string, p *managedProc, cmd *exec.Cmd, exited chan struct{}, started time.Time, waitErr *error) {
	<-exited
	s.mu.Lock()
	mine, stopping := p.cmd == cmd, p.stopping
	s.mu.Unlock()
	if !mine || stopping {
		return
	}
	s.crashed(name, p, started, fmt.Errorf("server %s exited (%v); see %s", name, *waitErr, p.state.LogPath))
}

// crashed records a crash and restarts after the backoff, or marks the
// server crash_loop at the fifth crash in five minutes.
func (s *Supervisor) crashed(name string, p *managedProc, started time.Time, why error) {
	now := s.opts.Now()
	msg := s.opts.Resolver.Scrubber().Scrub(why.Error())
	s.mu.Lock()
	if now.Sub(started) >= maxBackoff {
		p.streak = 0 // it stayed up: start the backoff over
	}
	p.streak++
	kept := p.crashes[:0]
	for _, c := range p.crashes {
		if now.Sub(c) < crashWindow {
			kept = append(kept, c)
		}
	}
	p.crashes = append(kept, now)
	p.state.PID, p.state.LastError, p.state.Identity = 0, msg, ""
	loop := len(p.crashes) >= crashLoopAfter
	if loop {
		p.state.State = ManagedCrashLoop
	} else {
		p.state.State = ManagedBackoff
	}
	delay, def := backoffDelay(p.streak), p.def
	s.mu.Unlock()
	if loop {
		return
	}
	go func() {
		select {
		case <-s.opts.After(delay):
		case <-s.ctx.Done():
			return
		}
		p.start.Lock()
		defer p.start.Unlock()
		s.mu.Lock()
		again := p.state.State == ManagedBackoff
		if again {
			p.state.Restarts++
		}
		s.mu.Unlock()
		if again {
			_ = s.start(s.ctx, name, p, def) // its failure is in status
		}
	}()
}

// backoffDelay is the wait before restart n (1-based): 1 s doubling to 60 s.
func backoffDelay(n int) time.Duration {
	if n > 7 {
		return maxBackoff
	}
	return min(time.Second<<(n-1), maxBackoff)
}

// Acquire counts a call in flight on name until release (idempotent).
func (s *Supervisor) Acquire(name string) (release func()) {
	p := s.proc(name)
	s.mu.Lock()
	p.inFlight++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			p.inFlight--
			s.mu.Unlock()
		})
	}
}

// StopIdle stops name if it runs with no call in flight and is not
// keep_running, reporting whether it did.
func (s *Supervisor) StopIdle(name string) bool {
	p := s.proc(name)
	p.start.Lock()
	defer p.start.Unlock()
	s.mu.Lock()
	idle := p.inFlight == 0 && !p.def.KeepRunning && p.state.State == ManagedReady
	s.mu.Unlock()
	if idle {
		s.stopLocked(p)
	}
	return idle
}

// Stop stops name: SIGTERM to its process group, SIGKILL after 5 s. A
// server it adopted is only forgotten, never signalled. A crash_loop is
// cleared.
func (s *Supervisor) Stop(name string) {
	p := s.proc(name)
	p.start.Lock()
	defer p.start.Unlock()
	s.stopLocked(p)
}

func (s *Supervisor) stopLocked(p *managedProc) {
	s.mu.Lock()
	p.stopping = true
	exited := p.exited
	s.mu.Unlock()
	if exited != nil {
		s.kill(p)
		<-exited
	}
	s.mu.Lock()
	p.cmd, p.exited, p.crashes, p.streak = nil, nil, nil, 0
	p.state.State, p.state.PID, p.state.Identity = ManagedStopped, 0, ""
	s.mu.Unlock()
}

// kill sends SIGTERM to p's process group, then SIGKILL after stopGrace.
func (s *Supervisor) kill(p *managedProc) {
	s.mu.Lock()
	cmd, exited := p.cmd, p.exited
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(stopGrace):
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL) // also the group's stragglers
}

// StopAll stops every server and ends pending restarts.
func (s *Supervisor) StopAll() {
	s.end()
	s.mu.Lock()
	names := make([]string, 0, len(s.srv))
	for n := range s.srv {
		names = append(names, n)
	}
	s.mu.Unlock()
	for _, n := range names {
		s.Stop(n)
	}
}

// StartKeepRunning starts every managed server marked keep_running, each
// in the background; a failure is in its status.
func (s *Supervisor) StartKeepRunning(ctx context.Context, servers map[string]toolreg.Server) {
	for name, def := range servers {
		if def.KeepRunning && def.Transport() == "managed-http" {
			s.proc(name)
			go func() { _, _ = s.Ensure(ctx, name, def) }()
		}
	}
}

// childPath is the PATH a child runs with: its literal env's, else base's.
func childPath(base, literal map[string]string) string {
	if p, ok := literal["PATH"]; ok {
		return p
	}
	return base["PATH"]
}

// probeMCP POSTs an MCP initialize to u; a 2xx answer is ready. A non-2xx
// answer is an error with its status and at most 200 bytes of its body.
func probeMCP(ctx context.Context, u string, headers map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"aimux-supervisor","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return fmt.Errorf("initialize answered %s: %s", resp.Status, bytes.TrimSpace(snippet))
}

func probeTCP(hostport string) error {
	c, err := net.DialTimeout("tcp", hostport, time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

// rotatingLog appends to path and, past limit bytes, moves it to path.1
// (replacing the older one), so two files are kept.
type rotatingLog struct {
	mu    sync.Mutex
	path  string
	limit int64
	f     *os.File
	size  int64
}

func openRotatingLog(path string, limit int64) (*rotatingLog, error) {
	l := &rotatingLog{path: path, limit: limit}
	return l, l.open()
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // G304: aimux's own log
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

func (l *rotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > 0 && l.size+int64(len(b)) > l.limit {
		_ = l.f.Close()
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			return 0, err
		}
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	return n, err
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// ManagedStarter is the pool's Starter for the managed servers in defs:
// it has the supervisor start the server (or finds it running), then
// connects to its url with the headers resolved at that start. Each open
// backend counts as in flight, so the server is stopped when the last one
// closes (the pool reaped it idle), unless keep_running.
func ManagedStarter(sup *Supervisor, defs map[string]toolreg.Server) Starter {
	return func(ctx context.Context, server string) (Backend, error) {
		def, ok := defs[server]
		if !ok {
			return nil, fmt.Errorf("no managed MCP server named %q is configured", server)
		}
		u, err := sup.Ensure(ctx, server, def)
		if err != nil {
			return nil, err
		}
		release := sup.Acquire(server)
		p := sup.proc(server)
		sup.mu.Lock()
		headers := p.headers
		sup.mu.Unlock()
		b, err := startHTTP(ctx, server, HTTPServerConfig{URL: u, Headers: headers})
		if err != nil {
			release()
			return nil, err // not a nil *httpBackend inside a non-nil Backend
		}
		return &managedBackend{httpBackend: b, sup: sup, server: server, release: release}, nil
	}
}

// managedBackend is an HTTP backend to a managed server; closing it may
// stop the server.
type managedBackend struct {
	*httpBackend
	sup     *Supervisor
	server  string
	release func()
}

func (b *managedBackend) Close() error {
	err := b.httpBackend.Close()
	b.release()
	b.sup.StopIdle(b.server)
	return err
}
