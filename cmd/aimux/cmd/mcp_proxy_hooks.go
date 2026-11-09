package cmd

import (
	"encoding/json"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// hookEvents maps `aimux mcp proxy hooks <name>` to its Claude Code event.
var hookEvents = map[string]string{"added": "PostToolUse", "check": "SessionStart"}

// newMCPProxyHooksCmd is what the hooks install adds run: Claude Code's hook
// input on stdin, an answer (or nothing) on stdout, always exit 0 so a hook
// never fails a tool call or a session start.
func newMCPProxyHooksCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "hooks added|check",
		Short:     "Answer Claude Code's MCP server hooks (run by the hooks install adds)",
		Hidden:    true,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"added", "check"},
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil //nolint:nilerr // a hook never fails
			}
			in := proxyInstaller(home, restartPort(home, 7381, false), io.Discard)
			if bin, err := stableBinary("aimux mcp proxy install"); err == nil {
				in.Agent.Binary, in.HookBinary = bin, bin
			}
			_ = answerProxyHook(args[0], cmd.InOrStdin(), cmd.OutOrStdout(), in)
			return nil
		},
	}
}

// hookInput is the part of Claude Code's hook input the hooks read.
type hookInput struct {
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

type hookOutput struct {
	SystemMessage      string `json:"systemMessage,omitempty"`
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// answerProxyHook reads one hook input for event ("added" or "check") and
// writes the answer; nothing for an unknown event, unreadable input or
// nothing to say.
func answerProxyHook(event string, stdin io.Reader, stdout io.Writer, in mcpproxy.Installer) error {
	name, ok := hookEvents[event]
	if !ok {
		return nil
	}
	var hi hookInput
	if err := json.NewDecoder(stdin).Decode(&hi); err != nil {
		return nil //nolint:nilerr // unreadable input: say nothing
	}
	var r mcpproxy.HookReply
	if event == "added" {
		r = in.HookAdded(hi.ToolInput.Command, hi.Cwd)
	} else {
		r = in.HookCheck(hi.Cwd)
	}
	if r.User == "" && r.Claude == "" {
		return nil
	}
	var out hookOutput
	out.SystemMessage = r.User
	out.HookSpecificOutput.HookEventName, out.HookSpecificOutput.AdditionalContext = name, r.Claude
	return json.NewEncoder(stdout).Encode(out)
}
