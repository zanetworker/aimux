// Package remoteapp composes `aimux remote serve`: it builds the services
// the remote API needs (provider registry, live sessions, chat manager and
// router, broker, search, terminal input, chat queue and dispatcher, namer,
// folders, Mac layout, waiting tracker, push, device login), runs their
// background jobs and shuts them down in order. cmd resolves flags and
// config into Options and calls New(cfg, opts).Run(ctx).
//
// It is the remote API's composition root, so it imports net/http (the
// listener and http.Server) and the iTerm2 driver. It must not import cmd
// or frontend packages (boundary_test.go): the phone web client's handler
// comes in through Options.Client.
package remoteapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/folders"
	"github.com/zanetworker/aimux/internal/gitremote"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/waiting"
)

// Options is what remote serve runs with once flags and config are
// resolved. Reserve, Getenv, DetectHost and Drivers default when nil; Out
// and ErrOut default to io.Discard.
type Options struct {
	Home            string
	Port            int      // on 127.0.0.1
	Allowed         []string // tailscale logins admitted
	AdapterCommand  string   // the ACP adapter sessions run on
	AdapterArgs     []string
	MaxRuns         int
	ApprovalTimeout time.Duration
	// Reserve keeps two aimux processes from continuing one session;
	// default acprun.Reservation(Home), the lock aimux chat takes too.
	Reserve func(sessionID string) (release func(), err error)
	Version string       // the API's version on health
	Client  http.Handler // the phone web client served at /app/
	Out     io.Writer    // startup lines
	ErrOut  io.Writer    // warnings and failures while running

	Getenv     func(string) string                 // default os.Getenv
	DetectHost func() (string, error)              // default: tailscale's MagicDNS name
	Drivers    func() ([]terminput.Driver, func()) // default: iTerm2 on macOS
	// OpenTab opens a pane running argv in dir for Open on Mac; default: a
	// pane in iTerm2's aimux window (windowOpener). Used only with an
	// iTerm2 driver and terminal input on.
	OpenTab func(ctx context.Context, title string, argv []string, dir string) error

	// LocalOnly serves only the local clients' sockets: no tailnet
	// listener, so no Allowed, Host names or device login.
	LocalOnly bool
	// UnsafeNoDeviceLogin runs the tailnet listener with device login off.
	UnsafeNoDeviceLogin bool
	// Scopes is the clients (clientscope.Load): what each may ask for, and
	// a listener for every client with a Socket.
	Scopes clientscope.Scopes
}

// Server is remote serve, ready to run.
type Server struct {
	cfg  config.Config
	opts Options
	// wrapJob, when set, wraps each job as build lists it (tests watch a
	// job end through it).
	wrapJob func(job) job
	// idleAfter and idleEvery: a local-only serve exits once idle for
	// idleAfter, checked every idleEvery (defaults localIdleAfter and
	// time.Minute).
	idleAfter, idleEvery time.Duration
}

