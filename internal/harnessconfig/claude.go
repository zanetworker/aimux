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

func (claudeCode) Config(c Capabilities, l Launch) (Spec, error) {
	switch l.Mode {
	case ModeHeadless:
		return claudePrint(c, l)
	case ModeInteractive:
		return claudeACP(c)
	}
	return Spec{}, fmt.Errorf("unknown mode %q", l.Mode)
}

// uuidRE is the shape Claude Code requires for --session-id.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Files needs no flag: the OpenShell policy enforces the paths.
func claudePrint(c Capabilities, l Launch) (Spec, error) {
	if l.SessionID != "" && (l.ResumeID != "" || !uuidRE.MatchString(l.SessionID)) {
		return Spec{}, fmt.Errorf("session id %q: must be a UUID and not combined with a resume", l.SessionID)
	}
	servers := grantedServers(c.Grant)
	allowed, err := allowedTools(c.Grant, servers)
	if err != nil {
		return Spec{}, err
	}
	tools := ""
	if c.Web {
		// claude -p denies a tool in --tools unless --allowedTools names it too.
		tools = "WebSearch,WebFetch"
		allowed = joinSorted("WebFetch", "WebSearch", allowed)
	}
	a := []string{"claude", "-p", "--output-format", "stream-json", "--verbose",
		"--tools", tools, "--strict-mcp-config", "--mcp-config", MCPConfigPlaceholder}
	if allowed != "" {
		a = append(a, "--allowedTools", allowed)
	}
	if !c.InheritUser {
		a = append(a, "--setting-sources", "")
	}
	if c.Model != "" {
		a = append(a, "--model", c.Model)
	}
	if l.ResumeID != "" {
		a = append(a, "--resume", l.ResumeID)
	}
	if l.SessionID != "" {
		a = append(a, "--session-id", l.SessionID)
	}
	return Spec{
		Argv:      a,
		MCPConfig: func(baseURL, token string) string { return mcpConfig(baseURL, token, servers) },
		Env:       map[string]string{"DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1"},
	}, nil
}

// joinSorted merges two names with an already comma-joined list into one sorted list.
func joinSorted(a, b, list string) string {
	names := []string{a, b}
	if list != "" {
		names = append(names, strings.Split(list, ",")...)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// grantedServers is g's server names; none for a nil grant.
func grantedServers(g ToolGrantView) []string {
	if g == nil {
		return nil
	}
	return g.Servers()
}

// allowedTools lists exact mcp__<server>__<tool> names for servers in g, sorted.
func allowedTools(g ToolGrantView, servers []string) (string, error) {
	var names []string
	for _, srv := range servers {
		if !nameRE.MatchString(srv) {
			return "", fmt.Errorf("invalid MCP server name %q", srv)
		}
		for _, t := range g.Tools(srv) {
			if !nameRE.MatchString(t) {
				return "", fmt.Errorf("invalid tool name %q on server %q", t, srv)
			}
			names = append(names, "mcp__"+srv+"__"+t)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ","), nil
}

func claudeACP(c Capabilities) (Spec, error) {
	if len(grantedServers(c.Grant)) > 0 {
		return Spec{}, fmt.Errorf("acp mode with tools is not supported yet")
	}
	opts := map[string]any{"tools": []any{}, "mcpServers": map[string]any{}, "strictMcpConfig": true}
	if !c.InheritUser {
		opts["settingSources"] = []any{}
	}
	if c.Model != "" {
		opts["model"] = c.Model
	}
	return Spec{ACPMeta: map[string]any{"claudeCode": map[string]any{"options": opts}}}, nil
}

// mcpConfig returns single-line mcpServers JSON, one http entry per server.
func mcpConfig(baseURL, token string, servers []string) string {
	type entry struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	m := map[string]entry{}
	for _, s := range servers {
		m[s] = entry{"http", strings.TrimRight(baseURL, "/") + "/mcp/" + s, map[string]string{"Authorization": "Bearer " + token}}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": m})
	return string(b)
}
