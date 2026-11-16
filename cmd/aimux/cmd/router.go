package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/autoroute"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/decide"
)

// routerSchemaVersion is the schema of every `aimux router ... --json` document.
const routerSchemaVersion = "1"

// routerProfiles are the profiles `router test` lets the router pick from:
// all three modes, as an owner-scoped client would see them.
var routerProfiles = []string{"page", "research", "work-readonly"}

// routerFacts maps a --facts word to its field.
var routerFacts = map[string]func(*autoroute.Facts){
	"site-private":  func(f *autoroute.Facts) { f.SitePrivate = true },
	"page-readable": func(f *autoroute.Facts) { f.PageReadable = true },
	"use-page":      func(f *autoroute.Facts) { f.UsePage = true },
}

// routerEnv holds what the router commands touch outside the process.
type routerEnv struct {
	Home  string
	GOOS  string
	Probe keyProbe // key sources, by existence only
	// Build makes the decider for cfg writing to log (decide.Build with the
	// real key chain by default).
	Build func(cfg decide.DeciderConfig, log *decide.Log) (*decide.Decider, error)
}

func newRouterCmd() *cobra.Command {
	home, _ := os.UserHomeDir()
	return newRouterCmdWith(routerEnv{Home: home, GOOS: runtime.GOOS, Probe: defaultKeyProbe(home), Build: routerBuild(func() string {
		cfg, _ := config.Load(config.DefaultPath()) // no config: the environment's key, else claude -p
		return cfg.Sessions.APIKey
	})})
}

// routerBuild is the real mode-router builder: the config's key chain for
// openrouter backends, and for chat backends the model client on the
// Anthropic key apiKey returns (sessions.api_key), read only when building.
func routerBuild(apiKey func() string) func(cfg decide.DeciderConfig, log *decide.Log) (*decide.Decider, error) {
	return func(cfg decide.DeciderConfig, log *decide.Log) (*decide.Decider, error) {
		return decide.Build(autoroute.DeciderName, cfg, decide.Deps{Log: log, LLM: decide.ChatClient(apiKey())})
	}
}

func newRouterCmdWith(env routerEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "router",
		Short: "Inspect and try Auto mode's mode router: status and test",
		Long: "Auto mode asks a small model which mode (page, web or workspace) a question needs. status shows how the\n" +
			"router is set up; test routes one question for real, with your key, and never runs the question itself.",
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the mode router: backends, thresholds, config file and whether a key is found; read-only",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return routerStatus(env, c.OutOrStdout(), c.ErrOrStderr())
		},
	}
	var facts string
	test := &cobra.Command{
		Use:   "test <question>",
		Short: "Route one question with the real router and decider (calls the model once; the question is not run)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			f, err := parseRouterFacts(facts)
			if err != nil {
				return err
			}
			return routerTest(c, env, args[0], f)
		},
	}
	test.Flags().StringVar(&facts, "facts", "", "Comma-separated facts to send: site-private, page-readable, use-page")
	cmd.AddCommand(status, test)
	return cmd
}

func parseRouterFacts(s string) (autoroute.Facts, error) {
	var f autoroute.Facts
	for _, w := range strings.Split(s, ",") {
		if w = strings.TrimSpace(w); w == "" {
			continue
		}
		set, ok := routerFacts[w]
		if !ok {
			names := make([]string, 0, len(routerFacts))
			for n := range routerFacts {
				names = append(names, n)
			}
			sort.Strings(names)
			return f, fmt.Errorf("unknown fact %q in --facts (use: %s)", w, strings.Join(names, ", "))
		}
		set(&f)
	}
	return f, nil
}

func (e routerEnv) configPath() string {
	return filepath.Join(e.Home, ".config", "aimux", "deciders.yaml")
}

type routerBackend struct {
	Kind      string  `json:"kind"`
	Model     string  `json:"model"`
	Threshold float64 `json:"threshold"`
}

type routerKeyDoc struct {
	Found  bool   `json:"found"`
	Source string `json:"source"`
	Line   string `json:"line"`
}

type routerStatusDoc struct {
	SchemaVersion string          `json:"schema_version"`
	Decider       string          `json:"decider"`
	ConfigPath    string          `json:"config_path"`
	ConfigPresent bool            `json:"config_present"`
	ConfigValid   bool            `json:"config_valid"`
	ConfigError   string          `json:"config_error,omitempty"`
	Threshold     float64         `json:"threshold,omitempty"`
	TimeoutMs     int64           `json:"timeout_ms,omitempty"`
	Backends      []routerBackend `json:"backends"`
	Key           routerKeyDoc    `json:"key"`
}

