package remoteapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/catalog"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/terminput/iterm2"
	"github.com/zanetworker/aimux/internal/waiting"
)

// resolveTrustedHosts returns the Host names the API accepts: configured
// names, or this machine's MagicDNS name from tailscale.
func resolveTrustedHosts(configured []string, detect func() (string, error)) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}
	name, err := detect()
	if err != nil || strings.TrimSuffix(name, ".") == "" {
		return nil, fmt.Errorf("cannot tell which Host names to trust: set remote_access.hostnames (your tailnet name, e.g. mac.tailnet.ts.net) or start tailscale (%v)", err)
	}
	return []string{strings.TrimSuffix(name, ".")}, nil
}

// tailscaleDNSName asks tailscale for this machine's MagicDNS name.
func tailscaleDNSName() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale status: %w", err)
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return "", fmt.Errorf("tailscale status: %w", err)
	}
	return st.Self.DNSName, nil
}

func tailscaleServeHint(port int, hosts []string) string {
	hint := fmt.Sprintf("Expose it on your tailnet (HTTPS, tailnet only):\n  tailscale serve --bg --https=8443 http://127.0.0.1:%d\n", port)
	if len(hosts) > 0 {
		hint += fmt.Sprintf("Then open the web client on your phone: https://%s:8443/app/\n", hosts[0])
	}
	return hint
}

// buildTerminalInput builds the service that types phone messages into
// terminal sessions. The attempt log (~/.aimux/logs/terminal-input.log,
// 0600, never the text) is opened only when terminal input is on.
func buildTerminalInput(cfg config.TerminalInputConfig, home string, sessions terminput.Sessions, drivers ...terminput.Driver) (*terminput.Service, func(), error) {
	var log io.WriteCloser
	if cfg.Enabled {
		var err error
		if log, err = terminput.OpenLog(filepath.Join(home, ".aimux", "logs", "terminal-input.log")); err != nil {
			return nil, nil, fmt.Errorf("open the terminal input log: %w", err)
		}
	}
	closeLog := func() {
		if log != nil {
			_ = log.Close()
		}
	}
	var w io.Writer
	if log != nil {
		w = log
	}
	return terminput.NewService(cfg, sessions, terminput.SystemProbe{}, w, drivers...), closeLog, nil
}

// namingConfig wires the session namer from sessions config: the API key
// from sessions.api_key or the environment (never logged), else claude -p;
// today's counts in ~/.aimux/naming-state.json (0600). Usage goes to the
// namer's default, ~/.aimux/data/usage.jsonl, under "naming".
func namingConfig(cfg config.SessionsConfig, home string, lookup func(string) (history.Session, bool), logw io.Writer) history.NamerConfig {
	primary, fallback := history.NamingBackends(cfg.APIKey)
	nc := history.NamerConfig{Model: cfg.TitleModel, DailyLimit: cfg.NamingDailyLimit, Lookup: lookup,
		Primary: primary, Fallback: fallback, StatePath: filepath.Join(home, ".aimux", "naming-state.json")}
	if logw != nil {
		nc.Logf = func(format string, args ...any) { _, _ = fmt.Fprintf(logw, format+"\n", args...) }
	}
	return nc
}

// lastFocusEvery is how often Last on your Mac looks at the Mac.
const lastFocusEvery = 15 * time.Second

// buildLastFocus is Last on your Mac: nil unless terminal input is on and
// a driver can report focus. It only reads which pane has focus.
func buildLastFocus(cfg config.TerminalInputConfig, sessions func() map[string]int, drivers ...terminput.Driver) *terminput.LastFocus {
	if !cfg.Enabled {
		return nil
	}
	for _, d := range drivers {
		if fr, ok := d.(terminput.FocusReporter); ok {
			return &terminput.LastFocus{Enabled: true, Sys: terminput.SystemProbe{}, Focus: fr, Sessions: sessions, Now: time.Now}
		}
	}
	return nil
}

// buildLayoutReader reads the iTerm2 layout for the session list, from the
// first driver that can report one, whether or not terminal input is on.
// lastFocus may be nil. Nil when no driver reports a layout.
func buildLayoutReader(sessions func() map[string]int, lastFocus *terminput.LastFocus, log io.Writer, drivers ...terminput.Driver) *terminput.LayoutReader {
	for _, d := range drivers {
		if src, ok := d.(terminput.LayoutSource); ok {
			r := &terminput.LayoutReader{Source: src, Sys: terminput.SystemProbe{}, Sessions: sessions, Now: time.Now, Log: log}
			if lastFocus != nil {
				r.LastOnMac = lastFocus.Last
			}
			return r
		}
	}
	return nil
}

// Both the iTerm2 driver and the process probe feed the layout reader.
var (
	_ terminput.LayoutSource = (*iterm2.Driver)(nil)
	_ terminput.LayoutSystem = terminput.SystemProbe{}
)

