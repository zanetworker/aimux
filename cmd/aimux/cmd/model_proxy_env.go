package cmd

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
)

func newModelProxyEnvCmd() *cobra.Command {
	var o envOpts
	cmd := &cobra.Command{
		Use:   "env [codex-model]",
		Short: "Print the shell lines that send this shell's Claude Code sessions through the model proxy",
		Long: "Prints lines for eval. After\n\n  eval \"$(aimux model proxy env)\"\n\n" +
			"Claude Code sessions started from that shell go through the model proxy: a Claude model is answered by " +
			"Anthropic on your own Claude login, a codex: model by Codex, and a <name>: model by that named upstream when the proxy has one. It sets ANTHROPIC_BASE_URL, with the " +
			"proxy key in it, and unsets ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN so the login is used. It says " +
			"nothing about which model a session uses. It needs a running proxy that was started with --anthropic.\n\n" +
			"The output holds the proxy key: it is refused when it would be shown on a terminal, and a shell with " +
			"tracing on (set -x) would print it. When this command fails it prints nothing, and eval of nothing " +
			"succeeds, so in a script use: lines=\"$(aimux model proxy env)\" && eval \"$lines\".\n\n" +
			"Before printing, it asks whatever listens on the port to prove it holds the proxy key, without sending the key. " +
			"eval \"$(aimux model proxy env --off)\" undoes it, and needs no proxy.\n\n" +
			"With --codex the shell is set for Codex alone, as the proxy has always been used: the proxy key is the API\n" +
			"key, no Claude login is involved, and the model is pinned to codex:<model> (gpt-6.2-sol unless one is\n" +
			"named: aimux model proxy env --codex gpt-6.2-astra). That form needs no --anthropic on the proxy.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			// A port named in the shell stands when none is given here, as the function this replaces read it.
			if named, err := strconv.Atoi(os.Getenv("AIMUX_MODEL_PROXY_PORT")); err == nil && named > 0 && named < 65536 && !cmd.Flags().Changed("port") {
				o.port = named
			}
			if len(args) == 1 {
				if !o.codex {
					return errors.New("a model is named only with --codex; the other form leaves the model to the session")
				}
				o.model = args[0]
			}
			if file, ok := cmd.OutOrStdout().(*os.File); ok && !o.off && isTerminal(file) {
				again := "aimux model proxy env"
				if o.codex {
					again += " --codex"
				}
				if o.model != "" && modelproxy.ValidCodexModel(o.model) {
					again += " " + o.model
				}
				if cmd.Flags().Changed("port") {
					again += fmt.Sprintf(" --port %d", o.port)
				}
				return fmt.Errorf(`this prints the proxy key, so it is not shown on a terminal; run it as: lines="$(%s)" && eval "$lines"`, again)
			}
			return modelProxyEnv(cmd.Context(), o, keyring.Stored(modelproxy.KeyService).Lookup, os.Getenv, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().IntVar(&o.port, "port", modelproxy.StablePort, "Port the proxy listens on at 127.0.0.1 (AIMUX_MODEL_PROXY_PORT, when set, stands in for it)")
	cmd.Flags().BoolVar(&o.off, "off", false, "Print the lines that undo it")
	cmd.Flags().BoolVar(&o.codex, "codex", false, "Set the shell for Codex alone: the proxy key as the API key and the model pinned to codex:<model>")
	return cmd
}

// envOpts is how `aimux model proxy env` was asked.
type envOpts struct {
	port  int
	off   bool
	codex bool   // Codex alone, with the model pinned
	model string // the Codex model; the default when empty
}

// modelProxyEnv writes the lines to stdout and one sentence about them to stderr. Nothing reaches stdout unless
// every check passed, so an eval of a failed run changes nothing; the key is written to stdout only.
func modelProxyEnv(ctx context.Context, o envOpts, key func(context.Context) (string, error), getenv func(string) string, stdout, stderr io.Writer) error {
	port := o.port
	if o.off {
		// What to unset is read from the marker the first form left in the shell, not from the proxy key. Only a
		// shell set before there was a marker needs the key looked at, to tell whether its API key variable
		// holds it; when that cannot be told, nothing is changed and the owner is told, so a shell is never left
		// with the proxy key in it and the proxy's address gone.
		marker := getenv(modelproxy.SessionEnvMarker)
		held := false
		if marker == "" && getenv("ANTHROPIC_API_KEY") != "" && strings.HasPrefix(getenv("ANTHROPIC_BASE_URL"), "http://127.0.0.1:") {
			secret, err := key(ctx)
			if err != nil || secret == "" {
				return errors.New("this shell has ANTHROPIC_API_KEY set and points at a local proxy, and the proxy key cannot be read to tell whether that is it; unset ANTHROPIC_BASE_URL and ANTHROPIC_API_KEY yourself")
			}
			held = getenv("ANTHROPIC_API_KEY") == secret
		}
		_, err := fmt.Fprintln(stdout, strings.Join(modelproxy.SessionEnvOff(marker, held), "\n"))
		return err
	}
	// With one of these set, Claude Code talks to that provider and never reads the base URL.
	for _, other := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if getenv(other) != "" {
			return fmt.Errorf("this shell has %s set, so Claude Code would use that provider and not the model proxy; unset it first", other)
		}
	}
	// The key is read first and sent nowhere: the listener is asked to prove it holds the same one. Whatever
	// answers on the port is about to be sent the key, the Claude login and every conversation.
	secret, err := key(ctx)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("no proxy key in the %s secret-store entry yet; start the proxy once with: aimux model proxy serve", modelproxy.KeyService)
		}
		return fmt.Errorf("read the model proxy key: %w", err)
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	nonce := rand.Text()
	live, err := challengeModelLiveness(probe, fmt.Sprintf("127.0.0.1:%d", port), nonce)
	if err == nil && o.codex {
		err = modelproxy.CheckProxyIdentity(live, secret, nonce, port)
	} else if err == nil {
		err = modelproxy.CheckSessionProxy(live, secret, nonce, port)
	}
	switch {
	case errors.Is(err, modelproxy.ErrNoAnthropicEngine):
		return fmt.Errorf("the model proxy on port %d runs without its Anthropic engine, so a session sent there could only use Codex. "+
			"Restart that proxy with --anthropic, or start another on a free port (aimux model proxy serve --port <port> --anthropic) and pass that port here", port)
	case err != nil:
		return fmt.Errorf("nothing on port %d showed it is the model proxy (it does not answer, is an older build, or is another program); see: aimux model proxy status --port %d", port, port)
	}
	if o.codex {
		lines, err := modelproxy.SessionEnvCodex(port, secret, cmp.Or(o.model, modelproxy.DefaultCodexModel))
		if err != nil {
			return err
		}
		// One chain: if the address cannot be set (a read-only variable), the key is not exported to wherever
		// the old address points.
		if _, err := fmt.Fprintln(stdout, strings.Join(lines, " &&\n")); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Claude Code sessions started from this shell now run on Codex (%s) through the model proxy on port %d. "+
			"Undo with: eval \"$(aimux model proxy env --off)\"\n", cmp.Or(o.model, modelproxy.DefaultCodexModel), port)
		return nil
	}
	lines := modelproxy.SessionEnv(port, secret)
	if getenv(modelproxy.SessionEnvMarker) == modelproxy.SessionEnvCodexed {
		// The shell was set for Codex alone before: the model that form pinned goes, or a later session would
		// ask Anthropic for a Codex model.
		lines = append(lines, "unset ANTHROPIC_MODEL")
	}
	if _, err := fmt.Fprintln(stdout, strings.Join(lines, " &&\n")); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Claude Code sessions started from this shell now go through the model proxy on port %d: "+
		"Claude models on your Claude login, codex: models on Codex. Undo with: eval \"$(aimux model proxy env --off)\"\n", port)
	return nil
}