// New fills the defaults of opts; nothing is opened until Run.
func New(cfg config.Config, opts Options) *Server {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.ErrOut == nil {
		opts.ErrOut = io.Discard
	}
	if opts.Reserve == nil {
		opts.Reserve = acprun.Reservation(opts.Home)
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.DetectHost == nil {
		opts.DetectHost = tailscaleDNSName
	}
	if opts.Drivers == nil {
		opts.Drivers = terminalDrivers
	}
	if opts.OpenTab == nil {
		opts.OpenTab = windowOpener().OpenTab
	}
	return &Server{cfg: cfg, opts: opts}
}

// Run builds the services, serves until ctx ends or the process gets
// SIGINT or SIGTERM, then stops: the HTTP server drains, the awaited jobs
// (chat queue, namer, Last on your Mac, folders, waiting alerts) return,
// and what was opened closes last first (listener, queue file, audit log,
// terminal input log, terminal drivers, chat manager). A build failure closes what
// was opened before it.
func (s *Server) Run(ctx context.Context) error {
	if !s.opts.LocalOnly && !s.cfg.RemoteAccess.DeviceLoginOn() && !s.opts.UnsafeNoDeviceLogin {
		return errNoDeviceLogin
	}
	if sock := s.opts.Scopes.Clients[wingmanClient].Socket; sock != "" {
		if pid, ok := socketOwner(sock); ok {
			if !s.opts.LocalOnly && ownerLocalOnly(sock) {
				return localOnlyOwnerError(pid, sock)
			}
			_, _ = fmt.Fprintf(s.opts.Out, "already running (pid %d)\n", pid)
			return nil
		}
	}
	w := &wiring{}
	defer w.close()
	if err := s.build(w); err != nil {
		return err
	}
	return w.serve(ctx, s.opts.Out)
}

// localOnlyOwnerError refuses a full serve while a local-only one (the one
// Wingman starts) holds the sockets: exiting 0 would leave the phone and
// web offline with no sign of it.
func localOnlyOwnerError(pid int, sock string) error {
	stop := fmt.Sprintf("stop it with `kill %d`", pid)
	if pid == 0 {
		stop = "stop it (find it with `pgrep -fl 'aimux remote serve --local-only'`)"
	}
	return fmt.Errorf("a local-only aimux remote serve (pid %d, started by Wingman) holds %s, so the phone and web cannot reach this Mac; %s, then run aimux remote serve again (Wingman uses the full serve too)", pid, sock, stop)
}

// errNoDeviceLogin is H3: without device login, any local process can send
// tailscale serve's identity header for an allowed login.
var errNoDeviceLogin = errors.New("device login is off: any local process could claim a tailnet login; pass --unsafe-no-device-login to run anyway")

// wingmanClient is the client whose socket tells a second remote serve that
// one is already running.
const wingmanClient = "wingman"

// job is one background loop of remote serve. Shutdown waits for an
// awaited job to return before anything closes; the others end with the
// context on their own.
type job struct {
	name  string
	await bool
	run   func(ctx context.Context)
}

// wiring is a built remote serve: its jobs, its listener and server, the
// closers of what it opened, and its startup lines.
type wiring struct {
	jobs    []job
	closers []func()     // run last first
	ln      net.Listener // the tailnet listener; nil with LocalOnly
	srv     *http.Server
	locals  []localServer // one per client socket
	banner  func(addr net.Addr) string
	exit    chan struct{} // closed: stop serving (a local-only serve gone idle); nil: never
}

// localServer serves one local client's socket.
type localServer struct {
	ln  net.Listener
	srv *http.Server
}

func (w *wiring) onClose(f func()) { w.closers = append(w.closers, f) }

// close runs the closers, the last added first.
func (w *wiring) close() {
	for i := len(w.closers) - 1; i >= 0; i-- {
		w.closers[i]()
	}
}

// startJobs starts every job in a goroutine of its own; wait returns once
// every awaited job has returned, checked in list order.
func startJobs(ctx context.Context, jobs []job) (wait func()) {
	var done []chan struct{}
	for _, j := range jobs {
		if !j.await {
			go j.run(ctx)
			continue
		}
		ch := make(chan struct{})
		done = append(done, ch)
		go func(run func(context.Context)) { defer close(ch); run(ctx) }(j.run)
	}
	return func() {
		for _, ch := range done {
			<-ch
		}
	}
}

// serve starts the jobs, prints the startup lines and serves until ctx
// ends. In-flight requests end, then the awaited jobs, before the caller
// runs the closers.
func (w *wiring) serve(ctx context.Context, out io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	wait := startJobs(ctx, w.jobs)
	// The awaited jobs stop before the closers run (the terminal drivers
	// among them).
	defer func() {
		stop()
		wait()
	}()
	all := append([]localServer(nil), w.locals...)
	if w.srv != nil {
		all = append(all, localServer{ln: w.ln, srv: w.srv})
	}
	shutDown := make(chan struct{})
	go func() { //nolint:gosec // G118: the shutdown runs after ctx ended, so it needs a context of its own
		defer close(shutDown)
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, ls := range all {
			_ = ls.srv.Shutdown(shut)
		}
	}()
	var addr net.Addr
	if w.ln != nil {
		addr = w.ln.Addr()
	}
	_, _ = io.WriteString(out, w.banner(addr))
	failed := make(chan error, len(all))
	for _, ls := range all {
		go func(ls localServer) {
			if err := ls.srv.Serve(ls.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				failed <- err
			}
		}(ls)
	}
	var err error
	select {
	case <-ctx.Done():
	case <-w.exit:
		_, _ = fmt.Fprintln(out, "no local requests and no running profile session for a while: exiting (idle)")
		stop()
	case err = <-failed:
		stop() // one listener failed: stop the others
	}
	<-shutDown // in-flight requests end before the terminal drivers close
	return err
}

// startup is what the startup lines say.
type startup struct {
	localOnly     bool
	idleAfter     time.Duration // a local-only serve exits when idle this long
	sockets       []string      // "client on path", one per local client
	port          int
	allowed       []string
	hosts         []string
	push          string // pushSummary; "" when no notifier is on
	hooksSettings string // Claude Code's settings.json, read by hooksSummary
	terminalOn    bool
	drivers       int
}

// lines are remote serve's startup lines, in order.
func (s startup) lines(addr net.Addr) string {
	out := ""
	if s.localOnly {
		out = fmt.Sprintf("aimux remote API on local sockets only (--local-only); exits when idle for %v (no local requests, no running profile session)\n", s.idleAfter)
	} else {
		out = fmt.Sprintf("aimux remote API on http://%s/v1 for %v via %v\n%s", addr, s.allowed, s.hosts, tailscaleServeHint(s.port, s.hosts))
	}
	for _, l := range s.sockets {
		out += "Local client " + l + "\n"
	}
	if s.push != "" {
		out += fmt.Sprintf("Approval notifications: %s\n", s.push)
	}
	out += hooksSummary(s.hooksSettings) + "\n"
	if s.terminalOn {
		out += fmt.Sprintf("Terminal input: on (%d terminal driver(s)); run `aimux remote terminal-input check` once at the Mac\n", s.drivers)
	}
	return out
}

// every runs f each period until ctx ends.
func every(period time.Duration, f func()) func(ctx context.Context) {
	return func(ctx context.Context) {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f()
			}
		}
	}
}

