package mcpproxy

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Progress relay. Claude Code resets its idle limit on each progress
// notification of a call, so a long call that reports progress must have it
// relayed. Sessions share a backend and may use the same token (both send
// 1), so each forwarded call gets a token of its own; the backend's updates
// for it go back to the session that made the call, with that session's
// original token.

var progressSeq atomic.Uint64

type progressKey struct{}

// progressSink is where one forwarded call's progress goes.
type progressSink struct {
	token string               // the token the backend sees
	send  func(map[string]any) // delivers to the calling session
}

// relayProgress rewrites req's progress token to a proxy-unique one and
// returns a context carrying where its updates go. Without a token, or
// outside an MCP server context, req and ctx are returned unchanged.
func relayProgress(ctx context.Context, req mcp.CallToolRequest) (context.Context, mcp.CallToolRequest) {
	meta := req.Params.Meta
	if meta == nil || meta.ProgressToken == nil {
		return ctx, req
	}
	srv := server.ServerFromContext(ctx)
	if srv == nil {
		return ctx, req
	}
	original := meta.ProgressToken
	token := fmt.Sprintf("aimux-%d", progressSeq.Add(1))
	m := *meta
	m.ProgressToken = token
	req.Params.Meta = &m
	sink := progressSink{token: token, send: func(params map[string]any) {
		params["progressToken"] = original
		_ = srv.SendNotificationToClient(ctx, "notifications/progress", params) // a closed session drops it
	}}
	return context.WithValue(ctx, progressKey{}, sink), req
}

func progressSinkFrom(ctx context.Context) (progressSink, bool) {
	s, ok := ctx.Value(progressKey{}).(progressSink)
	return s, ok
}

// progressRoutes maps a backend's in-flight progress tokens to their sinks.
type progressRoutes struct{ m sync.Map } // token -> func(map[string]any)

// track routes token's updates to send until the returned func is called.
func (r *progressRoutes) track(s progressSink) func() {
	r.m.Store(s.token, s.send)
	return func() { r.m.Delete(s.token) }
}

// deliver hands a backend's notifications/progress to its call's sink; an
// update for a token no call is waiting on is dropped.
func (r *progressRoutes) deliver(n mcp.JSONRPCNotification) {
	if n.Method != "notifications/progress" {
		return
	}
	token := fmt.Sprint(n.Params.AdditionalFields["progressToken"])
	send, ok := r.m.Load(token)
	if !ok {
		return
	}
	params := make(map[string]any, len(n.Params.AdditionalFields))
	for k, v := range n.Params.AdditionalFields {
		params[k] = v
	}
	send.(func(map[string]any))(params)
}
