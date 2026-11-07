package cmd

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	// fakeMCPEnv makes the test binary a stdio MCP server with the tools
	// read_page and delete_page, for tests that start real backends.
	fakeMCPEnv = "AIMUX_CMD_TEST_FAKE_MCP"
	// fakeMCPHangEnv makes that server never answer tools/list.
	fakeMCPHangEnv = "AIMUX_CMD_TEST_FAKE_MCP_HANG"
	// fakeMCPPIDEnv names a file the server writes its pid to.
	fakeMCPPIDEnv = "AIMUX_CMD_TEST_FAKE_MCP_PID"
	// proxyCLIEnv makes the test binary run `aimux mcp proxy` with its
	// arguments, for tests that signal the command.
	proxyCLIEnv = "AIMUX_CMD_TEST_PROXY_CLI"
)

func TestMain(m *testing.M) {
	// The server is checked first: it inherits the CLI's environment.
	if os.Getenv(fakeMCPEnv) == "1" {
		runFakeMCP()
		return
	}
	if os.Getenv(proxyCLIEnv) == "1" {
		cmd := newMCPProxyCmd()
		cmd.SetArgs(os.Args[1:])
		if err := cmd.Execute(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runFakeMCP() {
	if f := os.Getenv(fakeMCPPIDEnv); f != "" {
		_ = os.WriteFile(f, []byte(strconv.Itoa(os.Getpid())), 0o600) // #nosec G703 -- test helper, path set by the test
	}
	hooks := &server.Hooks{}
	if os.Getenv(fakeMCPHangEnv) == "1" {
		hooks.AddBeforeListTools(func(context.Context, any, *mcp.ListToolsRequest) { time.Sleep(time.Hour) })
	}
	s := server.NewMCPServer("fake", "1.0.0", server.WithHooks(hooks))
	for _, name := range []string{"read_page", "delete_page"} {
		s.AddTool(mcp.NewTool(name), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("ok"), nil
		})
	}
	_ = server.ServeStdio(s)
}
