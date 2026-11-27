package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"encoding/json"
	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/ledger"
	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/secretref"
	"io"
)

func newModelCmd() *cobra.Command {
	model := &cobra.Command{Use: "model", Short: "Work with model APIs"}
	proxy := &cobra.Command{Use: "proxy", Short: "Expose a local Anthropic Messages API"}
	proxy.AddCommand(newModelProxyServeCmd(), newModelProxyStatusCmd(), newModelProxyStatsCmd(), newModelProxyEnvCmd(), newModelProxyRouteCmd(), newModelProxyDoctorCmd())
	model.AddCommand(proxy)
	return model
}

// messagesUpstream is one --messages-upstream with where its key is kept.
type messagesUpstream struct {
	name, address string
	ref           *secretref.Ref // nil: the secret-store entry <name>-key
}

// serveEngineLines is what a starting proxy says of its engines: each one it answers with and how, and Anthropic
// as off when it is. Codex is a program and reports its own start; the others pass requests on and have nothing
// to start, so without these lines a flag that took effect looked the same as one that did not.
func serveEngineLines(anthropic bool, upstreams []messagesUpstream) []string {
	width := len("anthropic")
	for _, u := range upstreams {
		width = max(width, len(u.name))
	}
	row := func(name, state, what string) string {
		return fmt.Sprintf("  %-*s  %-3s  %s", width, name, state, what)
	}
	lines := []string{"Engines", row("codex", "on", "the Codex program on this machine, on your ChatGPT plan; its start is reported below")}
	if anthropic {
		lines = append(lines, row("anthropic", "on", "Claude models go to "+modelproxy.AnthropicUpstream+" on the caller's own Claude login; nothing to start"))
	} else {
		lines = append(lines, row("anthropic", "off", "start with --anthropic, or for the everyday proxy: aimux engine enable anthropic"))
	}
	for _, u := range upstreams {
		lines = append(lines, row(u.name, "on", u.name+":<model> goes to "+u.address+" with the key kept for it"))
	}
	return lines
}

// parseMessagesUpstreams reads the two repeated flags. A name given twice, a key for a name that is not an
// upstream, and a reference that is not one are refused before anything starts.
func parseMessagesUpstreams(pairs, keys []string) ([]messagesUpstream, error) {
	var upstreams []messagesUpstream
	index := map[string]int{}
	for _, pair := range pairs {
		name, address, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("--messages-upstream %q: give it as name=address, for example enmaas=https://gateway.example", pair)
		}
		if _, twice := index[name]; twice {
			// One name is one key; two addresses under it would share it.
			return nil, fmt.Errorf("--messages-upstream: the name %q is given twice", name)
		}
		index[name] = len(upstreams)
		upstreams = append(upstreams, messagesUpstream{name: name, address: address})
	}
	for _, pair := range keys {
		name, text, ok := strings.Cut(pair, "=")
		at, known := index[name]
		if !ok || !known {
			return nil, fmt.Errorf("--messages-upstream-key: give it as name=reference, for a name that --messages-upstream also gives")
		}
		var ref secretref.Ref
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.DisallowUnknownFields()
		// What follows the name may be a key pasted by mistake, so it is never repeated in the error.
		var extra json.RawMessage
		if decoder.Decode(&ref) != nil || ref.Validate() != nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
			return nil, fmt.Errorf(`--messages-upstream-key for %s: this says where the key is kept, never the key itself, for example {"keychain":{"item":"%s-key"}}`, name, name)
		}
		if upstreams[at].ref != nil {
			return nil, fmt.Errorf("--messages-upstream-key: the name %q is given twice", name)
		}
		upstreams[at].ref = &ref
	}
	return upstreams, nil
}

// key reads the upstream's key when a request needs it. Each read is a fresh one: the proxy keeps the key for a
// minute itself and asks again when the upstream refuses it, so nothing here may answer from a cache of its own,
// and no key that was rotated away stays in memory.
func (u messagesUpstream) key() func(context.Context) (string, error) {
	if u.ref == nil {
		return keyring.System(u.name + "-key").Lookup
	}
	return func(ctx context.Context) (string, error) {
		secrets := secretref.NewResolver(secretref.Options{})
		values, err := secrets.Resolve(ctx, u.name, secretref.Refs{Headers: map[string]secretref.Ref{"x-api-key": *u.ref}})
		if err != nil {
			return "", err
		}
		return values.Headers["x-api-key"], nil
	}
}

// keyAt says where the key is kept, in words; empty for the default entry.
func (u messagesUpstream) keyAt() string {
	switch {
	case u.ref == nil:
		return ""
	case u.ref.Keychain != nil:
		return "the secret-store entry " + u.ref.Keychain.Item
	case u.ref.Env != nil:
		return "the environment variable " + u.ref.Env.Name
	case u.ref.File != nil:
		return "the file " + u.ref.File.Path
	default:
		return "the command that prints it"
	}
}

func newModelProxyStatusCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether the model proxy is answering, and how its last turn and Codex check went",
		Long: "Reads the running proxy's /health on 127.0.0.1 and prints: the proxy's own state and uptime, whether " +
			"its Codex app-server is running, how many turns it has sent to Codex and how the last one went, how many " +
			"calls it passed on to each other engine since it started (from its request history), and the " +
			"same for the rate-limit checks behind the HUD's usage file (they cost no tokens, so a recent one proves " +
			"the Codex login still works). Nothing here starts Codex or spends tokens. Exits non-zero when the proxy " +
			"is degraded or not running, so a monitor can act on the exit code.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			key, err := keyring.Stored(modelproxy.KeyService).Lookup(cmd.Context())
			if err != nil {
				if errors.Is(err, keyring.ErrNotFound) {
					return fmt.Errorf("no proxy key in the %s secret-store entry yet; start the proxy once with: aimux model proxy serve", modelproxy.KeyService)
				}
				return fmt.Errorf("read the model proxy key: %w", err)
			}
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			history := ""
			if home, err := os.UserHomeDir(); err == nil {
				history = modelproxy.RequestLogFile(home, port)
			}
			return modelProxyStatus(cmd.Context(), addr, key, history, cmd.OutOrStdout(), time.Now)
		},
	}
	cmd.Flags().IntVar(&port, "port", modelproxy.StablePort, "Port the proxy listens on at 127.0.0.1")
	return cmd
}

func newModelProxyStatsCmd() *cobra.Command {
	var port int
	var since time.Duration
	var session string
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Summarise the proxy's recent requests: how each kind went and how long it took",
		Long: "Reads the request history the proxy writes (one JSON line per answered request, no prompts or " +
			"answers) and prints a row per outcome, slowest first: how many, the mean and 95th percentile time, " +
			"the average prompt size and how many failed. Fallbacks also print what they say about themselves, " +
			"which is where the next fix usually hides. When the window holds more than one conversation it also " +
			"lists them, costliest first, by a short one-way tag; --session takes a session id (hashed to its " +
			"tag) or the tag itself. Reads files only, so the proxy need not be running.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find home folder for the request log: %w", err)
			}
			return modelProxyStats(modelproxy.RequestLogFile(home, port), since, session, cmd.OutOrStdout(), time.Now)
		},
	}
	cmd.Flags().IntVar(&port, "port", modelproxy.StablePort, "Which listener's history to read")
	cmd.Flags().DurationVar(&since, "since", 24*time.Hour, "How far back to summarise, such as 2h or 168h")
	cmd.Flags().StringVar(&session, "session", "", "Only one conversation: a Claude Code session id, or the tag the history shows")
	return cmd
}

