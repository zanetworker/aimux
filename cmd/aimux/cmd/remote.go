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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/remoteclient"
	"github.com/zanetworker/aimux/internal/search"
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

func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "API for your sessions from other devices, through tailscale serve",
	}
	cmd.AddCommand(newRemoteServeCmd())
	return cmd
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
				d, err := index.Session(ctx, id, 1)
				if err != nil {
					return "" // a session not written yet: the tool alone still says what is asked
				}
				return d.Title
			}
			asked, resolved := approvalHooks(pushes, sessionTitle, cmd.ErrOrStderr())

			var broker *remoteapi.Broker
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
			}, remoteapi.BrokerOptions{ApprovalTimeout: s.approvalTimeout, OnApproval: asked, OnApprovalResolved: resolved})
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
			api := &remoteapi.API{Version: cmd.Root().Version, Client: remoteclient.Handler(), Broker: broker, Sessions: index.Sessions, Session: index.Session,
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
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
			if err != nil {
				return fmt.Errorf("listen on 127.0.0.1:%d: %w (choose another port with --port)", s.port, err)
			}
			srv := &http.Server{Handler: api.Handler(s.allowed, hosts), ReadHeaderTimeout: 10 * time.Second}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
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
			go func() {
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
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "Port on 127.0.0.1 (default 7382, or remote_access.port)")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "Tailscale logins to admit (overrides remote_access.allowed_logins)")
	return cmd
}
