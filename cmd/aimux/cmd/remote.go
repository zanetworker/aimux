package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	webclient "github.com/zanetworker/aimux/clients/web"
	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/gitremote"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/terminput/iterm2"
)

type remoteSettings struct {
	port            int
	allowed         []string
	adapter         string
	maxRuns         int
	approvalTimeout time.Duration
}

// resolveRemoteSettings applies flags over remote_access config over
// defaults. It refuses to run with nobody allowed.
func resolveRemoteSettings(cfg config.RemoteAccessConfig, port int, allow []string) (remoteSettings, error) {
	s := remoteSettings{port: 7382, adapter: defaultClaudeAdapter, maxRuns: 2, approvalTimeout: 10 * time.Minute, allowed: cfg.AllowedLogins}
	if cfg.Port > 0 {
		s.port = cfg.Port
	}
	if port > 0 {
		s.port = port
	}
	if len(allow) > 0 {
		s.allowed = allow
	}
	if cfg.Adapter != "" {
		s.adapter = cfg.Adapter
	}
	if cfg.MaxRuns > 0 {
		s.maxRuns = cfg.MaxRuns
	}
	if cfg.ApprovalTimeout != "" {
		d, err := time.ParseDuration(cfg.ApprovalTimeout)
		if err != nil || d <= 0 {
			return s, fmt.Errorf("remote_access.approval_timeout %q is not a duration like 10m", cfg.ApprovalTimeout)
		}
		s.approvalTimeout = d
	}
	if len(s.allowed) == 0 {
		return s, errors.New("nobody is allowed: set remote_access.allowed_logins in ~/.aimux/config.yaml or pass --allow <tailscale-login>")
	}
	return s, nil
}

// pushSetup is what remote serve notifies approvals through.
type pushSetup struct {
	notifiers []push.Notifier
	devices   *push.DeviceStore // set when APNs is on: the app registers here
	clickURL  string            // web client a tapped ntfy notification opens
}

// buildPush turns remote_access.push into senders, refusing a half-done
// setup at startup rather than failing on the first approval.
func buildPush(cfg config.PushConfig, hosts, allowedLogins []string, home string, getenv func(string) string) (pushSetup, error) {
	var p pushSetup
	if n := cfg.Ntfy; n.Topic != "" {
		server := n.Server
		if server == "" {
			server = "https://ntfy.sh"
		}
		token := ""
		if n.TokenEnv != "" {
			if token = getenv(n.TokenEnv); token == "" {
				return p, fmt.Errorf("remote_access.push.ntfy.token_env names %s, which is not set", n.TokenEnv)
			}
		}
		p.notifiers = append(p.notifiers, &push.Ntfy{Server: server, Topic: n.Topic, Token: token})
		p.clickURL = n.ClickURL
		if p.clickURL == "" && len(hosts) > 0 {
			p.clickURL = "https://" + hosts[0] + ":8443/app/"
		}
	}
	if a := cfg.APNs; a != (config.APNsConfig{}) {
		var missing []string
		for _, f := range []struct{ name, v string }{{"key_file", a.KeyFile}, {"key_id", a.KeyID}, {"team_id", a.TeamID}, {"topic", a.Topic}} {
			if f.v == "" {
				missing = append(missing, f.name)
			}
		}
		if len(missing) > 0 {
			return p, fmt.Errorf("remote_access.push.apns needs %s", strings.Join(missing, ", "))
		}
		keyFile := a.KeyFile
		if rest, ok := strings.CutPrefix(keyFile, "~/"); ok {
			keyFile = filepath.Join(home, rest)
		}
		key, err := push.LoadAPNsKey(keyFile)
		if err != nil {
			return p, err
		}
		p.devices = &push.DeviceStore{Path: filepath.Join(home, ".aimux", "remote-devices.json")}
		p.notifiers = append(p.notifiers, &push.APNs{Key: key, KeyID: a.KeyID, TeamID: a.TeamID, Topic: a.Topic, Devices: p.devices, AllowedLogins: allowedLogins})
	}
	return p, nil
}

// titleLookupTimeout bounds reading the session's title, so a slow read of
// a large session leaves the delivery its own time.
const titleLookupTimeout = 5 * time.Second

// lookupTitle reads the session's title, giving up after
// titleLookupTimeout even when the read ignores its context (the session
// reader does): the alert then goes out without it.
func lookupTitle(sessionTitle func(ctx context.Context, id string) string, id string) string {
	ctx, cancel := context.WithTimeout(context.Background(), titleLookupTimeout)
	defer cancel()
	got := make(chan string, 1) // buffered: a late read finishes and is dropped
	go func() { got <- sessionTitle(ctx, id) }()
	select {
	case t := <-got:
		return t
	case <-ctx.Done():
		return ""
	}
}