// refreshing runs refresh at once and then period after each run ends,
// until ctx ends; a failure while running is one line on errOut.
func refreshing(refresh func(context.Context) error, period time.Duration, errOut io.Writer) func(ctx context.Context) {
	return func(ctx context.Context) {
		for {
			if err := refresh(ctx); err != nil && ctx.Err() == nil {
				_, _ = fmt.Fprintln(errOut, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(period):
			}
		}
	}
}

// build constructs every service into w, adding a closer for each thing it
// opens, so a failure part way closes what was opened.
func (s *Server) build(w *wiring) error {
	cfg, o := s.cfg, s.opts
	home, errOut := o.Home, o.ErrOut
	var hosts []string
	if !o.LocalOnly {
		h, err := resolveTrustedHosts(cfg.RemoteAccess.Hostnames, o.DetectHost)
		if err != nil {
			return err
		}
		hosts = h
	}
	pushes, err := buildPush(cfg.RemoteAccess.Push, hosts, o.Allowed, home, o.Getenv)
	if err != nil {
		return err
	}
	var index *remoteapi.LocalIndex // set below, before the server takes requests
	sessionTitle := func(ctx context.Context, id string) string {
		title, err := index.Title(ctx, id) // never reads the terminal
		if err != nil {
			return "" // a session not written yet: the tool alone still says what is asked
		}
		return title
	}
	asked, resolved := approvalHooks(pushes, sessionTitle, errOut)

	var broker *remoteapi.Broker
	var dispatcher *chatqueue.Dispatcher // set before the server takes requests, so before any turn
	// Each provider's Live is the one reader of which of its sessions
	// are open in a terminal: the list, details, terminal input, the
	// queue, waiting, Last on your Mac and the layout read it through
	// the index (harness.List), the router before a send
	// (harness.Strict). New sessions and continuing ones run on the
	// default provider's ACP adapter.
	providers := Providers(home)
	def := providers[0]
	spec := func(cwd string) acprun.Spec {
		return acprun.Spec{Command: o.AdapterCommand, Args: o.AdapterArgs, Cwd: cwd}
	}
	manager := acprun.NewManager(acprun.ManagerOptions{
		MaxRuns:     o.MaxRuns,
		Fingerprint: def.Store.Fingerprint,
		Reserve:     o.Reserve,
		Start: func(ctx context.Context, id, cwd string) (acprun.Runner, error) {
			return acprun.Start(ctx, spec(cwd), id, broker.Approver())
		},
		StartNew: func(ctx context.Context, cwd string) (acprun.Runner, string, error) {
			r, err := acprun.StartNew(ctx, spec(cwd), broker.Approver())
			if err != nil {
				return nil, "", err
			}
			return r, r.SessionID(), nil
		},
	})
	w.onClose(manager.Close)
	router := &acprun.Router{Lookup: acprun.SessionLookup{Provider: def.Store.Name(), Store: def.Store, Live: def.Live}.Lookup, Manager: manager}
	// Sessions started under a profile (Meta in the profile store) go to
	// that profile's engine; every other session goes to router and manager
	// exactly as before. The engines are registered when profile sessions
	// can be started; until then a profile session is an error, never a
	// fallback to the unlocked host path.
	profiles, closeProfiles, err := buildProfileWiring(home, o.Getenv, errOut, nil)
	if err != nil {
		return err
	}
	w.onClose(closeProfiles)
	profileRouter := profiles.router
	profileRouter.Fallback, profileRouter.FallbackCheck, profileRouter.FallbackCancel = router.Send, router.Check, manager.Cancel
	broker = remoteapi.NewBroker(remoteapi.BrokerDeps{
		Check: profileRouter.Check, Send: profileRouter.Send, Cancel: profileRouter.Cancel,
	}, remoteapi.BrokerOptions{ApprovalTimeout: o.ApprovalTimeout, OnApproval: asked, OnApprovalResolved: resolved,
		OnTurnEnd: func(string) { dispatcher.Kick() }})
	// Keyword ranking only (below), in an index of its own: without
	// embeddings, refreshing the shared index would mark changed
	// sessions as seen before the CLI and TUI embed them. One service
	// refreshes it in the background; a second one answers queries, so
	// a search reads the last committed index (SQLite WAL) instead of
	// waiting for a refresh that can take minutes.
	remoteIndex := filepath.Join(home, ".aimux", "remote-search.db")
	searcher := search.DefaultService(io.Discard)
	searcher.Embedder, searcher.DBPath = nil, remoteIndex
	reader := search.DefaultService(io.Discard)
	reader.Embedder, reader.DBPath = nil, remoteIndex
	index = &remoteapi.LocalIndex{
		Providers: providers, NewProvider: def.Store.Name(), Broker: broker,
		// aimux's own adapters, the page engine's among them, are not terminals.
		Owned:   func(pid int) bool { return manager.Owns(pid) || profiles.owns(pid) },
		Repo:    gitremote.Lookup,
		Created: &remoteapi.CreatedStore{Path: filepath.Join(home, ".aimux", "remote-created.json")},
		Running: func(id string) bool { return manager.Running(id) || profiles.running(id) },
		// Finished sandbox sessions' transcripts are only in aimux's archive.
		Archived: profiles.archived,
		// Keyword ranking: every hit shows where the words matched, and a
		// query nothing contains finds nothing. The index is refreshed in
		// the background (the search refresh job); refreshing per query
		// takes seconds.
		SearchIndex: func(ctx context.Context, q string, limit int, ids []string) ([]search.Result, error) {
			rs, _, err := reader.Query(ctx, q, search.QueryOpts{Mode: search.ModeKeyword, Limit: limit, IncludeAutomated: true, NoRefresh: true, IDs: ids})
			return rs, err
		},
	}
	// Terminal input: the service reaches a session through the same
	// registry rule (live holder, not aimux's own) that makes it
	// open_in_terminal.
	drivers, closeDrivers := o.Drivers()
	w.onClose(closeDrivers)
	terminal, closeLog, err := buildTerminalInput(cfg.RemoteAccess.TerminalInput, home, index, drivers...)
	if err != nil {
		return err
	}
	w.onClose(closeLog)
	statusCache := remoteapi.NewStatusCache(terminal, time.Second) // detail reads only; sends read fresh
	index.Terminal = statusCache
	api := &remoteapi.API{Version: o.Version, TerminalInput: terminal, TerminalEnabled: cfg.RemoteAccess.TerminalInput.Enabled,
		Links:         &remoteapi.Links{JiraBase: cfg.RemoteAccess.Links.JiraBase, JiraProjects: cfg.RemoteAccess.Links.JiraProjects},
		TerminalTyped: statusCache.Invalidate, Log: errOut, Client: o.Client, Broker: broker, Sessions: index.Sessions, Session: index.Session,
		Search: index.Search, Projects: index.Projects,
		Create: func(ctx context.Context, cwd, text string) (string, error) {
			id, err := manager.New(ctx, cwd)
			if err != nil {
				return "", err
			}
			if err := index.Created.Add(id, cwd, text); err != nil {
				_, _ = fmt.Fprintln(errOut, err) // the session works; it may only be hidden from the list
			}
			return id, nil
		},
	}

	// Profile sessions: the resolved catalog, the clients' scopes, the
	// store and the engines; the store's health and the step-up of
	// sessions without a profile.
	profiles.Install(api, o.Scopes, cfg.RemoteAccess.StepUpLegacy)

	// Open on Mac: only with an iTerm2 driver and terminal input on.
	api.Handoff = buildHandoff(cfg.RemoteAccess.TerminalInput, drivers, handoffDeps{
		index: index, broker: broker, manager: manager, open: o.OpenTab,
		resume: resumeCommand(home, def.Store.Name()),
	})

	if pushes.devices != nil {
		api.RegisterDevice, api.UnregisterDevice = pushes.devices.Add, pushes.devices.Remove
	}
	var deviceLogin *devicelogin.Server
	if !o.LocalOnly { // device login guards the tailnet listener only
		dl, closeAudit, err := buildDeviceLogin(cfg.RemoteAccess.DeviceLoginOn(), home, errOut)
		if err != nil {
			return err
		}
		w.onClose(closeAudit)
		deviceLogin = dl
	}
	api.DeviceLogin = deviceLogin
	sockets := socketClients(o.Scopes)
	if deviceLogin == nil && len(sockets) > 0 { // local clients' lines need a log of their own
		path := AuditPath(home)
		log, err := terminput.OpenLog(path) // 0600, append
		if err != nil {
			return fmt.Errorf("open the audit log %s: %w", path, err)
		}
		w.onClose(func() { _ = log.Close() })
		api.Audit = devicelogin.NewAudit(log, time.Now, errOut)
	}
	var audit chatqueue.Auditor
	if deviceLogin != nil {
		audit = deviceLogin.Audit()
	}
	queue, d, err := openChatQueue(home, cfg.RemoteAccess.TerminalInput, broker, index, terminal, audit, statusCache.Invalidate, api.PublishQueue)
	if err != nil {
		return err
	}
	w.onClose(queue.Close)
	dispatcher = d
	api.Queue, api.QueueKick, api.QueueNote, api.SessionExists = queue, d.Kick, d.Note, index.SessionExists
	// A Send now's history turn is aimux's own, not the owner's.
	api.QueueSentNow = func(session, text string) func() { return d.NoteSentNow(session, text, time.Now()) }
	// Titles and descriptions: "Update summary" always; automatic
	// naming of the sessions clients show only with sessions.auto_title.
	namer := history.NewNamer(namingConfig(cfg.Sessions, home, index.Lookup, errOut))
	api.Record, api.Naming, api.AutoName = index.Record, namer, cfg.Sessions.AutoTitle
	lastFocus := buildLastFocus(cfg.RemoteAccess.TerminalInput, index.TerminalPIDs, drivers...)
	if lastFocus != nil {
		api.LastOnMac = lastFocus.Last
	}
	// Folder names and the Mac layout of listed sessions. The layout
	// is read with or without terminal input: it only reads.
	resolver := folders.NewResolver(home, filepath.Join(home, ".aimux", "folders.json"))
	api.Folder = resolver.Folder
	if layout := buildLayoutReader(index.TerminalPIDs, lastFocus, errOut, drivers...); layout != nil {
		api.Places = layout.Places
	}
	// What each session is doing (activity, waiting, working): open
	// sessions, aimux's runs and approvals, and the markers aimux remote
	// hooks notify writes for terminal sessions at a permission prompt.
	waits := &waiting.Tracker{Dir: waiting.Dir(home), Live: index.TerminalPIDs}
	api.States = buildStates(index, broker, waits)
	api.SessionsAt = index.SessionsAt // the list's state and working from the service's read
	if !o.LocalOnly {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", o.Port))
		if err != nil {
			return fmt.Errorf("listen on 127.0.0.1:%d: %w (choose another port with --port)", o.Port, err)
		}
		w.ln = ln
		w.onClose(func() { _ = ln.Close() }) // Serve closes it too; this covers a build that never serves
		w.srv = &http.Server{Handler: api.Handler(o.Allowed, hosts), ReadHeaderTimeout: 10 * time.Second}
	}
	api.LocalOnly = o.LocalOnly
	// A local-only serve (Wingman starts one when nothing answers on its
	// socket) exits on its own once idle, so it never outlives its use and
	// never keeps a later full serve from starting.
	var idle *idleWatch
	if o.LocalOnly {
		idle = newIdleWatch(s.idleAfterOr(), time.Now, func() bool { return profileSessionsBusy(profiles, broker) })
	}
	var socketLines []string
	for _, c := range sockets {
		if c.name == deviceClient {
			return fmt.Errorf("client %s cannot have a socket: device requests are checked by device login, on the tailnet listener only", c.name)
		}
		ln, err := listenSocket(c.socket)
		if err != nil {
			return fmt.Errorf("client %s: %w", c.name, err)
		}
		w.onClose(func() { _ = ln.Close() })
		h := api.LocalHandler(c.name)
		if idle != nil {
			h = idle.wrap(h)
		}
		w.locals = append(w.locals, localServer{ln: ln, srv: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}})
		socketLines = append(socketLines, c.name+" on "+c.socket)
	}
	if o.LocalOnly && len(w.locals) == 0 {
		return errors.New("--local-only needs a client with a socket (clients.yaml); none has one")
	}

	// The background jobs, started in this order. Shutdown waits for the
	// awaited ones before anything closes.
	if deviceLogin != nil {
		w.jobs = append(w.jobs, job{name: "device login", run: deviceLogin.Watch}) // ends streams of revoked or expired logins
	}
	w.jobs = append(w.jobs,
		job{name: "queue events", run: api.QueueEvents},
		job{name: "chat queue", await: true, run: dispatcher.Run},
	)
	if cfg.Sessions.AutoTitle {
		w.jobs = append(w.jobs, job{name: "namer", await: true, run: namer.Run})
	}
	if lastFocus != nil {
		w.jobs = append(w.jobs, job{name: "last focus", await: true, run: func(ctx context.Context) { lastFocus.Run(ctx, lastFocusEvery) }})
	}
	w.jobs = append(w.jobs,
		job{name: "folders", await: true, run: resolver.Run},
		job{name: "waiting alerts", await: true, run: func(ctx context.Context) {
			runWaitingAlerts(ctx, waits, waitingAlerts(pushes, sessionTitle, errOut))
		}},
		job{name: "reap idle runs", run: every(time.Minute, func() { manager.Reap(); profiles.reap() })},
		job{name: "search refresh", run: refreshing(searcher.Refresh, time.Minute, errOut)},
	)
	if idle != nil {
		w.exit = make(chan struct{})
		var once sync.Once
		w.jobs = append(w.jobs, job{name: "idle exit", run: idle.run(s.idleEveryOr(), func() { once.Do(func() { close(w.exit) }) })})
	}

	if s.wrapJob != nil {
		for i := range w.jobs {
			w.jobs[i] = s.wrapJob(w.jobs[i])
		}
	}

	st := startup{localOnly: o.LocalOnly, idleAfter: s.idleAfterOr(), sockets: socketLines, port: o.Port, allowed: o.Allowed, hosts: hosts, hooksSettings: filepath.Join(home, ".claude", "settings.json"),
		terminalOn: cfg.RemoteAccess.TerminalInput.Enabled, drivers: len(drivers)}
	if len(pushes.notifiers) > 0 {
		st.push = pushSummary(cfg.RemoteAccess.Push)
	}
	w.banner = st.lines
	return nil
}

