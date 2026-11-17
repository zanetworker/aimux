package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
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

// listenMaxFailures is how many failed listening streams in a row a
// backend tries before it stops listening (its tool changes are then seen
// at the next relist or expired pin check); listenLog is where the first
// failure is reported. Variables so tests can change them.
var (
	listenMaxFailures = 5
	listenLog         = log.Printf
)

// listenGuard is an HTTP backend's transport. It passes every request
// through and watches the listening stream's GETs: the first failure is
// logged (server and status or network error, never the headers), a stream
// that opens resets the count, and once listenMaxFailures failed in a row
// the next GET is answered 405 without reaching the server, which makes
// mcp-go stop listening. A 2xx that is not an event stream counts as a
// failure; only a 2xx event stream resets the count. A GET that resumes a response stream carries
// Last-Event-ID (mcp-go's resumeSSE; the listening GET never does) and is
// passed through untouched: it is part of a call, not the listening stream.
type listenGuard struct {
	server string
	next   http.RoundTripper
	// max and logf are listenMaxFailures and listenLog as the backend
	// started (zero: read them on use), so mcp-go's listening goroutine,
	// which outlives Close, never reads the variables a test restores.
	max  int
	logf func(format string, args ...any)
	// sc hides the backend's header values in what it logs (a server can
	// echo them, in a Content-Type for example).
	sc *scrubber

	mu       sync.Mutex
	failures int
	logged   bool
}

func (g *listenGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.Header.Get("Last-Event-ID") != "" {
		return g.next.RoundTrip(req)
	}
	g.mu.Lock()
	stop := g.failures >= g.limit()
	g.mu.Unlock()
	if stop {
		return &http.Response{
			Status: "405 Method Not Allowed", StatusCode: http.StatusMethodNotAllowed,
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{}, Body: http.NoBody, Request: req,
		}, nil
	}
	resp, err := g.next.RoundTrip(req)
	if req.Context().Err() != nil {
		return resp, err // the client is closing
	}
	switch {
	case err != nil:
		g.failed(err.Error())
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted:
		// mcp-go reads only an event stream; any other body is a failed
		// stream it retries every second, so it counts like an error status.
		ct := resp.Header.Get("Content-Type")
		if mt, _, _ := mime.ParseMediaType(ct); mt != "text/event-stream" {
			g.failed(fmt.Sprintf("status %d with content type %q", resp.StatusCode, clipHeader(ct)))
			break
		}
		g.mu.Lock()
		g.failures = 0
		g.mu.Unlock()
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		// mcp-go handles these: a new session, or no stream offered.
	default:
		g.failed(fmt.Sprintf("status %d", resp.StatusCode))
	}
	return resp, err
}

func (g *listenGuard) limit() int {
	if g.max > 0 {
		return g.max
	}
	return listenMaxFailures
}

// clipHeader bounds a server-sent header value for a log line.
func clipHeader(v string) string {
	if len(v) > 64 {
		return v[:64] + "..."
	}
	return v
}

func (g *listenGuard) failed(why string) {
	g.mu.Lock()
	g.failures++
	first := !g.logged
	g.logged = true
	g.mu.Unlock()
	if g.sc != nil {
		why = g.sc.scrub(why)
	}
	if first {
		logf := g.logf
		if logf == nil {
			logf = listenLog
		}
		logf("mcp proxy: %s: listening for tool changes failed (%s); retrying up to %d times in a row, then its changes are seen at the next relist",
			g.server, why, g.limit())
	}
}

type httpBackend struct {
	server string
	c      *client.Client
	listID atomic.Int64
	sc     *scrubber // hides the header values in every error it returns
}

// headerScrubbedError is err with its header values scrubbed; errors.As and errors.Is
// still see err.
type headerScrubbedError struct {
	msg string
	err error
}

func (e *headerScrubbedError) Error() string { return e.msg }
func (e *headerScrubbedError) Unwrap() error { return e.err }

// scrubErr hides header values in err's text, keeping a RequestError a
// RequestError (callers tell a live server's refusal from a dead one).
func scrubErr(sc *scrubber, err error) error {
	if err == nil || sc == nil {
		return err
	}
	var re *RequestError
	if errors.As(err, &re) {
		return &RequestError{Code: re.Code, Err: &headerScrubbedError{msg: sc.scrub(re.Err.Error()), err: re.Err}}
	}
	return &headerScrubbedError{msg: sc.scrub(err.Error()), err: err}
}

// headerScrubber hides each header value and, for "Scheme token" values,
// the token alone too.
func headerScrubber(headers map[string]string) *scrubber {
	if len(headers) == 0 {
		return nil
	}
	// Every header value is a secret here, however short: no minSecretLen
	// cutoff as for env values.
	seen := map[string]bool{}
	sc := &scrubber{}
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			sc.values = append(sc.values, v)
		}
	}
	for _, v := range headers {
		add(v)
		if i := strings.LastIndexByte(v, ' '); i >= 0 {
			add(v[i+1:])
		}
	}
	sort.Slice(sc.values, func(i, j int) bool { return len(sc.values[i]) > len(sc.values[j]) })
	return sc
}