// approvalHooks are the broker's OnApproval and OnApprovalResolved: the
// first names the session and sends the notification, the second takes it
// back once the approval is settled. The broker runs them in goroutines of
// their own, so an approval answered quickly can be resolved before its
// alert is out: the alert is then skipped, or taken back only after it was
// sent. Failures are logged. Both are nil when nothing is on.
func approvalHooks(p pushSetup, sessionTitle func(ctx context.Context, id string) string, errOut io.Writer) (asked, resolved func(remoteapi.ApprovalNotice)) {
	if len(p.notifiers) == 0 {
		return nil, nil
	}
	type alert struct {
		resolved bool
		sent     bool
		done     chan struct{} // closed once asked has sent or skipped the alert
	}
	var mu sync.Mutex
	alerts := map[string]*alert{}
	get := func(id string) *alert { // under mu
		a, ok := alerts[id]
		if !ok {
			a = &alert{done: make(chan struct{})}
			alerts[id] = a
		}
		return a
	}
	asked = func(n remoteapi.ApprovalNotice) {
		mu.Lock()
		a := get(n.ApprovalID)
		mu.Unlock()
		defer close(a.done)
		title := lookupTitle(sessionTitle, n.Session)
		mu.Lock()
		skip := a.resolved
		a.sent = !skip
		mu.Unlock()
		if skip {
			return // answered or expired already: nothing to look at
		}
		m := push.ForApproval(title, n.Title)
		m.ApprovalID, m.SessionID, m.Expires = n.ApprovalID, n.Session, n.Expires
		if p.clickURL != "" {
			m.Link = p.clickURL + "#/s/" + url.PathEscape(n.Session)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := push.Send(ctx, p.notifiers, m); err != nil {
			_, _ = fmt.Fprintln(errOut, "approval notification:", err)
		}
	}
	resolved = func(n remoteapi.ApprovalNotice) {
		mu.Lock()
		a := get(n.ApprovalID)
		a.resolved = true
		mu.Unlock()
		select {
		case <-a.done:
		case <-time.After(titleLookupTimeout + 40*time.Second): // asked is bounded well inside this
		}
		mu.Lock()
		sent := a.sent
		delete(alerts, n.ApprovalID)
		mu.Unlock()
		if !sent {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := push.Retract(ctx, p.notifiers, push.Message{ApprovalID: n.ApprovalID, SessionID: n.Session}); err != nil {
			_, _ = fmt.Fprintln(errOut, "approval notification (taking it back):", err)
		}
	}
	return asked, resolved
}

// pushSummary names the senders that are on, for the startup banner.
func pushSummary(cfg config.PushConfig) string {
	var on []string
	if cfg.Ntfy.Topic != "" {
		on = append(on, "ntfy topic "+cfg.Ntfy.Topic)
	}
	if cfg.APNs.Topic != "" {
		on = append(on, "APNs for "+cfg.APNs.Topic)
	}
	return strings.Join(on, ", ")
}

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

// buildDeviceLogin sets up device login when on: the paired devices in
// ~/.aimux and the audit log ~/.aimux/logs/remote-audit.log (0600). Off,
// it returns nil and prints a warning to out: the tailnet check alone admits
// any device of an allowed login.
func buildDeviceLogin(on bool, home string, out io.Writer) (*devicelogin.Server, func(), error) {
	if !on {
		_, _ = fmt.Fprintln(out, "Warning: remote_access.device_login is false: any device signed in to your tailnet as an allowed login can use this API, with no pairing and no Face ID.")
		return nil, func() {}, nil
	}
	path := auditPath(home)
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

// auditPath is the remote API's audit log, shared by the server and the CLI.
func auditPath(home string) string { return filepath.Join(home, ".aimux", "logs", "remote-audit.log") }

// auditRevoke writes the revoke line for d to the audit log, as the server
// writes its own lines; the server does not see who revoked.
func auditRevoke(d devicelogin.Device, errOut io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("audit the revoke: resolve home directory: %w", err)
	}
	log, err := terminput.OpenLog(auditPath(home))
	if err != nil {
		return fmt.Errorf("audit the revoke: open %s: %w", auditPath(home), err)
	}
	defer func() { _ = log.Close() }()
	devicelogin.NewAudit(log, time.Now, errOut).Write(devicelogin.AuditEntry{Login: d.Login, Device: d.Name, Action: devicelogin.ActionRevoke, Result: "ok"})
	return nil
}

func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "API for your sessions from other devices, through tailscale serve",
	}
	cmd.AddCommand(newRemoteServeCmd(), newRemoteTerminalInputCmd(), newRemotePairCmd(), newRemoteDevicesCmd(), newRemoteRevokeCmd())
	return cmd
}

