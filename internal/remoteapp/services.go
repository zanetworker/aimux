package remoteapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/catalog"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/discovery"
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