// buildDeviceLogin sets up device login when on: the paired devices in
// ~/.aimux and the audit log ~/.aimux/logs/remote-audit.log (0600). Off,
// it returns nil and prints a warning to out: the tailnet check alone admits
// any device of an allowed login.
func buildDeviceLogin(on bool, home string, out io.Writer) (*devicelogin.Server, func(), error) {
	if !on {
		_, _ = fmt.Fprintln(out, "Warning: remote_access.device_login is false: any device signed in to your tailnet as an allowed login can use this API, with no pairing and no Face ID.")
		return nil, func() {}, nil
	}
	path := AuditPath(home)
	log, err := terminput.OpenLog(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open the audit log %s: %w", path, err)
	}
	dl, err := devicelogin.NewServer(devicelogin.Options{
		Store: devicelogin.Open(filepath.Join(home, ".aimux")),
		Audit: devicelogin.NewAudit(log, time.Now, out),
		Login: func(r *http.Request) string { return remoteapi.LoginFrom(r.Context()) },
		Log:   out,
	})
	if err != nil {
		_ = log.Close()
		return nil, nil, err
	}
	_, _ = fmt.Fprintln(out, "Device login: on; pair a phone or browser with `aimux remote pair --name <name>`")
	return dl, func() { _ = log.Close() }, nil
}

// openChatQueue opens the chat queue (~/.aimux/remote-queue.json, 0600) and
// its dispatcher, which sends through the broker (sessions started from
// the phone) and through input (sessions open in a terminal) under the
// same switches as the terminal routes: with terminal input off it never
// reads or types into a terminal. noteChanged hears changes of a session's
// waiting line. The caller runs the dispatcher.
func openChatQueue(home string, tcfg config.TerminalInputConfig, broker *remoteapi.Broker, index *remoteapi.LocalIndex,
	input remoteapi.QueueInput, audit chatqueue.Auditor, typed, noteChanged func(string)) (*chatqueue.Store, *chatqueue.Dispatcher, error) {
	store, err := chatqueue.Open(filepath.Join(home, ".aimux", "remote-queue.json"), time.Now, tcfg.AllowSlashCommands)
	if err != nil {
		return nil, nil, err
	}
	d := chatqueue.New(chatqueue.Config{
		Store: store, ACP: remoteapi.QueueACP{Broker: broker},
		Terminal: remoteapi.QueueTerminal{Index: index, Input: input}, TerminalEnabled: tcfg.Enabled,
		Audit: audit, Typed: typed, NoteChanged: noteChanged,
	})
	return store, d, nil
}

// AuditPath is the remote API's audit log, shared by the server and the CLI (aimux remote revoke).
func AuditPath(home string) string { return filepath.Join(home, ".aimux", "logs", "remote-audit.log") }

// Providers is the harnesses remote serve lists, follows and continues
// sessions of (catalog.ProvidersOf over the registry): every registry
// provider that implements catalog.Store and provider.LiveSource, so a new harness implements those
// two and needs no change here or inside remoteapi. Today that is Claude
// only. The first is the default: sessions a client creates, the router and
// the chat manager are its, since the ACP adapter (--adapter) is Claude
// Code's. Another entry's sessions are listed but cannot be continued until
// the router and the manager dispatch per provider.
func Providers(home string) []remoteapi.Provider {
	return catalog.ProvidersOf(provider.NewRegistry(home, config.Config{}).Local(), discovery.LiveTTL)
}

// hooksSummary is remote serve's startup line about permission alerts for
// terminal sessions.
func hooksSummary(settingsPath string) string {
	st, err := waiting.Status(settingsPath)
	switch {
	case err != nil:
		return "Permission alerts for terminal sessions: cannot read " + settingsPath
	case len(st.Missing) > 0:
		return "Permission alerts for terminal sessions: off; run `aimux remote hooks install`"
	}
	return "Permission alerts for terminal sessions: on (Claude Code hooks)"
}

// windowOpener opens Open on Mac's panes in iTerm2's aimux windows (up to
// iterm2.MaxPanes a window), found by their user.aimux session variable.
func windowOpener() *iterm2.Opener {
	return &iterm2.Opener{}
}

// resumeCommand is the registry provider name's ResumeCommand; nil when the
// registry has no such provider.
func resumeCommand(home, name string) func(agent.Agent) *exec.Cmd {
	p := provider.NewRegistry(home, config.Config{}).ByName(name)
	if p == nil {
		return nil
	}
	return p.ResumeCommand
}

// maxTabTitle is how many runes of the shown title name the new pane.
const maxTabTitle = 60

// handoffDeps is what Open on Mac is built from.
type handoffDeps struct {
	index   *remoteapi.LocalIndex
	broker  *remoteapi.Broker
	manager *acprun.Manager
	resume  func(agent.Agent) *exec.Cmd // the default provider's ResumeCommand
	open    func(ctx context.Context, title string, argv []string, dir string) error
	dropped *droppedPIDs // set by buildHandoff
}