func startHTTP(ctx context.Context, server string, cfg HTTPServerConfig) (*httpBackend, error) {
	sc := headerScrubber(cfg.Headers)
	b, err := startHTTPRaw(ctx, server, cfg, sc)
	if err != nil {
		return nil, scrubErr(sc, err)
	}
	b.sc = sc
	return b, nil
}

func startHTTPRaw(ctx context.Context, server string, cfg HTTPServerConfig, sc *scrubber) (*httpBackend, error) {
	// The transport logs through slog.Default; the proxy reports errors itself.
	ctx, cancel := context.WithTimeout(ctx, httpStartTimeout)
	defer cancel()
	// Continuous listening opens the GET stream a server's notifications
	// (tools/list_changed) arrive on while no request is in flight. It is
	// deprecated for 2026-07-28, which has no such stream; the client stays
	// on the legacy handshake below, where it is the only way to hear them.
	// mcp-go retries a failed stream every second for the client's life and
	// stops only on a 405, so listenGuard logs the first failure and, after
	// listenMaxFailures in a row, answers the stream's GET with 405 itself.
	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		transport.WithContinuousListening(), //nolint:staticcheck // SA1019: needed on the legacy protocol, see above
		transport.WithHTTPBasicClient(&http.Client{Transport: &listenGuard{server: server, next: http.DefaultTransport, max: listenMaxFailures, logf: listenLog, sc: sc}}),
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(cfg.Headers))
	}
	trans, err := transport.NewStreamableHTTP(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect MCP server %s: %w", server, err)
	}
	// Pinned to the initialize handshake, as for stdio backends: 2026-07-28 has
	// no sessions, and our raw tools/list carries no _meta protocol version.
	c := client.NewClient(trans, client.WithProtocolVersion(mcp.LATEST_LEGACY_PROTOCOL_VERSION))
	// The listening stream lives as long as the client (Close ends it), not
	// as long as the start timeout; Start itself sends nothing.
	if err := c.Start(context.WithoutCancel(ctx)); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("start MCP server %s: %w", server, err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_LEGACY_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "aimux-mcp-proxy", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize MCP server %s: %w", server, err)
	}
	return &httpBackend{server: server, c: c}, nil
}

// ListToolsRaw pages through tools/list and returns each tool's JSON as sent.
func (b *httpBackend) ListToolsRaw(ctx context.Context) ([]json.RawMessage, error) {
	tools, err := b.listToolsRaw(ctx)
	if err != nil {
		return nil, scrubErr(b.sc, err)
	}
	// Tool lists are saved (catalog, pins): one that echoes a header value
	// is refused rather than stored.
	if b.sc != nil {
		for _, t := range tools {
			if echoesValue(b.sc, t) {
				return nil, &RequestError{Err: fmt.Errorf("list tools of %s: its tool list contains one of its header values; not stored", b.server)}
			}
		}
	}
	return tools, nil
}

// echoesValue reports whether any string token in raw (keys and values,
// at any depth, decoded, duplicates included, so every escape form and
// every member counts) holds one of sc's configured values of 8 bytes or
// more (shorter values would match ordinary words). A tool that does not
// decode counts as an echo: it cannot be checked.
func echoesValue(sc *scrubber, raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		s, ok := tok.(string)
		if !ok {
			continue
		}
		for _, val := range sc.values {
			if len(val) >= 8 && strings.Contains(s, val) {
				return true
			}
		}
	}
}

func (b *httpBackend) listToolsRaw(ctx context.Context) ([]json.RawMessage, error) {
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
	res, err := b.callTool(ctx, req)
	return res, scrubErr(b.sc, err)
}

func (b *httpBackend) callTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, httpCallTimeout)
	defer cancel()
	req.Params.Meta = withoutProtocolMeta(req.Params.Meta)
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

// OnToolsChanged runs fn on each tools/list_changed the server sends.
func (b *httpBackend) OnToolsChanged(fn func()) { onToolsChanged(b.c, fn) }

// onToolsChanged registers fn on c for tools/list_changed only; c keeps
// its other notification handlers.
func onToolsChanged(c *client.Client, fn func()) {
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method == mcp.MethodNotificationToolsListChanged {
			fn()
		}
	})
}

func (b *httpBackend) Close() error { return b.c.Close() }

// protocolMetaPrefix marks _meta keys that describe one connection (protocol
// version, client info and capabilities, log level), not the call.
const protocolMetaPrefix = "io.modelcontextprotocol/"

// withoutProtocolMeta drops the caller's protocol keys from a call's _meta:
// they describe the caller's connection to the proxy, and forwarded to a
// backend on another protocol version they contradict that connection's
// Mcp-Protocol-Version header. m is not modified.
func withoutProtocolMeta(m *mcp.Meta) *mcp.Meta {
	if m == nil || len(m.AdditionalFields) == 0 {
		return m
	}
	out := &mcp.Meta{ProgressToken: m.ProgressToken}
	for k, v := range m.AdditionalFields {
		if strings.HasPrefix(k, protocolMetaPrefix) {
			continue
		}
		if out.AdditionalFields == nil {
			out.AdditionalFields = map[string]any{}
		}
		out.AdditionalFields[k] = v
	}
	return out
}