// pairingValid is how long a code from `aimux remote pair` works.
const pairingValid = 5 * time.Minute

// pairedStore opens the paired-device files in ~/.aimux.
func pairedStore() (*devicelogin.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	return devicelogin.Open(filepath.Join(home, ".aimux")), nil
}

func newRemotePairCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "pair --name <name>",
		Short: "Print a one-time code that pairs a phone or browser with the remote API",
		Long: "Print a code of 8 characters (XXXX-XXXX) to type into the aimux iPhone app or the web client. " +
			"It works once, for 5 minutes, and replaces any earlier code; five wrong tries burn it, so run this again. " +
			"The device is listed under the name you give (1 to 32 characters, not already paired). " +
			"Only the code's SHA-256 is saved (in ~/.aimux/remote-pairing.json); the code itself is shown here only.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return fmt.Errorf("read %s: %w", config.DefaultPath(), err)
			}
			store, err := pairedStore()
			if err != nil {
				return err
			}
			code, hash := devicelogin.NewCode()
			expires := time.Now().Add(pairingValid)
			if err := store.SetPending(name, hash, expires); err != nil {
				return fmt.Errorf("pair %q: %w", name, err)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Pairing code for %s:\n\n    %s\n\nType it on the device within 5 minutes (until %s). It works once.\n",
				name, code, expires.Format("15:04"))
			if !cfg.RemoteAccess.DeviceLoginOn() {
				_, _ = fmt.Fprintln(out, "Note: remote_access.device_login is false in ~/.aimux/config.yaml, so the server does not ask for a paired device yet.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Name the device is listed under, e.g. iphone (required)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newRemoteDevicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List the devices paired with the remote API",
		Long:  "List each paired device's name, kind (secure-enclave for the iPhone app, webauthn for a browser passkey), when it was paired and when it last used the API (- if never).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := pairedStore()
			if err != nil {
				return err
			}
			devices, err := store.Devices()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(devices) == 0 {
				_, _ = fmt.Fprintln(out, "No paired devices. Pair one with: aimux remote pair --name <name>")
				return nil
			}
			seen := store.LastSeen()
			const layout = "2006-01-02 15:04"
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tKIND\tCREATED\tLAST SEEN")
			for _, d := range devices {
				last := "-"
				if t, ok := seen[d.ID]; ok {
					last = t.Local().Format(layout)
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Name, d.Kind, d.Created.Local().Format(layout), last)
			}
			return w.Flush()
		},
	}
}

func newRemoteRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name>",
		Short: "Unpair a device: its next request to the remote API fails",
		Long:  "Remove a paired device by the name `aimux remote devices` shows. A running server notices within a second or two; the device's logins end on their next request and it must be paired again to get back in.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := pairedStore()
			if err != nil {
				return err
			}
			d, err := store.Remove(args[0])
			if errors.Is(err, devicelogin.ErrNoDevice) {
				return fmt.Errorf("no paired device named %q (see aimux remote devices)", args[0])
			}
			if err != nil {
				return err
			}
			if err := auditRevoke(d, cmd.ErrOrStderr()); err != nil {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err) // the revoke stands; only its audit line is missing
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s (%s). Its next request is refused.\n", d.Name, d.Kind)
			return nil
		},
	}
}

