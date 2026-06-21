// Package openshell wraps the openshell CLI for sandbox lifecycle
// operations. Both internal/runtime and internal/mcpserver use this
// package to avoid duplicating CLI integration logic.
package openshell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CommandRunner executes a command and returns (stdout, exitCode, error).
// Inject a custom runner in tests to avoid requiring the openshell binary.
type CommandRunner func(ctx context.Context, name string, args ...string) (stdout string, exitCode int, err error)

// Config holds connection settings for the OpenShell gateway.
type Config struct {
	Binary   string // path to openshell CLI, default "openshell"
	Gateway  string // gateway endpoint URL
	Insecure bool   // skip TLS verification
}

// CreateOpts configures a new sandbox.
type CreateOpts struct {
	Name     string
	Image    string
	Provider string            // OpenShell provider name for credential injection (e.g., "claude")
	Env      map[string]string // environment variables injected into the sandbox
	Labels   map[string]string
	NoKeep   bool
}

// ExecResult holds the output of a completed exec.
type ExecResult struct {
	ExitCode int
	Output   string
}

// SandboxInfo describes a sandbox from the list output.
type SandboxInfo struct {
	Name   string
	Status string
}

// Client wraps the openshell CLI.
type Client struct {
	cfg             Config
	runner          CommandRunner
	isDefaultRunner bool
}

// NewClient creates an OpenShell client with the given config.
func NewClient(cfg Config) *Client {
	if cfg.Binary == "" {
		cfg.Binary = "openshell"
	}
	c := &Client{cfg: cfg, isDefaultRunner: true}
	c.runner = c.defaultRunner
	return c
}

// SetRunner replaces the command runner. Use in tests to inject
// a fake that avoids shelling out to the real openshell binary.
func (c *Client) SetRunner(r CommandRunner) {
	c.runner = r
	c.isDefaultRunner = false
}

// Binary returns the configured openshell binary path.
func (c *Client) Binary() string {
	return c.cfg.Binary
}

func (c *Client) defaultRunner(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- args from trusted caller
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return strings.TrimSpace(stdout.String()), exitErr.ExitCode(),
				fmt.Errorf("command failed (exit %d): %w\nstderr: %s",
					exitErr.ExitCode(), err, strings.TrimSpace(stderr.String()))
		}
		return "", 1, fmt.Errorf("command failed: %w\nstderr: %s",
			err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), 0, nil
}

func (c *Client) gatewayArgs() []string {
	var args []string
	if c.cfg.Gateway != "" {
		args = append(args, "--gateway-endpoint", c.cfg.Gateway)
	}
	if c.cfg.Insecure {
		args = append(args, "--gateway-insecure")
	}
	return args
}

func (c *Client) run(ctx context.Context, args ...string) (string, int, error) {
	fullArgs := append(c.gatewayArgs(), args...)
	return c.runner(ctx, c.cfg.Binary, fullArgs...)
}