func routerStatus(env routerEnv, out, errOut io.Writer) error {
	doc := routerStatusDoc{SchemaVersion: routerSchemaVersion, Decider: autoroute.DeciderName, ConfigPath: env.configPath(), Backends: []routerBackend{}}
	if _, err := os.Stat(doc.ConfigPath); err == nil {
		doc.ConfigPresent = true
	}
	keyCfg := autoroute.DefaultDecider().Key
	cfg, err := autoroute.LoadDecider(doc.ConfigPath)
	if err != nil {
		doc.ConfigError = err.Error()
	} else {
		doc.ConfigValid, keyCfg = true, cfg.Key
		doc.Threshold, doc.TimeoutMs = cfg.Threshold, cfg.Timeout.Milliseconds()
		for _, b := range cfg.Backends {
			t := cfg.Threshold
			if b.Threshold != nil {
				t = *b.Threshold
			}
			doc.Backends = append(doc.Backends, routerBackend{Kind: b.Kind, Model: b.Model, Threshold: t})
		}
	}
	ks := autoKeyStatus(keyCfg, env.Probe, env.GOOS)
	doc.Key = routerKeyDoc(ks)

	if jsonOutput {
		return writeRouterJSON(out, doc)
	}
	p := func(label, v string) { _, _ = fmt.Fprintf(out, "%-10s %s\n", label+":", v) }
	p("decider", doc.Decider)
	switch {
	case !doc.ConfigValid:
		p("config", doc.ConfigPath+" INVALID: "+doc.ConfigError)
	case doc.ConfigPresent:
		p("config", doc.ConfigPath+" (valid)")
	default:
		p("config", doc.ConfigPath+" (absent; built-in defaults, valid)")
	}
	if doc.ConfigValid {
		p("timeout", (time.Duration(doc.TimeoutMs) * time.Millisecond).String())
		for i, b := range doc.Backends {
			p(fmt.Sprintf("backend %d", i+1), fmt.Sprintf("%s %s, decided at confidence >= %g", b.Kind, b.Model, b.Threshold))
		}
	}
	p("key", doc.Key.Line)
	switch {
	case !doc.ConfigValid:
		_, _ = fmt.Fprintf(errOut, "Next: fix %s (Auto asks you every time while it is invalid), then aimux router status\n", doc.ConfigPath)
	case !doc.Key.Found:
		_, _ = fmt.Fprintln(errOut, "Next: store the OpenRouter key: security add-generic-password -U -a \"$USER\" -s aimux-openrouter -w (then aimux router test \"<question>\")")
	}
	return nil
}

func writeRouterJSON(out io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, string(b))
	return nil
}

type routerTestDoc struct {
	SchemaVersion string             `json:"schema_version"`
	Test          bool               `json:"test"`
	Facts         autoroute.Facts    `json:"facts"`
	Mode          string             `json:"mode"`
	Profile       string             `json:"profile"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Decided       bool               `json:"decided"`
	Source        string             `json:"source"`
	Reason        string             `json:"reason"`
	Backend       string             `json:"backend"`
	Model         string             `json:"model"`
	Ms            int64              `json:"ms"`
	CostUSD       *float64           `json:"cost_usd"`
	DecisionID    string             `json:"decision_id,omitempty"`
}

func routerTest(c *cobra.Command, env routerEnv, question string, f autoroute.Facts) error {
	path := env.configPath()
	cfg, err := autoroute.LoadDecider(path)
	if err != nil {
		return fmt.Errorf("cannot route with this config (aimux router status shows it): %w", err)
	}
	d, err := env.Build(cfg, &decide.Log{Path: filepath.Join(env.Home, ".aimux", "logs", "decisions.jsonl")})
	if err != nil {
		return fmt.Errorf("build the mode router: %w", err)
	}
	d.Test = true // the log line says so, and shadow evaluation leaves it out
	res := autoroute.Route(c.Context(), d, question, f, routerProfiles)
	doc := routerTestDoc{SchemaVersion: routerSchemaVersion, Test: true, Facts: f, Mode: res.Mode, Profile: res.Profile,
		Confidence: res.Confidence, Probabilities: res.Probabilities, Decided: res.Decided, Source: res.Source, Reason: res.Reason,
		Backend: res.Backend, Model: res.Model, Ms: res.Ms, CostUSD: res.CostUSD, DecisionID: res.DecisionID}
	if doc.Probabilities == nil {
		doc.Probabilities = map[string]float64{}
	}
	out, errOut := c.OutOrStdout(), c.ErrOrStderr()
	if jsonOutput {
		if err := writeRouterJSON(out, doc); err != nil {
			return err
		}
	} else {
		printRouterTest(out, doc)
	}
	switch {
	case slices.Contains([]string{decide.UndecidedNoKey, decide.UndecidedKeyError}, res.Reason):
		_, _ = fmt.Fprintln(errOut, "Next: aimux router status shows which key source is missing")
	case res.Reason == decide.UndecidedTimeout || res.Reason == decide.UndecidedError:
		_, _ = fmt.Fprintln(errOut, "Next: check aimux router status and your network; Auto asks you when the router cannot answer")
	}
	return nil
}

func printRouterTest(out io.Writer, d routerTestDoc) {
	p := func(label, v string) { _, _ = fmt.Fprintf(out, "%-15s %s\n", label+":", v) }
	or := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	p("mode", or(d.Mode))
	p("profile", or(d.Profile))
	p("confidence", fmt.Sprintf("%.2f", d.Confidence))
	keys := make([]string, 0, len(d.Probabilities))
	for k := range d.Probabilities {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %.2f", k, d.Probabilities[k]))
	}
	p("probabilities", or(strings.Join(parts, ", ")))
	p("decided", fmt.Sprint(d.Decided))
	p("source", or(d.Source))
	p("reason", or(d.Reason))
	p("backend", or(d.Backend))
	p("model", or(d.Model))
	p("ms", fmt.Sprint(d.Ms))
	cost := "-"
	if d.CostUSD != nil {
		cost = fmt.Sprintf("$%.6f", *d.CostUSD)
	}
	p("cost", cost)
}
