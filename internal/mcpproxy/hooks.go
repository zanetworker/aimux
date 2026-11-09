package mcpproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	{Event: "PostToolUse", Matcher: "Bash", Suffix: " mcp proxy hooks added", Timeout: 300}, // a move may cold-start servers twice (list tools, probe)
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
		heredocs  []heredoc // opened on this line; their bodies start after it
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
				if rs[i] != '\n' { // a backslash-newline inside "" is removed
					word.WriteRune(rs[i])
				}
			} else {
				word.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == '\\' && i+1 < len(rs) && rs[i+1] == '\n': // line continuation: the lines join
			i++
		case c == '\\' && i+1 < len(rs):
			i++
			word.WriteRune(rs[i])
			inWord = true
		case c == '#' && !inWord:
			inComment = true
		case c == '<' && i+2 < len(rs) && rs[i+1] == '<' && rs[i+2] == '<': // a here-string, not a heredoc
			endWord()
			i += 2
		case c == '<' && i+1 < len(rs) && rs[i+1] == '<':
			endWord()
			h, next := readHeredoc(rs, i+2)
			if h.delim != "" {
				heredocs = append(heredocs, h)
			}
			i = next - 1
		case c == '\n' && len(heredocs) > 0:
			endSeg()
			i = skipHeredocBodies(rs, i+1, heredocs) - 1
			heredocs = nil
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

// heredoc is a `<<WORD` (strip: `<<-WORD`, leading tabs ignored) whose body
// lines are data, not commands.
type heredoc struct {
	delim string
	strip bool
}

// readHeredoc reads the delimiter after `<<` at rs[i:] (an optional -, then
// a word, quotes removed) and returns it with the index just past it.
func readHeredoc(rs []rune, i int) (heredoc, int) {
	var h heredoc
	if i < len(rs) && rs[i] == '-' {
		h.strip, i = true, i+1
	}
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
		i++
	}
	var d strings.Builder
	for i < len(rs) && !strings.ContainsRune(" \t\n;|&<>()", rs[i]) {
		if rs[i] != '\'' && rs[i] != '"' && rs[i] != '\\' {
			d.WriteRune(rs[i])
		}
		i++
	}
	h.delim = d.String()
	return h, i
}

// skipHeredocBodies skips, from the line starting at rs[i:], each heredoc's
// body through its delimiter line, in order; it returns the index after the
// last one (len(rs) when a body never ends).
func skipHeredocBodies(rs []rune, i int, hs []heredoc) int {
	for _, h := range hs {
		for i < len(rs) {
			end := i
			for end < len(rs) && rs[end] != '\n' {
				end++
			}
			line := string(rs[i:end])
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			i = end + 1
			if line == h.delim {
				break
			}
		}
	}
	if i > len(rs) {
		return len(rs)
	}
	return i
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

// HookReply is what a hook says: User is shown to the owner, Claude goes
// into the session's context. Both empty: the hook prints nothing.
type HookReply struct {
	User, Claude string
}

// quiet is the installer for a hook's reads: no daemon call, no output.
func (i Installer) quiet() Installer {
	i.Status, i.Out = nil, io.Discard
	return i
}

// HookAdded answers the PostToolUse hook for a Bash command line run in a
// session at cwd. Unless the command added an MCP server and the proxy is
// installed, it says nothing. A new user-scope stdio server is moved behind
// the proxy (install's own all-or-nothing move of every server still
// direct); a project-scoped server the command named is explained instead.
func (i Installer) HookAdded(command, cwd string) HookReply {
	if !IsServerAdd(command) {
		return HookReply{}
	}
	st, err := i.quiet().InstallStatus()
	if err != nil || st.State == InstallNotInstalled {
		return HookReply{}
	}
	project := i.projectReply(command, cwd).User
	if len(st.NotProxied) == 0 {
		return HookReply{User: project, Claude: project}
	}
	mover := i
	mover.Sessions = nil // its note goes nowhere in a hook; skip the process scan
	msg := MovedMessage(st.NotProxied)
	if err := mover.Install(nil); err != nil {
		msg = FailedMessage(st.NotProxied, strings.TrimSuffix(err.Error(), "."))
	}
	msg = strings.TrimSpace(msg + " " + project) // a project-scoped add is explained even when others moved
	return HookReply{User: msg, Claude: msg}
}

// projectReply explains the project-scoped servers at cwd that command
// names; nothing for servers it does not name (they were there before).
func (i Installer) projectReply(command, cwd string) HookReply {
	project, err := ProjectServers(i.ClaudeConfig, cwd)
	if err != nil || len(project) == 0 {
		return HookReply{}
	}
	segments, _ := shellSegments(command)
	named := map[string]bool{}
	for _, seg := range segments {
		for _, w := range seg {
			named[w] = true
		}
	}
	var msgs []string
	for _, name := range project {
		if named[name] {
			msgs = append(msgs, ProjectMessage(name))
		}
	}
	msg := strings.Join(msgs, " ")
	return HookReply{User: msg, Claude: msg}
}

// HookCheck answers the SessionStart hook for a session at cwd: the stdio
// servers still direct, read from files only. Project-only servers for cwd
// go to Claude, not the owner's line (the proxy cannot share them).
func (i Installer) HookCheck(cwd string) HookReply {
	st, err := i.quiet().InstallStatus()
	if err != nil || st.State == InstallNotInstalled {
		return HookReply{}
	}
	user := CheckMessage(st.NotProxied)
	if len(st.Drifted) > 0 { // moved, but a stale session wrote the direct entry back
		drift := fmt.Sprintf("Running direct again instead of through the aimux proxy: %s; to fix it, %s.", strings.Join(st.Drifted, ", "), DriftFix)
		user = strings.TrimSpace(user + " " + drift)
	}
	claude := user
	if project, err := ProjectServers(i.ClaudeConfig, cwd); err == nil && len(project) > 0 {
		note := fmt.Sprintf("Project-only MCP servers here (not shared by the aimux proxy): %s.", strings.Join(project, ", "))
		claude = strings.TrimSpace(claude + " " + note)
	}
	return HookReply{User: user, Claude: claude}
}