func (s *Server) idleAfterOr() time.Duration {
	if s.idleAfter > 0 {
		return s.idleAfter
	}
	return localIdleAfter
}

func (s *Server) idleEveryOr() time.Duration {
	if s.idleEvery > 0 {
		return s.idleEvery
	}
	return time.Minute
}

// profileSessionsBusy reports whether any profile session has a turn in
// the broker or a run in an engine; an unreadable store counts as busy (a
// serve that cannot tell keeps running).
func profileSessionsBusy(p *profileWiring, broker *remoteapi.Broker) bool {
	all, err := p.store.All()
	if err != nil {
		return true
	}
	for id := range all {
		if broker.Busy(id) || p.running(id) {
			return true
		}
	}
	return false
}

// ownerLocalOnly asks the serve answering on sock whether it is local-only
// (GET /v1/health). No answer, or an older serve without the field, counts
// as not local-only.
func ownerLocalOnly(sock string) bool {
	c := &http.Client{Timeout: socketDialTimeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}
	resp, err := c.Get("http://aimux.local/v1/health")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	var h struct {
		LocalOnly bool `json:"localOnly"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&h) != nil {
		return false
	}
	return h.LocalOnly
}

// deviceClient is the tailnet routes' client; it never gets a socket.
const deviceClient = "device"

// socketClient is a client with a socket of its own.
type socketClient struct{ name, socket string }

// socketClients are the clients with a Socket, by name.
func socketClients(scopes clientscope.Scopes) []socketClient {
	var out []socketClient
	for name, c := range scopes.Clients {
		if c.Socket != "" {
			out = append(out, socketClient{name: name, socket: c.Socket})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}