func newModelProxyServeCmd() *cobra.Command {
	var port int
	var command, serviceTier, onLimit string
	var anthropic bool
	var messagesUpstreams, messagesUpstreamKeys []string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the Codex model proxy on 127.0.0.1",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if serviceTier != modelproxy.ServiceTierPriority && serviceTier != modelproxy.ServiceTierStandard {
				cmd.SilenceUsage = true
				return fmt.Errorf("unknown --service-tier %q: choose %s or %s",
					serviceTier, modelproxy.ServiceTierPriority, modelproxy.ServiceTierStandard)
			}
			if onLimit != "" && onLimit != modelproxy.OnLimitReport {
				cmd.SilenceUsage = true
				return fmt.Errorf("unknown --on-limit %q: the one value in this build is %s", onLimit, modelproxy.OnLimitReport)
			}
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			ln, err := net.Listen("tcp4", addr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w (choose another port with --port)", addr, err)
			}
			defer func() { _ = ln.Close() }()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			key, err := modelproxy.EnsureKey(ctx, keyring.Stored(modelproxy.KeyService), func(k string) error {
				return keyring.Store(modelproxy.KeyService, k)
			})
			if err != nil {
				return err
			}
			backend := modelproxy.NewCodexBackend(modelproxy.CodexOptions{
				Command: command, ServiceTier: serviceTier, Stderr: cmd.ErrOrStderr(), Version: rootCmd.Version,
			})
			defer backend.Close()
			// Codex starts beside the listener, not before it: a slow or failed start must not keep the proxy
			// from answering, and Start reports either outcome itself.
			go func() { _ = backend.Start(ctx) }()
			var handlerOptions []modelproxy.HandlerOption
			if home, err := os.UserHomeDir(); err == nil {
				// The bound port, not the flag: --port 0 asks the OS for one, and two such listeners must not
				// share a history file.
				bound := port
				if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
					bound = tcp.Port
				}
				path := modelproxy.RequestLogFile(home, bound)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "model proxy: no request history: %v\n", err)
				}
				// A proxy that cannot keep its history should still answer: say so and serve on.
				if requests, err := modelproxy.OpenRequestLog(path, modelproxy.RequestLogLimit); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "model proxy: no request history: %v\n", err)
				} else {
					defer func() { _ = requests.Close() }()
					handlerOptions = append(handlerOptions, modelproxy.WithRequestLog(requests))
				}
			}
			// The ledger: one line per model call. Like the request history, a proxy that cannot keep it says so
			// and serves on; unlike it, health then reports the record as off.
			if home, err := os.UserHomeDir(); err == nil {
				bound := port
				if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
					bound = tcp.Port
				}
				dir, name := filepath.Join(home, ".aimux", "ledger"), modelproxy.LedgerName(bound)
				if writer, err := ledger.OpenWriter(dir, name); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "model proxy: no ledger: %v\n", err)
					handlerOptions = append(handlerOptions, modelproxy.WithLedgerOff())
				} else {
					defer func() { _ = writer.Close() }()
					// Each session's turn number goes on from this writer's own earlier lines.
					// Only the end of this writer's own file, so the proxy does not start slower as the file grows.
					earlier, _, err := ledger.ReadTail(dir, name, 8<<20)
					if err != nil {
						// The numbers then start from what each request holds, which a compacted session's
						// request no longer does in full.
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "model proxy: turn numbers not read from the ledger: %v\n", err)
					}
					handlerOptions = append(handlerOptions, modelproxy.WithLedger(writer, earlier...))
				}
			} else {
				handlerOptions = append(handlerOptions, modelproxy.WithLedgerOff())
			}
			if onLimit != "" {
				handlerOptions = append(handlerOptions, modelproxy.WithOnLimit(onLimit))
			}
			// Each Codex model's window, from Codex's own list, read when asked: the list is refreshed by Codex.
			if userHome, err := os.UserHomeDir(); err == nil {
				cache := modelproxy.CodexModelsCache(userHome, os.Getenv)
				handlerOptions = append(handlerOptions, modelproxy.WithCodexWindows(func(model string) int {
					window, _ := modelproxy.CodexContextWindow(cache, model)
					return window
				}))
			}
			if anthropic {
				option, err := modelproxy.WithAnthropic(modelproxy.AnthropicUpstream)
				if err != nil {
					return err
				}
				handlerOptions = append(handlerOptions, option)
			}
			upstreams, err := parseMessagesUpstreams(messagesUpstreams, messagesUpstreamKeys)
			if err != nil {
				return err
			}
			for _, upstream := range upstreams {
				option, err := modelproxy.WithMessagesUpstream(upstream.name, upstream.address, upstream.key(), upstream.keyAt())
				if err != nil {
					return err
				}
				handlerOptions = append(handlerOptions, option)
			}
			if port == modelproxy.StablePort {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("find home folder for the Codex usage file: %w", err)
				}
				go modelproxy.PollUsage(ctx, backend.ReadUsage, modelproxy.UsageFile(home, port), 2*time.Minute, cmd.ErrOrStderr())
			}
			server := &http.Server{
				Handler:           modelproxy.NewHandler(backend, key, handlerOptions...),
				ReadHeaderTimeout: 5 * time.Second,
				BaseContext:       func(net.Listener) context.Context { return ctx },
			}
			stopServer := context.AfterFunc(ctx, func() { _ = server.Close() })
			defer stopServer()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Model proxy at http://%s\n%s\n", ln.Addr(), strings.Join(serveEngineLines(anthropic, upstreams), "\n"))
			if err := server.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", modelproxy.StablePort, "Port on 127.0.0.1 (clients send the key stored in the "+modelproxy.KeyService+" Keychain entry)")
	cmd.Flags().StringVar(&command, "codex-command", "", "Codex executable (default: codex-lean if available, otherwise codex)")
	cmd.Flags().BoolVar(&anthropic, "anthropic", false,
		"Also forward to Anthropic: a request that carries the caller's own Claude login and names a plain model (not codex:<model>, not <name>:<model> for a --messages-upstream) goes to "+modelproxy.AnthropicUpstream+" unchanged. Requests with only the proxy key and a plain model name are answered by Codex as before, or by a --messages-upstream when they name one. A model named with a prefix that is not an engine that is on is refused with HTTP 400")
	cmd.Flags().StringArrayVar(&messagesUpstreams, "messages-upstream", nil,
		"Another address that speaks the Messages API, as name=address (may be repeated). A request naming a model as <name>:<model> with the proxy key is sent there, with that upstream's key and none of the caller's credentials. The key is read from the secret-store entry <name>-key unless --messages-upstream-key says where else it is kept. A plain model name, with no prefix, still goes to Codex or Anthropic")
	cmd.Flags().StringArrayVar(&messagesUpstreamKeys, "messages-upstream-key", nil,
		`Where a --messages-upstream's key is kept, as name=reference, when it is not the secret-store entry <name>-key. The reference is the JSON form the config file's key takes, such as enmaas={"env":{"name":"ENMAAS_KEY"}}; it is never the key itself`)
	cmd.Flags().StringVar(&onLimit, "on-limit", "", "What to do when a plan limit comes back from an engine: report writes, on the call's ledger line, what a fallback to another engine would have done, and changes nothing. Empty: nothing")
	cmd.Flags().StringVar(&serviceTier, "service-tier", modelproxy.ServiceTierPriority,
		"OpenAI service tier: priority is Codex's Fast tier (about twice the tokens a second, spending the plan's allowance faster); standard leaves Codex's own default")
	return cmd
}
