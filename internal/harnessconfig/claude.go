package harnessconfig

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MCPConfigPlaceholder stands in for the MCP config file path in Argv; the
// in-sandbox wrapper swaps it for a 0600 temp file holding the config.
const MCPConfigPlaceholder = "wingman-mcp-config-file"

// nameRE is the only shape allowed for server and tool names: anything else
// (`*`, `,`, spaces, empty) could widen --allowedTools.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type claudeCode struct{}

func (claudeCode) Name() string { return "claude-code" }

func (claudeCode) Config(i Intent) (Spec, error) {
	switch i.Mode {
	case "print":
		return claudePrint(i)
	case "acp":
		return claudeACP(i)
	}
	return Spec{}, fmt.Errorf("unknown mode %q", i.Mode)
}

// uuidRE is the shape Claude Code requires for --session-id.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func claudePrint(i Intent) (Spec, error) {
	if i.SessionID != "" && (i.ResumeID != "" || !uuidRE.MatchString(i.SessionID)) {
		return Spec{}, fmt.Errorf("session id %q: must be a UUID and not combined with a resume", i.SessionID)
	}
	allowed, err := allowedTools(i.Tools)
	if err != nil {
		return Spec{}, err
	}
	tools := ""
	if i.Web {
		tools = "WebSearch,WebFetch"
	}
	a := []string{"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--tools", tools, "--strict-mcp-config", "--mcp-config", MCPConfigPlaceholder}
	if allowed != "" {
		a = append(a, "--allowedTools", allowed)
	}
	if !i.InheritUser {
		a = append(a, "--setting-sources", "")
	}
	if i.Model != "" {
		a = append(a, "--model", i.Model)
	}
	if i.ResumeID != "" {
		a = append(a, "--resume", i.ResumeID)
	}
	if i.SessionID != "" {
		a = append(a, "--session-id", i.SessionID)
	}
	return Spec{Argv: a, MCPConfig: func(baseURL, token string) string { return mcpConfig(baseURL, token, i.Tools) }}, nil
}

// allowedTools lists exact mcp__<server>__<tool> names, sorted.
func allowedTools(tools map[string][]string) (string, error) {
	var names []string
	for srv, ts := range tools {
		if !nameRE.MatchString(srv) {
			return "", fmt.Errorf("invalid MCP server name %q", srv)
		}
		for _, t := range ts {
			if !nameRE.MatchString(t) {
				return "", fmt.Errorf("invalid tool name %q on server %q", t, srv)
			}
			names = append(names, "mcp__"+srv+"__"+t)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ","), nil
}

func claudeACP(i Intent) (Spec, error) {
	if len(i.Tools) > 0 {
		return Spec{}, fmt.Errorf("acp mode with tools is not supported yet")
	}
	opts := map[string]any{"tools": []any{}, "mcpServers": map[string]any{}, "strictMcpConfig": true}
	if !i.InheritUser {
		opts["settingSources"] = []any{}
	}
	if i.Model != "" {
		opts["model"] = i.Model
	}
	return Spec{ACPMeta: map[string]any{"claudeCode": map[string]any{"options": opts}}}, nil
}

// mcpConfig returns single-line mcpServers JSON, one http entry per server in tools.
func mcpConfig(baseURL, token string, tools map[string][]string) string {
	type entry struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	m := map[string]entry{}
	for s := range tools {
		m[s] = entry{"http", strings.TrimRight(baseURL, "/") + "/mcp/" + s, map[string]string{"Authorization": "Bearer " + token}}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": m})
	return string(b)
}
