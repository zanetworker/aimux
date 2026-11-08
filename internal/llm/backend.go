package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// API calls the Anthropic Messages API.
type API struct {
	Key    string
	URL    string
	Client *http.Client
}

// NewAPI is the Messages API at url with key and a per-call timeout.
func NewAPI(key, url string, timeout time.Duration) *API {
	return &API{Key: key, URL: url, Client: &http.Client{Timeout: timeout}}
}

// Complete sends one user message (and the system prompt when set). Errors
// keep only the status and the API's error type: its message may echo input.
func (b *API) Complete(ctx context.Context, req Request) (Reply, error) {
	payload := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"messages":   []map[string]string{{"role": "user", "content": req.Prompt}},
	}
	if req.System != "" {
		payload["system"] = req.System
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Reply{}, fmt.Errorf("model request: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL, bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("model request: %w", err)
	}
	hreq.Header.Set("content-type", "application/json")
	hreq.Header.Set("x-api-key", b.Key)
	hreq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := b.Client.Do(hreq)
	if err != nil {
		return Reply{}, fmt.Errorf("anthropic API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Reply{}, fmt.Errorf("anthropic API: read reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		switch {
		case resp.StatusCode == http.StatusNotFound && e.Error.Type == "not_found_error":
			return Reply{}, fmt.Errorf("anthropic API status 404 for model %s: %w", req.Model, ErrModelNotFound)
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return Reply{}, fmt.Errorf("anthropic API status %d: %w", resp.StatusCode, ErrUnauthorized)
		default:
			return Reply{}, fmt.Errorf("anthropic API status %d (%s): %w", resp.StatusCode, e.Error.Type, ErrModelCall)
		}
	}
	var r struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Reply{}, fmt.Errorf("anthropic API reply (%d bytes): %w", len(data), ErrBadReply)
	}
	reply := Reply{Model: r.Model, InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
	for _, c := range r.Content {
		if c.Type == "text" {
			reply.Text += c.Text
		}
	}
	return reply, nil
}

// CLI runs `claude -p` with the owner's Claude Code login, isolated:
// flags confirmed with `claude --help` (Claude Code 2.1.314):
//
//	-p --model <id>                  print mode with the requested model
//	--safe-mode                      no CLAUDE.md, rules, memory, skills, plugins, hooks,
//	                                 MCP servers or custom commands; the login still works
//	--output-format json             one JSON result carrying usage and cost
//	--no-session-persistence         the run is not saved, so it is never listed or named
//	--tools ""                       no built-in tools
//	--strict-mcp-config              no MCP servers (none given with --mcp-config)
//	--settings {"disableAllHooks":true}  no hooks from settings or plugins
//	--disable-slash-commands         no skills or commands
//	--system-prompt <system prompt>  replaces Claude Code's long default prompt (fewer tokens)
//
// --bare would skip hooks too but reads only ANTHROPIC_API_KEY, never the
// owner's login, so it cannot serve as the no-key fallback; --safe-mode
// keeps the login. The flags after it stay as a second fence. The prompt
// goes on stdin, never on the command line where ps would show it. Each run
// uses a new empty temporary folder (removed after; a temp-folder cwd also
// marks the run automated if anything were saved) and Timeout. It runs in
// its own process group, killed whole on timeout, and Wait gives up on its
// output 5 s later, so a child holding stdout cannot hang the caller.
// There is no output-token flag: the system prompt keeps the answer short.
type CLI struct {
	Bin     string
	Timeout time.Duration
}

func (b *CLI) command(ctx context.Context, dir string, req Request) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.Bin, "-p", "--safe-mode", "--model", req.Model, "--output-format", "json", // #nosec G204 -- fixed binary, flags built here
		"--no-session-persistence", "--tools", "", "--strict-mcp-config",
		"--settings", `{"disableAllHooks":true}`, "--disable-slash-commands",
		"--system-prompt", req.System)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(req.Prompt)
	ownProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Complete runs claude -p once and reads its JSON result.
func (b *CLI) Complete(ctx context.Context, req Request) (Reply, error) {
	dir, err := os.MkdirTemp("", "aimux-llm-")
	if err != nil {
		return Reply{}, fmt.Errorf("claude -p: empty folder: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()
	out, err := b.command(ctx, dir, req).Output() // stderr is dropped: it may echo input
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Reply{}, fmt.Errorf("claude -p after %s: %w", b.Timeout, ErrTimeout)
	}
	if ctx.Err() != nil {
		return Reply{}, fmt.Errorf("claude -p: %w", ctx.Err())
	}
	if err != nil {
		return Reply{}, fmt.Errorf("claude -p: %w", exitOnly(err))
	}
	var r struct {
		IsError bool    `json:"is_error"`
		Result  string  `json:"result"`
		CostUSD float64 `json:"total_cost_usd"`
		Usage   *struct {
			InputTokens         int64 `json:"input_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return Reply{}, fmt.Errorf("claude -p reply (%d bytes): %w", len(out), ErrBadReply)
	}
	if r.IsError {
		return Reply{}, fmt.Errorf("claude -p reported an error: %w", ErrModelCall)
	}
	reply := Reply{Text: r.Result, Model: req.Model}
	if r.CostUSD > 0 {
		reply.CostUSD = &r.CostUSD
	}
	if r.Usage != nil {
		reply.InputTokens = r.Usage.InputTokens + r.Usage.CacheCreationTokens + r.Usage.CacheReadTokens
		reply.OutputTokens = r.Usage.OutputTokens
	} else {
		// about four characters a token
		reply.InputTokens = int64(len(req.System)+len(req.Prompt)) / 4
		reply.OutputTokens = int64(len(r.Result)) / 4
		reply.Estimated = true
	}
	return reply, nil
}

// exitOnly keeps an exec error's kind (exit status, not found) and drops
// anything else.
func exitOnly(err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Errorf("exit status %d: %w", ee.ExitCode(), ErrModelCall)
	}
	return err
}
