package cmd

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
)

func newModelProxyEnvCmd() *cobra.Command {
	var port int
	var off bool
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print the shell lines that send this shell's Claude Code sessions through the model proxy",
		Long: "Prints lines for eval. After\n\n  eval \"$(aimux model proxy env)\"\n\n" +
			"Claude Code sessions started from that shell go through the model proxy: a Claude model is answered by " +
			"Anthropic on your own Claude login, and a codex: model by Codex. It sets ANTHROPIC_BASE_URL, with the " +
			"proxy key in it, and unsets ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN so the login is used. It says " +
			"nothing about which model a session uses. It needs a running proxy that was started with --anthropic.\n\n" +
			"The output holds the proxy key: it is refused when it would be shown on a terminal, and a shell with " +
			"tracing on (set -x) would print it. When this command fails it prints nothing, and eval of nothing " +
			"succeeds, so in a script use: lines=\"$(aimux model proxy env)\" && eval \"$lines\".\n\n" +
			"Before printing, it asks whatever listens on the port to prove it holds the proxy key, without sending the key. " +
			"eval \"$(aimux model proxy env --off)\" undoes it, and needs no proxy.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			if file, ok := cmd.OutOrStdout().(*os.File); ok && !off && isTerminal(file) {
				return errors.New(`this prints the proxy key, so it is not shown on a terminal; run it as: eval "$(aimux model proxy env)"`)
			}
			return modelProxyEnv(cmd.Context(), port, off, keyring.Stored(modelproxy.KeyService).Lookup, os.Getenv, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().IntVar(&port, "port", modelproxy.StablePort, "Port the proxy listens on at 127.0.0.1")
	cmd.Flags().BoolVar(&off, "off", false, "Print the lines that undo it")
	return cmd
}

// modelProxyEnv writes the lines to stdout and one sentence about them to stderr. Nothing reaches stdout unless
// every check passed, so an eval of a failed run changes nothing; the key is written to stdout only.
func modelProxyEnv(ctx context.Context, port int, off bool, key func(context.Context) (string, error), getenv func(string) string, stdout, stderr io.Writer) error {
	if off {
		_, err := fmt.Fprintln(stdout, strings.Join(modelproxy.SessionEnvOff(), "\n"))
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
	if err == nil {
		err = modelproxy.CheckSessionProxy(live, secret, nonce, port)
	}
	switch {
	case errors.Is(err, modelproxy.ErrNoAnthropicEngine):
		return fmt.Errorf("the model proxy on port %d runs without its Anthropic engine, so a session sent there could only use Codex. "+
			"Restart that proxy with --anthropic, or start another on a free port (aimux model proxy serve --port <port> --anthropic) and pass that port here", port)
	case err != nil:
		return fmt.Errorf("nothing on port %d showed it is the model proxy (it does not answer, is an older build, or is another program); see: aimux model proxy status --port %d", port, port)
	}
	if _, err := fmt.Fprintln(stdout, strings.Join(modelproxy.SessionEnv(port, secret), "\n")); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Claude Code sessions started from this shell now go through the model proxy on port %d: "+
		"Claude models on your Claude login, codex: models on Codex. Undo with: eval \"$(aimux model proxy env --off)\"\n", port)
	return nil
}
