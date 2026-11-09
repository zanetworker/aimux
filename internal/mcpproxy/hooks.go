package mcpproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zanetworker/aimux/internal/claudehooks"
)

// The hooks install adds to Claude Code: one moves a server added in a
// session behind the proxy, one names servers still direct when a session
// starts. See docs/superpowers/specs/2026-11-09-mcp-proxy-server-hooks-design.md.

// hooksAdded is Manifest.Hooks once install added the hooks.
const hooksAdded = "added"

// HookSpec is the two hooks: after a Bash call (moves a server just added)
// and at session start (names servers still direct).
var HookSpec = claudehooks.Spec{
	{Event: "PostToolUse", Matcher: "Bash", Suffix: " mcp proxy hooks added", Timeout: 120},
	{Event: "SessionStart", Suffix: " mcp proxy hooks check", Timeout: 10},
}

// ensureHooks adds HookSpec to i.Settings for i.HookBinary and records it
// in m. Like the idle timeout, a failure is a note, not an install failure.
func (i Installer) ensureHooks(m *Manifest) {
	if i.Settings == "" || i.HookBinary == "" || m == nil {
		return
	}
	ch, err := claudehooks.Install(i.Settings, HookSpec, i.HookBinary, i.now())
	if err != nil {
		i.printf("Note: could not add the MCP server hooks to %s (%v); servers you add later stay direct until you run aimux mcp proxy install again.\n", i.Settings, err)
		return
	}
	if len(ch.Added)+len(ch.Updated) > 0 {
		i.printf("Added the MCP server hooks to %s: a server you add in a Claude Code session moves behind the proxy, and servers still direct are named when a session starts.\n", i.Settings)
	}
	if m.Hooks != hooksAdded {
		m.Hooks = hooksAdded
		if err := i.saveManifest(m); err != nil {
			i.printf("Note: added the MCP server hooks but could not record them (%v); uninstall will leave them.\n", err)
		}
	}
}

// dropHooks removes the hooks ensureHooks added, if any.
func (i Installer) dropHooks(m *Manifest) {
	if i.Settings == "" || m == nil || m.Hooks == "" {
		return
	}
	if _, err := claudehooks.Uninstall(i.Settings, HookSpec, i.now()); err != nil {
		i.printf("Note: could not remove the MCP server hooks from %s (%v); remove them by hand.\n", i.Settings, err)
	}
}

// hooksOn: every hook of HookSpec is in i.Settings.
func (i Installer) hooksOn() bool {
	if i.Settings == "" {
		return false
	}
	st, err := claudehooks.Status(i.Settings, HookSpec)
	return err == nil && len(st.Missing) == 0
}

// addCommands are the `claude mcp` subcommands that add servers.
var addCommands = map[string]bool{"add": true, "add-json": true, "add-from-claude-desktop": true}

// IsServerAdd reports whether a shell command line runs `claude mcp add`,
// `add-json` or `add-from-claude-desktop` as a command of its own (not inside
// quotes or a comment). A line that cannot be split into words is false.
func IsServerAdd(command string) bool {
	if !strings.Contains(command, "mcp") { // the common case: nothing to parse
		return false
	}
	segments, ok := shellSegments(command)
	if !ok {
		return false
	}
	for _, w := range segments {
		if len(w) >= 3 && filepath.Base(w[0]) == "claude" && w[1] == "mcp" && addCommands[w[2]] {
			return true
		}
	}
	return false
}

// shellSegments splits a command line into simple commands (split at &&,
// ||, ;, | and newlines outside quotes), each a list of words with quotes
// removed; a # starting a word begins a comment to the end of the line.
// false for an unclosed quote.
func shellSegments(line string) ([][]string, bool) {
	var (
		segs      [][]string
		words     []string
		word      strings.Builder
		inWord    bool
		quote     rune
		inComment bool
		endWord   = func() {
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		}
		endSeg = func() {
			endWord()
			if len(words) > 0 {
				segs = append(segs, words)
				words = nil
			}
		}
	)
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case inComment:
			if c == '\n' {
				inComment = false
				endSeg()
			}
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(rs) {
				i++
				word.WriteRune(rs[i])
			} else {
				word.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == '\\' && i+1 < len(rs):
			i++
			word.WriteRune(rs[i])
			inWord = true
		case c == '#' && !inWord:
			inComment = true
		case c == ';' || c == '|' || c == '&' || c == '\n':
			endSeg()
		case c == ' ' || c == '\t':
			endWord()
		default:
			word.WriteRune(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	endSeg()
	return segs, true
}

// ProjectServers lists, sorted, the stdio MCP servers Claude Code uses only
// for the project at cwd: projects[cwd].mcpServers in the Claude config and
// the mcpServers of <cwd>/.mcp.json. Missing files are not errors.
func ProjectServers(claudeConfig, cwd string) ([]string, error) {
	names := map[string]bool{}
	var cfg struct {
		Projects map[string]struct {
			MCPServers map[string]claudeServerEntry `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := readJSONIfExists(claudeConfig, &cfg); err != nil {
		return nil, err
	}
	addStdio(names, cfg.Projects[cwd].MCPServers)
	var repo struct {
		MCPServers map[string]claudeServerEntry `json:"mcpServers"`
	}
	if err := readJSONIfExists(filepath.Join(cwd, ".mcp.json"), &repo); err != nil {
		return nil, err
	}
	addStdio(names, repo.MCPServers)
	return sortedKeys(names), nil
}

func addStdio(names map[string]bool, servers map[string]claudeServerEntry) {
	for name, e := range servers {
		if (e.Type == "" || e.Type == "stdio") && e.Command != "" {
			names[name] = true
		}
	}
}

// readJSONIfExists decodes the JSON file at path into v; a missing file
// leaves v as it is.
func readJSONIfExists(path string, v any) error {
	data, err := os.ReadFile(path) // #nosec G304 -- the owner's Claude config or a project's .mcp.json
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// MovedMessage tells the owner which servers a hook moved behind the proxy.
func MovedMessage(names []string) string {
	verb := "it starts"
	if len(names) > 1 {
		verb = "they start"
	}
	return fmt.Sprintf("%s moved behind the aimux proxy (tools unchanged; %s on first use). Open sessions keep their direct copy until they restart.",
		strings.Join(names, ", "), verb)
}

// FailedMessage tells the owner the move failed and the servers stay direct.
func FailedMessage(names []string, reason string) string {
	stays := "stays a direct server"
	if len(names) > 1 {
		stays = "stay direct servers"
	}
	return fmt.Sprintf("%s %s: %s. Run aimux mcp proxy install after fixing it.", strings.Join(names, ", "), stays, reason)
}

// ProjectMessage tells the owner a project-scoped server is not shared.
func ProjectMessage(name string) string {
	return fmt.Sprintf("%s was added for this project only; the proxy shares user-scope servers. To share it: claude mcp remove %s, then claude mcp add --scope user …", name, name)
}

// CheckMessage names the stdio servers still direct; empty when there are none.
func CheckMessage(direct []string) string {
	switch len(direct) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("1 MCP server is not behind the aimux proxy: %s. Run aimux mcp proxy install.", direct[0])
	}
	return fmt.Sprintf("%d MCP servers are not behind the aimux proxy: %s. Run aimux mcp proxy install.", len(direct), strings.Join(direct, ", "))
}
