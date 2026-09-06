package environment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// LocalExecutor wraps exec.Command for launching local processes.
// It implements the executor pattern used by agent-compose but as a standalone aimux type.
type LocalExecutor struct{}

// NewLocalExecutor creates a new LocalExecutor.
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{}
}

// Launch starts a new process with the given binary, arguments, working directory, and environment.
// It returns the started process or an error if the process could not be started.
func (e *LocalExecutor) Launch(ctx context.Context, binary string, args []string, dir string, env map[string]string) (*os.Process, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary is from trusted config
	cmd.Dir = dir

	// Start with inherited environment
	cmd.Env = os.Environ()

	// Append/override with provided env vars
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("launch %s: %w", binary, err)
	}

	return cmd.Process, nil
}
