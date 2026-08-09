package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/agent"
	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/spawn"
)

func newKillCmd(discover discoverFunc, composeEngine *aimuxcompose.Engine) *cobra.Command {
	return &cobra.Command{
		Use:   "kill <pid>",
		Short: "Kill a running agent",
		Long:  "Terminate a running AI agent by its PID. Determines the appropriate kill strategy (SIGTERM for local processes, kubectl for pods).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PID %q: must be an integer", args[0])
			}

			agents, err := discover()
			if err != nil {
				return fmt.Errorf("discovery failed: %w", err)
			}

			var target *agent.Agent
			for i := range agents {
				if agents[i].PID == pid {
					target = &agents[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("no agent found with PID %d", pid)
			}

			action := controller.DetermineKillAction(*target)

			// KillSandbox handles its own tmux cleanup via ExecuteKillSandbox.
			if action.Type != controller.KillSandbox && target.TMuxSession != "" {
				spawn.KillTmuxSession(target.TMuxSession)
			}

			var killErr error
			switch action.Type {
			case controller.KillProcess:
				killErr = syscall.Kill(target.PID, syscall.SIGTERM)
			case controller.KillPod:
				killErr = fmt.Errorf("pod kill not implemented in CLI; use kubectl delete pod %s -n %s", action.PodName, action.Namespace)
			case controller.KillRemoveOnly:
				// Session-only entry: nothing to kill.
			case controller.KillSandbox:
				if composeEngine != nil {
					killErr = controller.ExecuteKillSandbox(action, composeEngine)
				}
			}

			if killErr != nil {
				return fmt.Errorf("kill failed: %w", killErr)
			}

			if jsonOutput {
				result := map[string]any{
					"pid":      target.PID,
					"provider": target.ProviderName,
					"project":  target.Name,
					"action":   killTypeString(action.Type),
					"killed":   true,
				}
				if action.Type == controller.KillPod {
					result["pod_name"] = action.PodName
					result["namespace"] = action.Namespace
				}
				if action.Type == controller.KillSandbox {
					result["sandbox_name"] = action.SandboxName
				}
				b, _ := json.MarshalIndent(result, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Killed %s agent (PID %d)\n", target.ProviderName, target.PID)
			}

			return nil
		},
	}
}

func killTypeString(kt controller.KillType) string {
	switch kt {
	case controller.KillProcess:
		return "sigterm"
	case controller.KillPod:
		return "pod"
	case controller.KillRemoveOnly:
		return "remove_only"
	case controller.KillSandbox:
		return "sandbox"
	default:
		return "unknown"
	}
}