func newRemoteServeCmd() *cobra.Command {
	var (
		port  int
		allow []string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the /v1 API on 127.0.0.1 for tailscale serve to expose",
		Long: "Serve the remote API: list Claude sessions, follow them, continue idle ones and answer " +
			"approvals from another device. It listens on 127.0.0.1 only and admits a request only when " +
			"tailscale serve forwarded it for a login in remote_access.allowed_logins. It also serves a " +
			"phone web client at /app/: after tailscale serve, open https://<this machine's tailnet name>:8443/app/.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return fmt.Errorf("read %s: %w", config.DefaultPath(), err)
			}
			s, err := resolveRemoteSettings(cfg.RemoteAccess, port, allow)
			if err != nil {
				return err
			}
			name, adapterArgs, err := parseAdapter(s.adapter)
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			projects := filepath.Join(home, ".claude", "projects")
			registry := filepath.Join(home, ".claude", "sessions")
			hosts, err := resolveTrustedHosts(cfg.RemoteAccess.Hostnames, tailscaleDNSName)
			if err != nil {
				return err
			}
			pushes, err := buildPush(cfg.RemoteAccess.Push, hosts, s.allowed, home, os.Getenv)
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
			asked, resolved := approvalHooks(pushes, sessionTitle, cmd.ErrOrStderr())

			var broker *remoteapi.Broker
			var dispatcher *chatqueue.Dispatcher // set before the server takes requests, so before any turn
			manager := acprun.NewManager(acprun.ManagerOptions{
				MaxRuns:     s.maxRuns,
				Fingerprint: acprun.ClaudeFingerprint(projects),
				Reserve:     sessionReservation(home),
				Start: func(ctx context.Context, id, cwd string) (acprun.Runner, error) {
					return acprun.Start(ctx, acprun.Spec{Command: name, Args: adapterArgs, Cwd: cwd}, id, broker.Approver())
				},
				StartNew: func(ctx context.Context, cwd string) (acprun.Runner, string, error) {
					r, err := acprun.StartNew(ctx, acprun.Spec{Command: name, Args: adapterArgs, Cwd: cwd}, broker.Approver())
					if err != nil {
						return nil, "", err
					}
					return r, r.SessionID(), nil
				},
			})
			defer manager.Close()
			router := &acprun.Router{Lookup: acprun.ClaudeLookup(projects, registry), Manager: manager}
			broker = remoteapi.NewBroker(remoteapi.BrokerDeps{
				Check: router.Check, Send: router.Send, Cancel: manager.Cancel,
			}, remoteapi.BrokerOptions{ApprovalTimeout: s.approvalTimeout, OnApproval: asked, OnApprovalResolved: resolved,
				OnTurnEnd: func(string) { dispatcher.Kick() }})
			claude := &provider.Claude{}
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
				ProjectsDir: projects, RegistryDir: registry, Broker: broker,
				Discover: history.Discover, ParseTrace: claude.ParseTrace, ParseTail: claude.ParseTraceTail, Owned: manager.Owns,
				Repo:    gitremote.Lookup,
				Created: &remoteapi.CreatedStore{Path: filepath.Join(home, ".aimux", "remote-created.json")},
				Running: manager.Running,
				// Keyword ranking: every hit shows where the words matched, and a
				// query nothing contains finds nothing. The index is refreshed in
				// the background (below); refreshing per query takes seconds.
				SearchIndex: func(ctx context.Context, q string, limit int, ids []string) ([]search.Result, error) {
					rs, _, err := reader.Query(ctx, q, search.QueryOpts{Mode: search.ModeKeyword, Limit: limit, IncludeAutomated: true, NoRefresh: true, IDs: ids})
					return rs, err
				},
			}
			// Terminal input: the service reaches a session through the same
			// registry rule (live holder, not aimux's own) that makes it
			// open_in_terminal.
			drivers, closeDrivers := terminalDrivers()
			defer closeDrivers()
			terminal, closeLog, err := buildTerminalInput(cfg.RemoteAccess.TerminalInput, home, index, drivers...)
			if err != nil {
				return err
			}
			defer closeLog()
			statusCache := remoteapi.NewStatusCache(terminal, time.Second) // detail reads only; sends read fresh
			index.Terminal = statusCache
			api := &remoteapi.API{Version: cmd.Root().Version, TerminalInput: terminal, TerminalEnabled: cfg.RemoteAccess.TerminalInput.Enabled,
				Links:         &remoteapi.Links{JiraBase: cfg.RemoteAccess.Links.JiraBase, JiraProjects: cfg.RemoteAccess.Links.JiraProjects},
				TerminalTyped: statusCache.Invalidate, Log: cmd.ErrOrStderr(), Client: webclient.Handler(), Broker: broker, Sessions: index.Sessions, Session: index.Session,
				Search: index.Search, Projects: index.Projects,
				Create: func(ctx context.Context, cwd, text string) (string, error) {
					id, err := manager.New(ctx, cwd)
					if err != nil {
						return "", err
					}
					if err := index.Created.Add(id, cwd, text); err != nil {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err) // the session works; it may only be hidden from the list
					}
					return id, nil
				},
			}

			if pushes.devices != nil {
				api.RegisterDevice, api.UnregisterDevice = pushes.devices.Add, pushes.devices.Remove
			}
			deviceLogin, closeAudit, err := buildDeviceLogin(cfg.RemoteAccess.DeviceLoginOn(), home, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			defer closeAudit()
			api.DeviceLogin = deviceLogin
			var audit chatqueue.Auditor
			if deviceLogin != nil {
				audit = deviceLogin.Audit()
			}
			queue, d, err := openChatQueue(home, cfg.RemoteAccess.TerminalInput, broker, index, terminal, audit, statusCache.Invalidate, api.PublishQueue)
			if err != nil {
				return err
			}
			defer queue.Close()
			dispatcher = d
			api.Queue, api.QueueKick, api.QueueNote, api.SessionExists = queue, d.Kick, d.Note, index.SessionExists
			// Titles and descriptions: "Update summary" always; automatic
			// naming of the sessions clients show only with sessions.auto_title.
			namer := history.NewNamer(namingConfig(cfg.Sessions, home, index.Lookup, cmd.ErrOrStderr()))
			api.Record, api.Naming, api.AutoName = index.Record, namer, cfg.Sessions.AutoTitle
			lastFocus := buildLastFocus(cfg.RemoteAccess.TerminalInput, index.TerminalPIDs, drivers...)
			if lastFocus != nil {
				api.LastOnMac = lastFocus.Last
			}
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
			if err != nil {
				return fmt.Errorf("listen on 127.0.0.1:%d: %w (choose another port with --port)", s.port, err)
			}
			srv := &http.Server{Handler: api.Handler(s.allowed, hosts), ReadHeaderTimeout: 10 * time.Second}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if deviceLogin != nil {
				go deviceLogin.Watch(ctx) // ends streams of revoked or expired logins
			}
			go api.QueueEvents(ctx)
			queueDone := make(chan struct{})
			go func() { defer close(queueDone); dispatcher.Run(ctx) }()
			namerDone := make(chan struct{})
			go func() {
				defer close(namerDone)
				if cfg.Sessions.AutoTitle {
					namer.Run(ctx)
				}
			}()
			focusDone := make(chan struct{})
			go func() {
				defer close(focusDone)
				if lastFocus != nil {
					lastFocus.Run(ctx, lastFocusEvery)
				}
			}()
			// The dispatcher, the namer and Last on your Mac stop before the
			// terminal drivers close (deferred earlier, so run later).
			defer func() { stop(); <-queueDone; <-namerDone; <-focusDone }()
			go func() {
				t := time.NewTicker(time.Minute)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						manager.Reap()
					}
				}
			}()
			go func() {
				for {
					if err := searcher.Refresh(ctx); err != nil && ctx.Err() == nil {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err)
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Minute):
					}
				}
			}()
			shutDown := make(chan struct{})
			go func() {
				defer close(shutDown)
				<-ctx.Done()
				shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(shut)
			}()
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "aimux remote API on http://%s/v1 for %v via %v\n%s", ln.Addr(), s.allowed, hosts, tailscaleServeHint(s.port, hosts))
			if len(pushes.notifiers) > 0 {
				_, _ = fmt.Fprintf(out, "Approval notifications: %s\n", pushSummary(cfg.RemoteAccess.Push))
			}
			if cfg.RemoteAccess.TerminalInput.Enabled {
				_, _ = fmt.Fprintf(out, "Terminal input: on (%d terminal driver(s)); run `aimux remote terminal-input check` once at the Mac\n", len(drivers))
			}
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			<-shutDown // in-flight requests end before the terminal drivers close
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "Port on 127.0.0.1 (default 7382, or remote_access.port)")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "Tailscale logins to admit (overrides remote_access.allowed_logins)")
	return cmd
}