// CreateSandbox creates a new sandbox and returns its name.
// The openshell CLI blocks after printing the sandbox name (waiting for
// user interaction). We start it in the background, read stdout via a
// pipe to get the name, then poll ListSandboxes for readiness.
func (c *Client) CreateSandbox(ctx context.Context, opts CreateOpts) (string, error) {
	name := opts.Name
	args := []string{"sandbox", "create"}
	if name != "" {
		args = append(args, "--name", name)
	}
	if opts.Image != "" {
		args = append(args, "--from", opts.Image)
	}
	if opts.NoKeep {
		args = append(args, "--no-keep")
	}
	if opts.Provider != "" {
		args = append(args, "--provider", opts.Provider)
	}
	for k, v := range opts.Env {
		args = append(args, "--env", fmt.Sprintf("%s=%s", k, v))
	}

	// For unit tests: if a custom runner is set, use synchronous path
	if c.runner != nil && !c.isDefaultRunner {
		output, _, err := c.run(ctx, args...)
		if err != nil {
			return "", fmt.Errorf("create sandbox: %w", err)
		}
		if name == "" {
			name = parseSandboxName(output)
		}
		if name == "" {
			return "", fmt.Errorf("could not parse sandbox name from output: %q", output)
		}
		// In test mode, check list once
		infos, _ := c.ListSandboxes(ctx)
		for _, info := range infos {
			if info.Name == name {
				return name, nil
			}
		}
		return name, nil
	}

	// Real path: start CLI in background, read name from pipe, poll for Ready
	fullArgs := append(c.gatewayArgs(), args...)
	cmd := exec.CommandContext(ctx, c.cfg.Binary, fullArgs...) // #nosec G204 -- args from trusted config

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create sandbox pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout pipe

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("create sandbox start: %w", err)
	}

	// Read output in a goroutine to avoid blocking
	outputCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var collected string
		for {
			n, readErr := stdoutPipe.Read(buf)
			if n > 0 {
				collected += string(buf[:n])
				if parsed := parseSandboxName(collected); parsed != "" {
					outputCh <- parsed
					return
				}
			}
			if readErr != nil {
				outputCh <- collected
				return
			}
		}
	}()

	// Wait for the name (up to 15s)
	select {
	case output := <-outputCh:
		if name == "" {
			name = parseSandboxName(output)
			if name == "" {
				name = output // might be the raw name
			}
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("timeout waiting for sandbox name")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return "", ctx.Err()
	}

	if name == "" {
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("could not determine sandbox name")
	}

	// Poll until sandbox is Ready
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		infos, listErr := c.ListSandboxes(ctx)
		if listErr == nil {
			for _, info := range infos {
				if info.Name == name && info.Status == "Ready" {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					return name, nil
				}
			}
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return "", ctx.Err()
		}
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return "", fmt.Errorf("sandbox %q did not become ready within timeout", name)
}

// DeleteSandbox removes a sandbox by name.
func (c *Client) DeleteSandbox(ctx context.Context, name string) error {
	_, _, err := c.run(ctx, "sandbox", "delete", name)
	return err
}

// Exec runs a command inside a named sandbox and returns the result.
// Uses positional syntax: openshell sandbox exec <NAME> --no-tty -- <command...>
func (c *Client) Exec(ctx context.Context, name string, command []string) (ExecResult, error) {
	args := []string{"sandbox", "exec", "-n", name, "--"}
	args = append(args, command...)

	output, exitCode, err := c.run(ctx, args...)
	if err != nil {
		return ExecResult{ExitCode: exitCode, Output: output}, err
	}
	return ExecResult{ExitCode: 0, Output: output}, nil
}

// ListSandboxes returns all sandboxes visible to the gateway.
func (c *Client) ListSandboxes(ctx context.Context) ([]SandboxInfo, error) {
	output, _, err := c.run(ctx, "sandbox", "list")
	if err != nil {
		return nil, err
	}
	return parseSandboxList(output), nil
}

// Status checks gateway connectivity by running openshell status.
func (c *Client) Status(ctx context.Context) error {
	_, _, err := c.run(ctx, "status")
	return err
}

func parseSandboxName(output string) string {
	// Strip ANSI escape codes
	clean := StripAnsi(output)
	for _, line := range strings.Split(clean, "\n") {
		line = strings.TrimSpace(line)
		// "Created sandbox: <name>" pattern
		if strings.HasPrefix(line, "Created sandbox:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "Created sandbox:"))
			if name != "" {
				return name
			}
		}
	}
	// Fallback: last non-empty single-word line
	lines := strings.Split(clean, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.Contains(line, " ") {
			return line
		}
	}
	return ""
}

// StripAnsi removes ANSI escape codes from a string.
func StripAnsi(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 'A' || s[j] > 'Z') && (s[j] < 'a' || s[j] > 'z') {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

func parseSandboxList(output string) []SandboxInfo {
	clean := StripAnsi(output)
	var result []SandboxInfo
	for _, line := range strings.Split(clean, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "No sandbox") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			// The list format is: NAME  CREATED_DATE CREATED_TIME  PHASE
			// We want the first field (name) and last field (phase/status)
			result = append(result, SandboxInfo{
				Name:   fields[0],
				Status: fields[len(fields)-1],
			})
		}
	}
	return result
}
