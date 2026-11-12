package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Per-request bounds: the transport's http.Client has none, so a server that
// is up but silent would block until the caller's context ends. Variables so
// tests can shorten them.
var (
	httpStartTimeout = 5 * time.Second  // connect and initialize
	httpListTimeout  = 5 * time.Second  // each tools/list page
	httpCallTimeout  = 30 * time.Second // each tools/call
)

// HTTPServerConfig is how to reach one Streamable HTTP MCP server.
type HTTPServerConfig struct {
	URL     string
	Headers map[string]string
}

// HTTPStarter returns a Starter that connects to the servers in cfgs.
func HTTPStarter(cfgs map[string]HTTPServerConfig) Starter {
	return func(ctx context.Context, server string) (Backend, error) {
		cfg, ok := cfgs[server]
		if !ok {
			return nil, fmt.Errorf("no HTTP MCP server named %q is configured", server)
		}
		b, err := startHTTP(ctx, server, cfg)
		if err != nil {
			return nil, err // not a nil *httpBackend inside a non-nil Backend
		}
		return b, nil
	}
}

type httpBackend struct {
	server string
	c      *client.Client
	listID atomic.Int64
}

func startHTTP(ctx context.Context, server string, cfg HTTPServerConfig) (*httpBackend, error) {
	// The transport logs through slog.Default; the proxy reports errors itself.
	ctx, cancel := context.WithTimeout(ctx, httpStartTimeout)
	defer cancel()
	opts := []transport.StreamableHTTPCOption{transport.WithHTTPLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	if len(cfg.Headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(cfg.Headers))
	}
	c, err := client.NewStreamableHttpClient(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect MCP server %s: %w", server, err)
	}
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("start MCP server %s: %w", server, err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "aimux-mcp-proxy", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize MCP server %s: %w", server, err)
	}
	return &httpBackend{server: server, c: c}, nil
}

// ListToolsRaw pages through tools/list and returns each tool's JSON as sent.
func (b *httpBackend) ListToolsRaw(ctx context.Context) ([]json.RawMessage, error) {
	var tools []json.RawMessage
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		id := mcp.NewRequestId(fmt.Sprintf("aimux-list-%d", b.listID.Add(1)))
		pctx, cancel := context.WithTimeout(ctx, httpListTimeout)
		resp, err := b.c.GetTransport().SendRequest(pctx, transport.JSONRPCRequest{
			JSONRPC: mcp.JSONRPC_VERSION, ID: id, Method: string(mcp.MethodToolsList), Params: params,
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("list tools of %s: %w", b.server, err)
		}
		if resp.Error != nil {
			// The server answered: it is alive, whatever the error says.
			return nil, &RequestError{Code: resp.Error.Code, Err: fmt.Errorf("list tools of %s: %s", b.server, resp.Error.Message)}
		}
		var page struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(resp.Result, &page); err != nil {
			return nil, &RequestError{Err: fmt.Errorf("list tools of %s: %w", b.server, err)}
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			return tools, nil
		}
		cursor = page.NextCursor
	}
}

func (b *httpBackend) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, httpCallTimeout)
	defer cancel()
	res, err := b.c.CallTool(ctx, req)
	var terr *transport.Error
	if err != nil && !errors.As(err, &terr) {
		// A JSON-RPC error answer: the server is alive and well.
		return nil, &RequestError{Err: err}
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (b *httpBackend) Close() error { return b.c.Close() }
