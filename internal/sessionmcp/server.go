package sessionmcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/search"
)

// Server exposes session search and resume as MCP tools.
type Server struct {
	svc *search.Service
}

// New returns a Server over svc.
func New(svc *search.Service) *Server {
	return &Server{svc: svc}
}

// Register adds the session tools to srv.
func (s *Server) Register(srv *server.MCPServer) {
	srv.AddTool(searchTool(), s.handleSearch)
	srv.AddTool(getTool(), s.handleGet)
	srv.AddTool(listTool(), s.handleList)
	srv.AddTool(continueTool(), s.handleContinue)
	srv.AddTool(virtualTool(), s.handleVirtual)
}

const noPaths = "Never build ~/.claude/projects/<slug>/<id>.jsonl paths by hand: a session is stored " +
	"under the directory it was started in, which is usually not the current one."

func searchTool() mcp.Tool {
	return mcp.NewTool("search_sessions",
		mcp.WithDescription("Search across all Claude Code sessions. Default mode is hybrid (keyword plus semantic); "+
			"it falls back to keyword when OPENAI_API_KEY is not set, and the result header says which ranking ran. "+
			"Use keyword for literal queries (IDs, project names, tool names, error strings); quoted phrases match exactly. "+
			"Automated sessions (SDK and scheduled runs) are hidden unless include_automated is true.\n\n"+
			"Use when: user asks 'did we discuss X', 'find the session where we worked on Y', "+
			"'which session had that Z fix', 'search my history for W', or references past work by topic."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query text")),
		mcp.WithString("mode", mcp.Enum(search.Modes...), mcp.DefaultString(search.ModeHybrid)),
		mcp.WithNumber("limit", mcp.DefaultNumber(5)),
		mcp.WithString("project_filter", mcp.Description("Only sessions whose working directory is this path or below it")),
		mcp.WithBoolean("include_automated", mcp.DefaultBool(false)),
	)
}

func getTool() mcp.Tool {
	return mcp.NewTool("get_session",
		mcp.WithDescription("Retrieve a session's conversation by its ID. Accepts a full UUID or a unique prefix "+
			"(e.g. 'd941e8'); an ambiguous prefix returns the matching candidates. Use last_n to return only the tail; output over 100k characters keeps the newest exchanges and its heading gives the before_seq that pages back. "+
			"Finds sessions in any project, including ones started after the last index refresh, and automated ones.\n\n"+
			"Use when: you already have a session ID and need to inspect its content, "+
			"or user says 'show me session X', 'what happened in session Y'. "+noPaths),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Full or prefix UUID of the session")),
		mcp.WithNumber("last_n", mcp.Description("Return only the last N exchanges")),
		mcp.WithNumber("before_seq", mcp.Description("Only exchanges before this sequence number, to page back through a long session; the output heading gives the next value")),
	)
}

func listTool() mcp.Tool {
	return mcp.NewTool("list_sessions",
		mcp.WithDescription("List recent sessions across all projects, newest first, with IDs, titles, dates and directories. "+
			"Automated sessions are hidden unless include_automated is true.\n\n"+
			"Use when: user asks 'what did I work on recently', 'show my sessions', "+
			"'what sessions do I have for project X', or needs to browse history without a specific query."),
		mcp.WithNumber("limit", mcp.DefaultNumber(20)),
		mcp.WithString("project_filter", mcp.Description("Only sessions whose working directory is this path or below it")),
		mcp.WithNumber("days", mcp.DefaultNumber(30), mcp.Description("Only sessions active in the last N days")),
		mcp.WithBoolean("include_automated", mcp.DefaultBool(false)),
	)
}

func continueTool() mcp.Tool {
	return mcp.NewTool("continue_session",
		mcp.WithDescription("Load a past session's transcript into the current context to continue its work. "+
			"Returns metadata, the native `cd <project> && claude --resume <id>` command, and the newest exchanges "+
			"that fit (older ones via get_session).\n\n"+
			"Use when: user says 'continue from session X', 'pick up where we left off', 'resume that session', "+
			"'load session X', pastes a bare session UUID (with or without 'resume this session'), "+
			"or wants to carry forward prior conversation context. This is the primary tool for resuming past work: "+
			"call it instead of locating the JSONL yourself. "+noPaths),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Full or prefix UUID of the session to resume")),
	)
}

func virtualTool() mcp.Tool {
	return mcp.NewTool("create_virtual_session",
		mcp.WithDescription(fmt.Sprintf("Combine context from several sessions into one block, oldest first. "+
			"Sessions of at most %d exchanges are shown whole; longer ones show their last num_exchanges.\n\n"+
			"Use when: work spans multiple sessions and user needs context from all of them, "+
			"e.g. 'combine these sessions', 'merge context from session A and B', "+
			"'I worked on this across several sessions'.", fullExchanges)),
		mcp.WithArray("session_ids", mcp.Required(), mcp.WithStringItems(), mcp.MinItems(2), mcp.MaxItems(maxVirtualSessions),
			mcp.Description("Session IDs or unique prefixes to combine")),
		mcp.WithNumber("num_exchanges", mcp.DefaultNumber(defaultNumExchanges),
			mcp.Description(fmt.Sprintf("Tail exchanges to keep for sessions over %d exchanges", fullExchanges))),
	)
}

func (s *Server) handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError("query is required"), nil
	}
	mode := req.GetString("mode", search.ModeHybrid)
	rs, semantic, err := s.svc.Query(ctx, query, search.QueryOpts{
		Mode:             mode,
		Limit:            req.GetInt("limit", 5),
		IncludeAutomated: req.GetBool("include_automated", false),
		Dir:              req.GetString("project_filter", ""),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(formatResults(rs, mode, semantic)), nil
}

func (s *Server) handleGet(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t, res := s.session(ctx, req)
	if res != nil {
		return res, nil
	}
	return mcp.NewToolResultText(formatSession(t, req.GetInt("last_n", 0), req.GetInt("before_seq", 0))), nil
}

func (s *Server) handleContinue(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t, res := s.session(ctx, req)
	if res != nil {
		return res, nil
	}
	return mcp.NewToolResultText(formatContinue(t)), nil
}

func (s *Server) handleList(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	rs, err := s.svc.Recent(ctx, search.SearchOpts{
		Limit:            req.GetInt("limit", 20),
		IncludeAutomated: req.GetBool("include_automated", false),
		Dir:              req.GetString("project_filter", ""),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if days := req.GetInt("days", 30); days > 0 {
		cutoff := time.Now().AddDate(0, 0, -days)
		kept := rs[:0]
		for _, r := range rs {
			if r.ModTime.After(cutoff) {
				kept = append(kept, r)
			}
		}
		rs = kept
	}
	return mcp.NewToolResultText(formatList(rs)), nil
}

func (s *Server) handleVirtual(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ids := req.GetStringSlice("session_ids", nil)
	if len(ids) < 2 {
		return mcp.NewToolResultError("session_ids needs at least 2 session IDs"), nil
	}
	if len(ids) > maxVirtualSessions {
		return mcp.NewToolResultError(fmt.Sprintf("session_ids takes at most %d session IDs", maxVirtualSessions)), nil
	}
	ts := make([]search.Transcript, 0, len(ids))
	for _, id := range ids {
		t, err := s.svc.Session(ctx, id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("%s: %v", id, err)), nil
		}
		ts = append(ts, t)
	}
	return mcp.NewToolResultText(formatVirtual(ts, req.GetInt("num_exchanges", defaultNumExchanges))), nil
}

// session resolves the request's session_id; a non-nil result is the tool
// error to return instead.
func (s *Server) session(ctx context.Context, req mcp.CallToolRequest) (search.Transcript, *mcp.CallToolResult) {
	id, err := req.RequireString("session_id")
	if err != nil {
		return search.Transcript{}, mcp.NewToolResultError("session_id is required")
	}
	t, err := s.svc.Session(ctx, id)
	if err != nil {
		return search.Transcript{}, mcp.NewToolResultError(err.Error())
	}
	return t, nil
}
