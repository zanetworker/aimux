package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// ServerConfig is how to launch one stdio MCP server, as in ~/.claude.json.
type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// StdioStarter returns a Starter that launches servers from cfgs.
func StdioStarter(cfgs map[string]ServerConfig) Starter {
	return func(ctx context.Context, server string) (Backend, error) {
		cfg, ok := cfgs[server]
		if !ok {
			return nil, fmt.Errorf("no MCP server named %q is configured", server)
		}
		b, err := startStdio(ctx, server, cfg)
		if err != nil {
			return nil, err // not a nil *stdioBackend inside a non-nil Backend
		}
		return b, nil
	}
}

type stdioBackend struct {
	server string
	c      *client.Client
	cmd    *exec.Cmd
	listID atomic.Int64

	closeOnce sync.Once
	closeErr  error
}

func startStdio(ctx context.Context, server string, cfg ServerConfig) (*stdioBackend, error) {
	cfg = expandConfig(cfg)
	b := &stdioBackend{server: server}
	env := make([]string, 0, len(cfg.Env))
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	// Run each server in its own session (so its own process group) so Close
	// also stops the real server when the command is a wrapper such as npx or
	// uv, and so it cannot read from the user's terminal.
	cmdFunc := func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, command, args...) // #nosec G204 -- command comes from the user's MCP config
		cmd.Env = append(cmd.Environ(), env...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // own session: no controlling terminal
		b.cmd = cmd
		return cmd, nil
	}
	c, err := client.NewStdioMCPClientWithOptions(cfg.Command, env, cfg.Args, transport.WithCommandFunc(cmdFunc))
	if err != nil {
		return nil, fmt.Errorf("start MCP server %s (%s): %w", server, cfg.Command, err)
	}
	b.c = c
	// Read the server's stderr: the client opens the pipe but never reads it,
	// and a server that logs more than the pipe holds would block and hang.
	if stderr, ok := client.GetStderr(c); ok {
		go func() { _, _ = io.Copy(io.Discard, stderr) }()
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "aimux-mcp-proxy", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("initialize MCP server %s: %w", server, err)
	}
	return b, nil
}

// ListToolsRaw pages through tools/list and returns each tool's JSON as sent.
func (b *stdioBackend) ListToolsRaw(ctx context.Context) ([]json.RawMessage, error) {
	var tools []json.RawMessage
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		// String IDs cannot collide with the client's own numeric IDs.
		id := mcp.NewRequestId(fmt.Sprintf("aimux-list-%d", b.listID.Add(1)))
		resp, err := b.c.GetTransport().SendRequest(ctx, transport.JSONRPCRequest{
			JSONRPC: mcp.JSONRPC_VERSION, ID: id, Method: string(mcp.MethodToolsList), Params: params,
		})
		if err != nil {
			return nil, fmt.Errorf("list tools of %s: %w", b.server, err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("list tools of %s: %s", b.server, resp.Error.Message)
		}
		var page struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(resp.Result, &page); err != nil {
			return nil, fmt.Errorf("list tools of %s: %w", b.server, err)
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			return tools, nil
		}
		cursor = page.NextCursor
	}
}

func (b *stdioBackend) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	res, err := b.c.CallTool(ctx, req)
	var terr *transport.Error
	if err != nil && !errors.As(err, &terr) {
		// The server answered with a JSON-RPC error: it is alive and well.
		return nil, &RequestError{Err: err}
	}
	return res, err
}

// Close shuts the client down, then kills anything left in the server's
// process group. Only the first call does anything: a later one could kill
// a new process group that reused the id.
func (b *stdioBackend) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.c.Close()
		if b.cmd != nil && b.cmd.Process != nil {
			_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL) // group may already be gone
		}
	})
	return b.closeErr
}
