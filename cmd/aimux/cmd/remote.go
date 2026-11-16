package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	webclient "github.com/zanetworker/aimux/clients/web"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/remoteapp"
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
// defaults. It refuses to run with nobody allowed, unless localOnly (no
// tailnet listener, so nobody to allow).
func resolveRemoteSettings(cfg config.RemoteAccessConfig, port int, allow []string, localOnly bool) (remoteSettings, error) {
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
	if len(s.allowed) == 0 && !localOnly {
		return s, errors.New("nobody is allowed: set remote_access.allowed_logins in ~/.aimux/config.yaml or pass --allow <tailscale-login>")
	}
	return s, nil
}

// auditRevoke writes the revoke line for d to the audit log, as the server
// writes its own lines; the server does not see who revoked.
func auditRevoke(d devicelogin.Device, errOut io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("audit the revoke: resolve home directory: %w", err)
	}
	log, err := terminput.OpenLog(remoteapp.AuditPath(home))
	if err != nil {
		return fmt.Errorf("audit the revoke: open %s: %w", remoteapp.AuditPath(home), err)
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
	cmd.AddCommand(newRemoteServeCmd(), newRemoteTerminalInputCmd(), newRemotePairCmd(), newRemoteDevicesCmd(), newRemoteRevokeCmd(), newRemoteHooksCmd())
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
		port         int
		allow        []string
		localOnly    bool
		unsafeNoAuth bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the /v1 API on 127.0.0.1 for tailscale serve to expose",
		Long: "Serve the remote API: list Claude sessions, follow them, continue idle ones and answer " +
			"approvals from another device. It listens on 127.0.0.1 only and admits a request only when " +
			"tailscale serve forwarded it for a login in remote_access.allowed_logins. It also serves a " +
			"phone web client at /app/: after tailscale serve, open https://<this machine's tailnet name>:8443/app/. " +
			"Local clients (Wingman) get a unix socket each, as ~/.config/aimux/clients.yaml says (default " +
			"~/.aimux/clients/wingman.sock, 0600): the socket names the client, so it needs no login, and the " +
			"client is held to the profiles and limits of its scope. --local-only serves only those sockets. " +
			"Without device login the tailnet listener is refused unless --unsafe-no-device-login is passed. " +
			"A second remote serve exits at once when one is already running.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return fmt.Errorf("read %s: %w", config.DefaultPath(), err)
			}
			s, err := resolveRemoteSettings(cfg.RemoteAccess, port, allow, localOnly)
			if err != nil {
				return err
			}
			scopes, err := clientscope.Load(clientscope.DefaultPath())
			if err != nil {
				return fmt.Errorf("read %s: %w", clientscope.DefaultPath(), err)
			}
			name, adapterArgs, err := parseAdapter(s.adapter)
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			return remoteapp.New(cfg, remoteapp.Options{
				Home: home, Port: s.port, Allowed: s.allowed, AdapterCommand: name, AdapterArgs: adapterArgs,
				MaxRuns: s.maxRuns, ApprovalTimeout: s.approvalTimeout,
				LocalOnly: localOnly, UnsafeNoDeviceLogin: unsafeNoAuth, Scopes: scopes,
				Version: cmd.Root().Version, Client: webclient.Handler(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr(),
			}).Run(cmd.Context())
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "Port on 127.0.0.1 (default 7382, or remote_access.port)")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "Tailscale logins to admit (overrides remote_access.allowed_logins)")
	cmd.Flags().BoolVar(&localOnly, "local-only", false, "Serve only the local clients' sockets: no tailnet listener, no --allow, no device login")
	cmd.Flags().BoolVar(&unsafeNoAuth, "unsafe-no-device-login", false, "Run the tailnet listener with remote_access.device_login false (any local process could claim an allowed login)")
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
				Sessions: openTerminalSessions(remoteapp.Providers(home)[0].Live, owned),
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
// per session, its short id, pid, tty and whether the driver reaches its pane.
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
	_, _ = fmt.Fprintf(out, "Open Claude sessions: %d (one row per session, with the pid aimux types into)\n", len(c.Sessions))
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
		_, _ = fmt.Fprintf(out, "  %-8s  pid %-6d  %-12s  %s\n", shortID(s.ID), s.PID, tty, reach)
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

// openTerminalSessions lists the live sessions that aimux does not own
// (harness.OpenSessions), one per session with the pid the server types
// into (the highest when two processes hold it), sorted by id.
func openTerminalSessions(live harness.LiveSessions, owned func(pid int) bool) []terminalSession {
	hs, _ := live.Holders(harness.List) // the registry is optional: none open
	var out []terminalSession
	for id, h := range harness.OpenSessions(hs, owned) {
		out = append(out, terminalSession{ID: id, PID: h.PID})
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