// terminalGOOS is runtime.GOOS; tests set it. Terminal input is macOS only.
var terminalGOOS = runtime.GOOS

func newRemoteTerminalInputCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "terminal-input",
		Short: "Typing phone messages into Claude Code sessions open in iTerm2",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Ask macOS for Automation access to iTerm2 and show which sessions can be reached",
		Long: "Run once at the Mac after enabling remote_access.terminal_input: it lists iTerm2's sessions " +
			"(one harmless Apple Event, so macOS asks for Automation access while you are there), then shows, " +
			"for each Claude session open in a terminal, its short id, its tty and whether aimux can reach its pane. " +
			"It never types, reads a screen or changes a window.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if terminalGOOS != "darwin" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Terminal input needs macOS and iTerm2; nothing to check on "+terminalGOOS+".")
				return nil
			}
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return fmt.Errorf("read %s: %w", config.DefaultPath(), err)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			drv := iterm2.New(iterm2.Options{})
			defer func() { _ = drv.Close() }()
			owned := func(pid int) bool { return aimuxAncestor(pid, discovery.GetParentPID, processName) }
			return runTerminalInputCheck(cmd.Context(), cmd.OutOrStdout(), terminalCheck{
				Enabled:  cfg.RemoteAccess.TerminalInput.Enabled,
				Sessions: openTerminalSessions(filepath.Join(home, ".claude", "sessions"), owned),
				TTY:      terminput.SystemProbe{}.TTY,
				Probe:    drv.Probe,
				Driver:   drv,
			})
		},
	})
	return cmd
}

