package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	acp "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/acprun"
	"golang.org/x/term"
)

const defaultClaudeAdapter = "npx -y @agentclientprotocol/claude-agent-acp"

func newChatCmd() *cobra.Command {
	var (
		adapter string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "chat <session-id> <message>",
		Short: "Send a message to an existing Claude session and stream the reply",
		Long: "Continue a Claude Code session that is not open in a terminal, through an ACP adapter. " +
			"The reply is written to the session's own file, so `claude --resume <id>` continues from it. " +
			"Permission requests are asked on the terminal; without one they are denied.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, message := args[0], strings.Join(args[1:], " ")
			name, adapterArgs, err := parseAdapter(adapter)
			if err != nil {
				return err
			}
			approve := denyNoTerminal(cmd.ErrOrStderr())
			if isTerminal(os.Stdin) {
				approve = promptApprover(os.Stdin, cmd.ErrOrStderr())
			}
			start := func(ctx context.Context, id, cwd string) (acprun.Runner, error) {
				return acprun.Start(ctx, acprun.Spec{Command: name, Args: adapterArgs, Cwd: cwd}, id, approve)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			router := &acprun.Router{
				Lookup:  acprun.ClaudeLookup(filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".claude", "sessions")),
				Manager: acprun.NewManager(acprun.ManagerOptions{Start: start, MaxRuns: 1}),
			}
			defer router.Manager.Close()

			// Ctrl-C stops the agent's turn; the session file keeps what was done.
			sigCtx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-sigCtx.Done()
				_ = router.Manager.Cancel(sessionID)
			}()

			out, info := cmd.OutOrStdout(), cmd.ErrOrStderr()
			reason, err := router.Send(context.Background(), sessionID, message,
				func(e acprun.Event) { renderChatEvent(out, info, e, asJSON) })
			if err != nil {
				return err
			}
			if !asJSON {
				_, _ = fmt.Fprintln(out)
				if reason != string(acp.StopReasonEndTurn) {
					_, _ = fmt.Fprintf(info, "(stopped: %s)\n", reason)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&adapter, "adapter", defaultClaudeAdapter, "ACP adapter command for Claude sessions")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print each event as a JSON line")
	return cmd
}

// renderChatEvent prints the agent's reply text to out and a short line per
// tool call or permission request to info; with asJSON every event is one
// JSON line on out.
func renderChatEvent(out, info io.Writer, e acprun.Event, asJSON bool) {
	if asJSON {
		line, _ := json.Marshal(e)
		_, _ = fmt.Fprintln(out, string(line))
		return
	}
	if e.Kind != acprun.KindUpdate {
		return // permission requests are shown by the approver
	}
	var u struct {
		Kind    string `json:"sessionUpdate"`
		Title   string `json:"title"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(e.Data, &u) != nil {
		return
	}
	switch u.Kind {
	case "agent_message_chunk":
		if u.Content.Type == "text" {
			_, _ = fmt.Fprint(out, u.Content.Text)
		}
	case "tool_call":
		_, _ = fmt.Fprintf(info, "\n• %s\n", u.Title)
	}
}

// promptApprover asks on the terminal before the agent may use a tool.
// Anything but y or yes, including end of input, denies.
func promptApprover(in io.Reader, out io.Writer) acprun.Approver {
	lines := bufio.NewReader(in)
	return func(ctx context.Context, req acp.RequestPermissionRequest) (string, bool) {
		title := "a tool call"
		if req.ToolCall.Title != nil {
			title = *req.ToolCall.Title
		}
		_, _ = fmt.Fprintf(out, "\nAllow %s? [y/N] ", title)
		answer, _ := lines.ReadString('\n')
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			return "", false
		}
		for _, kind := range []acp.PermissionOptionKind{acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways} {
			for _, o := range req.Options {
				if o.Kind == kind {
					return string(o.OptionId), true
				}
			}
		}
		return "", false
	}
}

// parseAdapter splits an adapter command line on spaces.
func parseAdapter(s string) (string, []string, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", nil, errors.New("--adapter is empty")
	}
	return fields[0], fields[1:], nil
}

// isTerminal reports whether f is an interactive terminal. A character
// device is not enough: /dev/null is one too.
func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) } // #nosec G115 -- file descriptors are small

// denyNoTerminal denies every request and says so, for runs with nobody to ask.
func denyNoTerminal(out io.Writer) acprun.Approver {
	return func(ctx context.Context, req acp.RequestPermissionRequest) (string, bool) {
		title := "a tool call"
		if req.ToolCall.Title != nil {
			title = *req.ToolCall.Title
		}
		_, _ = fmt.Fprintf(out, "\ndenied: %s (no terminal to ask for approval)\n", title)
		return "", false
	}
}
