package cmd

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/parts"
)

// doctorDeps is what the proxy's doctor reads outside the process.
type doctorDeps struct {
	key      func(context.Context) (string, error)
	getenv   func(string) string
	home     string
	cliBuild string
	now      func() time.Time
}

func newModelProxyDoctorCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Say what is wrong with the model proxy, and the command that fixes each thing",
		Long: "Checks what breaks in practice, in one go: whether the proxy answers and is the model proxy, whether its\n" +
			"build is older than this aimux, whether it holds the stored key, whether the Codex login still works,\n" +
			"whether its engines are the ones the config file states, whether the ledger is written, how the calls of\n" +
			"the last hour went (a rate limit, a refused login, an upstream that cannot be reached), and whether the\n" +
			"shell it runs in points at an engine that is off. Each line has a state (ok, warn, fail), and where a\n" +
			"command fixes it, that command follows; a rate limit, for one, has none. It reads only: nothing is started, restarted or changed, and no\n" +
			"tokens are spent. The proxy key is sent only to a listener that first proved it holds the same key.\n" +
			"Exits 1 when something is failing; a warning alone exits 0.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			if named, err := strconv.Atoi(os.Getenv("AIMUX_MODEL_PROXY_PORT")); err == nil && named > 0 && named < 65536 && !cmd.Flags().Changed("port") {
				port = named
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find the home folder: %w", err)
			}
			// With no port named, the one the installed job starts the proxy on, as aimux status reads it.
			if !cmd.Flags().Changed("port") && port == modelproxy.StablePort {
				label := parts.MustService("model-proxy").Label
				port = parts.InstalledProxyPort(filepath.Join(home, "Library", "LaunchAgents", label+".plist"), modelproxy.StablePort)
			}
			deps := doctorDeps{
				key:    func(ctx context.Context) (string, error) { return keyring.Stored(modelproxy.KeyService).Lookup(ctx) },
				getenv: os.Getenv, home: home, cliBuild: cmd.Root().Version, now: time.Now,
			}
			return runProxyDoctor(cmd.Context(), port, deps, jsonOutput, cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&port, "port", modelproxy.StablePort, "Port the proxy listens on at 127.0.0.1")
	return cmd
}

// errProxyFailing: the doctor found something that refuses sessions.
var errProxyFailing = errors.New("the model proxy has something to fix; see above")

func runProxyDoctor(ctx context.Context, port int, d doctorDeps, asJSON bool, out io.Writer) error {
	facts, err := gatherProxyFacts(ctx, port, d)
	if err != nil {
		return err
	}
	findings := controller.DiagnoseProxy(facts)
	verdict := controller.ProxyVerdict(findings)
	if asJSON {
		if err := writeJSON(out, map[string]any{"schema_version": 1, "port": port, "verdict": verdict, "findings": findings}); err != nil {
			return err
		}
	} else {
		writeProxyFindings(out, port, findings)
	}
	if verdict == controller.FindingFail {
		return errProxyFailing
	}
	return nil
}