// terminalSession is a Claude session held by a terminal process.
type terminalSession struct {
	ID  string
	PID int
}

// terminalCheck is what `aimux remote terminal-input check` reports on.
type terminalCheck struct {
	Enabled  bool
	Sessions []terminalSession
	TTY      func(pid int) (string, error)
	Probe    func(ctx context.Context) error // one harmless Apple Event
	Driver   terminput.Driver
}

const checkTimeout = 5 * time.Second

// runTerminalInputCheck prints whether input is on, the Automation result and,
// per session, its short id, tty and whether the driver reaches its pane.
// It prints no titles and no text.
func runTerminalInputCheck(ctx context.Context, out io.Writer, c terminalCheck) error {
	if c.Enabled {
		_, _ = fmt.Fprintln(out, "Terminal input: on")
	} else {
		_, _ = fmt.Fprintln(out, "Terminal input: off (set remote_access.terminal_input.enabled: true in ~/.aimux/config.yaml)")
	}
	pctx, cancel := context.WithTimeout(ctx, checkTimeout)
	perr := c.Probe(pctx)
	cancel()
	denied := errors.Is(perr, terminput.ErrAutomationDenied)
	switch {
	case perr == nil:
		_, _ = fmt.Fprintln(out, "iTerm2 Automation: allowed")
	case denied:
		_, _ = fmt.Fprintln(out, "iTerm2 Automation: denied; allow it in System Settings > Privacy & Security > Automation, then run this again")
	default:
		_, _ = fmt.Fprintf(out, "iTerm2 Automation: error: %s\n", firstLine(perr))
	}
	_, _ = fmt.Fprintf(out, "Open Claude sessions: %d\n", len(c.Sessions))
	for _, s := range c.Sessions {
		tty, err := c.TTY(s.PID)
		reach := ""
		switch {
		case err != nil || tty == "":
			tty, reach = "-", "not in a terminal"
		case denied:
			reach = "unknown (Automation denied)"
		default:
			lctx, cancel := context.WithTimeout(ctx, checkTimeout)
			_, found, err := c.Driver.Locate(lctx, terminput.Target{PID: s.PID, TTY: tty})
			cancel()
			switch {
			case err != nil:
				reach = "error: " + firstLine(err)
			case found:
				reach = "reachable"
			default:
				reach = "not in iTerm2"
			}
		}
		_, _ = fmt.Fprintf(out, "  %-8s  %-12s  %s\n", shortID(s.ID), tty, reach)
	}
	return nil
}

// firstLine cuts an error from the driver or osascript to its first line,
// at most 200 bytes, so a long AppleScript error stays one row.
func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// openTerminalSessions lists the registry's live sessions that aimux does not
// own, sorted by id.
func openTerminalSessions(registryDir string, owned func(pid int) bool) []terminalSession {
	var out []terminalSession
	for pid, e := range discovery.ReadClaudeRegistry(registryDir) {
		if discovery.IsLiveHolder(e) && !owned(pid) {
			out = append(out, terminalSession{ID: e.SessionID, PID: pid})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// aimuxAncestor reports whether an aimux process is among pid's ancestors
// (up to 10 levels): such a Claude runs under aimux, not in the owner's
// terminal. It is a display heuristic for the check command only and must
// never gate typing; the server decides ownership with Manager.Owns.
func aimuxAncestor(pid int, parent func(int) int, comm func(int) string) bool {
	seen := map[int]bool{}
	for p, depth := parent(pid), 0; p > 1 && depth < 10 && !seen[p]; p, depth = parent(p), depth+1 {
		seen[p] = true
		if filepath.Base(comm(p)) == "aimux" {
			return true
		}
	}
	return false
}

func processName(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output() // #nosec G204 -- pid is an int
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