// buildHandoff is Open on Mac: nil (the route answers no_terminal) unless
// terminal input is on, an iTerm2 driver is configured and the default
// provider can resume a session.
func buildHandoff(cfg config.TerminalInputConfig, drivers []terminput.Driver, d handoffDeps) *remoteapi.Handoff {
	if !cfg.Enabled || d.resume == nil || d.open == nil || !hasDriver(drivers, "iterm2") {
		return nil
	}
	d.dropped = &droppedPIDs{bySession: map[string]map[int]bool{}}
	return &remoteapi.Handoff{
		Hold:   d.broker.Hold,
		Drop:   d.drop,
		State:  func(_ context.Context, id string) (string, error) { return d.state(id), nil },
		Target: d.target,
		Open: func(ctx context.Context, title string, argv []string, dir string) error {
			err := d.open(ctx, title, argv, dir)
			if errors.Is(err, iterm2.ErrNotAuthorized) {
				return fmt.Errorf("%w: %w", remoteapi.ErrNotAuthorized, err)
			}
			return err
		},
	}
}

func hasDriver(drivers []terminput.Driver, name string) bool {
	for _, dr := range drivers {
		if dr.Name() == name {
			return true
		}
	}
	return false
}

// droppedPIDs are, per session, the processes of the run the last handoff
// dropped. Once dropped, Manager.Owns no longer knows them, while the live
// read may still list them (cached up to discovery.LiveTTL, or still
// exiting).
type droppedPIDs struct {
	mu        sync.Mutex
	bySession map[string]map[int]bool
}

func (p *droppedPIDs) set(id string, pids map[int]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bySession[id] = pids
}

func (p *droppedPIDs) has(id string, pid int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bySession[id][pid]
}

// drop closes the session's idle run, first noting the processes of that
// run holding the session (aimux's own, by Manager.Owns), so state does
// not take them for the new tab.
func (d handoffDeps) drop(id string) error {
	own := map[int]bool{}
	for _, p := range d.index.Providers {
		if p.Live == nil {
			continue
		}
		hs, _ := p.Live.Holders(harness.List)
		for _, h := range hs {
			if h.SessionID == id && d.manager.Owns(h.PID) {
				own[h.PID] = true
			}
		}
	}
	if err := d.manager.Drop(id); err != nil {
		return err
	}
	d.dropped.set(id, own)
	return nil
}

// state is the session's state from the live sessions read (in memory,
// refreshed at most once a LiveTTL), polled while the tab starts. A
// terminal holding the session wins over busy, since the handoff's own
// hold makes the Broker report busy until the handoff ends. A holder that
// is aimux's own, belonged to the run the handoff dropped, or whose
// process is gone is no terminal.
func (d handoffDeps) state(id string) string {
	cat := d.index.Catalog()
	owned := cat.Owned
	cat.Owned = func(pid int) bool {
		return (owned != nil && owned(pid)) || d.dropped.has(id, pid) || !processAlive(pid)
	}
	open := cat.Open()
	if _, ok := open[id]; ok {
		return remoteapi.StateOpenInTerminal
	}
	return cat.StateOf(id, open)
}

// processAlive reports whether pid is a running process (signal 0; EPERM
// means it runs as another user).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// target is the new tab's title and the command resuming the session:
// ErrNotStarted for a session created here without a transcript,
// ErrUnknownSession for one aimux does not know, ErrInUseElsewhere while
// another aimux process holds its lock. It runs once per handoff, so the
// lock is probed once and never taken: between Drop and the tab's claude
// starting, another aimux process could claim the session, as it could
// against a claude --resume typed by hand. A command whose binary did not
// resolve to an absolute path is refused, so it never reaches the shell
// line.
func (d handoffDeps) target(ctx context.Context, id string) (string, []string, string, error) {
	cat := d.index.Catalog()
	file, store := cat.File(id)
	if file == "" {
		if _, ok := d.index.Created.Get(id); ok {
			return "", nil, "", remoteapi.ErrNotStarted
		}
		return "", nil, "", fmt.Errorf("%w: %s", acprun.ErrUnknownSession, id)
	}
	if err := d.manager.ProbeReserved(id); err != nil {
		return "", nil, "", err
	}
	rec, ok := d.index.Lookup(id)
	if !ok {
		var err error
		if rec, err = d.index.Record(ctx, id); err != nil {
			return "", nil, "", err
		}
	}
	cwd := rec.Cwd
	if cwd == "" {
		cwd, _ = store.SessionCwd(file)
	}
	if cwd == "" {
		return "", nil, "", fmt.Errorf("session %s recorded no working directory to resume it in", id)
	}
	cmd := d.resume(agent.Agent{SessionID: id, WorkingDir: cwd})
	if cmd == nil {
		return "", nil, "", fmt.Errorf("session %s: the provider has no resume command", id)
	}
	if cmd.Err != nil {
		return "", nil, "", fmt.Errorf("session %s: resume command: %w", id, cmd.Err)
	}
	if !filepath.IsAbs(cmd.Path) {
		return "", nil, "", fmt.Errorf("session %s: resume command %q is not an absolute path", id, cmd.Path)
	}
	dir := cmd.Dir
	if dir == "" {
		dir = cwd
	}
	argv := append([]string{cmd.Path}, cmd.Args[1:]...)
	return cutRunes(history.ShownTitle(rec), maxTabTitle), argv, dir, nil
}

// cutRunes is s cut to its first n runes.
func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
