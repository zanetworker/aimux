package tui

import "strings"

// commandAliases maps short aliases to full command names.
var commandAliases = map[string]string{
	"i": "instances",
	"l": "logs",
	"s": "session",
	"t": "teams",
	"c": "costs",
	"q": "quit",
	"?": "help",
	"n": "new",
}

// allCommands is the full list of available commands.
var allCommands = []string{
	"instances", "logs", "traces", "session", "teams", "tasks", "costs",
	"help", "new", "kill", "export", "export-otel", "send", "quit",
}

// resolveCommand resolves an alias or validates a full command name.
func resolveCommand(input string) string {
	input = strings.TrimSpace(strings.ToLower(input))
	if full, ok := commandAliases[input]; ok {
		return full
	}
	for _, cmd := range allCommands {
		if cmd == input {
			return cmd
		}
	}
	return ""
}

// commandCompletions returns commands matching a prefix.
func commandCompletions(prefix string) []string {
	prefix = strings.ToLower(prefix)
	var matches []string
	for _, cmd := range allCommands {
		if strings.HasPrefix(cmd, prefix) {
			matches = append(matches, cmd)
		}
	}
	return matches
}
