package discovery

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripAnsi(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// DiscoverSandboxes returns agents for running OpenShell sandboxes.
// Uses a 3-second timeout so a stuck gateway never blocks the
// discovery tick. Returns nil on any error.
func DiscoverSandboxes() []agent.Agent {
	if _, err := exec.LookPath("openshell"); err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "openshell", "sandbox", "list") // #nosec G204
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	return parseSandboxAgents(string(out))
}

func parseSandboxAgents(output string) []agent.Agent {
	output = stripAnsi(output)
	var agents []agent.Agent

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "No sandbox") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		name := fields[0]
		phase := fields[len(fields)-1]

		var status agent.Status
		var lastAction string
		switch phase {
		case "Ready":
			status = agent.StatusActive
		case "Error":
			status = agent.StatusError
		case "Deleting", "Terminating":
			status = agent.StatusError
			lastAction = "Deleting"
		default:
			status = agent.StatusIdle
		}

		agents = append(agents, agent.Agent{
			Name:         name,
			ProviderName: "claude",
			WorkingDir:   "/sandbox",
			Status:       status,
			LastAction:   lastAction,
			Location:     "remote",
			SandboxName:  name,
			StartTime:    time.Now(),
			LastActivity: time.Now(),
			GroupCount:   1,
		})
	}

	return agents
}