// gatherProxyFacts reads everything DiagnoseProxy judges. An error is something that stops the reading itself
// (a secret store that cannot be read); anything wrong with the proxy is a fact, not an error.
func gatherProxyFacts(ctx context.Context, port int, d doctorDeps) (controller.ProxyFacts, error) {
	facts := controller.ProxyFacts{Port: port, Now: d.now(), CLIBuild: d.cliBuild, Answers: controller.ProxyNoAnswer}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	secret, err := d.key(ctx)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
	case err != nil:
		return facts, fmt.Errorf("read the model proxy key: %w", err)
	default:
		facts.KeyStored = true
	}

	// The keyless answer first, with a challenge: only a listener that proves it holds the key is sent it.
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	nonce := rand.Text()
	// One connection for the proof and for what follows it: the key goes only down the connection the proof came
	// back on. A second dial is refused, since by then something else could hold the port.
	dialed := false
	transport := &http.Transport{
		MaxConnsPerHost: 1,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if dialed {
				return nil, errors.New("the proxy closed the connection it proved itself on, and the key is not sent down another")
			}
			dialed = true
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
	defer transport.CloseIdleConnections()
	live, err := challengeModelLivenessOver(probe, transport, addr, nonce)
	var dial *net.OpError
	switch {
	case err == nil && live.Service == modelproxy.LivenessService:
		facts.Answers, facts.Build, facts.Status, facts.Engines = controller.ProxyAnswers, live.Version, live.Status, live.Engines
		facts.LedgerOff, facts.LedgerFailed = live.LedgerOff, live.LedgerFailed
		switch {
		case live.Proof == "":
			// A build from before the challenge: it cannot prove anything, which is not a wrong key.
			facts.ProofUnsupported = true
		case facts.KeyStored:
			proven := modelproxy.CheckProxyIdentity(live, secret, nonce, port) == nil
			facts.KeyProven = &proven
		}
	case errors.As(err, &dial) || errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
	default:
		facts.Answers = controller.ProxyOtherListener
	}
	if facts.KeyProven != nil && *facts.KeyProven {
		health, err := readProxyHealthOver(ctx, transport, addr, secret)
		if err != nil {
			facts.HealthError = err.Error()
		} else {
			facts.HealthRead, facts.Reason = true, health.Reason
			facts.CodexRunning = health.Codex.AppServerRunning
			facts.CodexCheckOKAt, facts.CodexCheckErrorAt, facts.CodexCheckError = health.Probe.LastOKAt, health.Probe.LastErrorAt, health.Probe.LastError
			facts.LedgerOff, facts.LedgerFailed = health.Ledger.Off, health.Ledger.Failed
		}
	}

	// What the config file states of each engine it names.
	switch views, stated, err := parts.ListEngines(filepath.Join(d.home, ".aimux", "config.yaml")); {
	case err != nil:
		facts.ConfigProblem = err.Error()
	case stated:
		facts.Stated = map[string]bool{}
		for _, v := range views {
			facts.Stated[v.Name] = v.On
		}
	}

	// The last hour of calls. A history that is not there yet is no calls.
	history := modelproxy.RequestLogFile(d.home, port)
	if _, statErr := os.Stat(history); statErr == nil {
		total, failures, incomplete, err := modelproxy.ReadRequestFailures(history, time.Hour, facts.Now)
		if err != nil {
			facts.HistoryError = err.Error()
		}
		facts.Calls, facts.HistoryIncomplete = total, incomplete
		for _, f := range failures {
			facts.Failures = append(facts.Failures, controller.CallFailure{Engine: f.Engine, Status: f.Status, Error: f.Error, Count: f.Count, LastAt: f.LastAt})
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		facts.HistoryError = statErr.Error()
	}

	// The shell this runs in, when it points at this proxy.
	if route := modelproxy.RouteOf(d.getenv, ""); route.Through && route.Port == port {
		facts.ShellThrough, facts.ShellEngine = true, route.Engine
		if len(facts.Engines) > 0 {
			on := slices.Contains(facts.Engines, route.Engine)
			facts.ShellEngineOn = &on
		}
	}
	return facts, nil
}

func writeProxyFindings(out io.Writer, port int, findings []controller.ProxyFinding) {
	var fails, warns int
	for _, f := range findings {
		switch f.Level {
		case controller.FindingFail:
			fails++
		case controller.FindingWarn:
			warns++
		}
	}
	head := "Nothing to fix."
	switch {
	case fails > 0 && warns > 0:
		head = fmt.Sprintf("%s failing, %s to look at.", plural(fails, "thing is", "things are"), plural(warns, "thing", "things"))
	case fails > 0:
		head = plural(fails, "thing is", "things are") + " failing."
	case warns > 0:
		head = plural(warns, "thing", "things") + " to look at; nothing is failing."
	}
	_, _ = fmt.Fprintf(out, "Model proxy on 127.0.0.1:%d. %s\n\n", port, head)
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "CHECK\tSTATE\tDETAIL")
	for _, f := range findings {
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\n", f.ID, f.Level, f.Detail)
	}
	_ = table.Flush()
	var fixes []string
	for _, f := range findings {
		if f.Fix != "" {
			fixes = append(fixes, fmt.Sprintf("  %s: %s", f.ID, f.Fix))
		}
	}
	if len(fixes) > 0 {
		_, _ = fmt.Fprintf(out, "\nFIX\n%s\n", strings.Join(fixes, "\n"))
	}
}
