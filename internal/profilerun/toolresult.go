package profilerun

import "context"

// ToolResult is one tool call a sandbox turn made and what came back: the
// tool's name, its input (JSON) and output text, and whether it failed.
type ToolResult struct {
	Name, Input, Output string
	Failed              bool
}

// toolResultsKey carries a turn's tool-result collector in its context.
type toolResultsKey struct{}

// WithToolResults returns ctx carrying collect, which a sandbox turn calls
// once per tool result. Only the run engine sets it: results never reach a
// client's event stream, they stay inside aimux (the planner's tool output
// feeds the query check).
func WithToolResults(ctx context.Context, collect func(ToolResult)) context.Context {
	return context.WithValue(ctx, toolResultsKey{}, collect)
}

// toolResultsFrom is ctx's collector, nil when none.
func toolResultsFrom(ctx context.Context) func(ToolResult) {
	f, _ := ctx.Value(toolResultsKey{}).(func(ToolResult))
	return f
}

// turnLinksKey carries the sink for a turn's links in its context.
type turnLinksKey struct{}

// WithTurnLinks returns ctx carrying report, which a run turn calls once
// with the links its answer may show (the pages its reader fetched). The
// broker puts them on the turn's turn_done event.
func WithTurnLinks(ctx context.Context, report func([]string)) context.Context {
	return context.WithValue(ctx, turnLinksKey{}, report)
}

// SetTurnLinks reports links to ctx's sink; nothing without one.
func SetTurnLinks(ctx context.Context, links []string) {
	if f, _ := ctx.Value(turnLinksKey{}).(func([]string)); f != nil {
		f(links)
	}
}
