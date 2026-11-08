package chatqueue

import (
	"regexp"
	"strings"
)

// workingPrefix is the line Claude Code puts before a message typed while
// it was working on a turn.
const workingPrefix = "The user sent a new message while you were working:"

// pasteTag is the marker Claude Code puts around a long paste.
var pasteTag = regexp.MustCompile(`</?pasted_content\b[^>]*>`)

// SamePrompt reports whether saved, a history turn's user text as Claude
// Code saved it, is the message text: Claude Code's wrappers removed, white
// space folded, and saved perhaps cut short with a trailing "…". It is the
// clients' rule (web timeline.mjs pendingShownInHistory, iOS Timeline.swift),
// pinned by api/testdata/clients/terminal/saved-prompt-match.json.
func SamePrompt(saved, text string) bool {
	head := strings.TrimSuffix(fold(asTyped(unwrapSavedPrompt(saved))), "…")
	return head != "" && strings.HasPrefix(fold(text), head)
}

func unwrapSavedPrompt(saved string) string {
	text := strings.TrimSpace(saved)
	text = strings.TrimPrefix(text, workingPrefix)
	return strings.TrimSpace(pasteTag.ReplaceAllString(text, ""))
}

// commandTag opens one of the tags Claude Code wraps a slash command, bash
// mode and their output in, written exactly so.
var commandTag = regexp.MustCompile(`<(command-name|command-message|command-args|local-command-stdout|local-command-stderr|command-stdout|command-stderr|bash-input|bash-stdout|bash-stderr|local-command-caveat)>`)

// asTyped is an unwrapped message in the form it was typed: each slash
// command as its name and args ("/tidy docs only"), bash mode as "!" and the
// input; output, the command's message and caveats left out. The clients'
// rule (Display in api/testdata/clients/README.md).
func asTyped(message string) string {
	var parts []string
	var run strings.Builder
	argsFor := -1      // index in parts of a <command-name> command that may still take args
	endRun := func() { // a command or an output ends the text before it
		if strings.TrimSpace(run.String()) != "" {
			parts = append(parts, run.String())
		}
		run.Reset()
		argsFor = -1
	}
	pos := 0
	for from := 0; from < len(message); {
		loc := commandTag.FindStringSubmatchIndex(message[from:])
		if loc == nil {
			break
		}
		start, open, tag := from+loc[0], from+loc[1], message[from+loc[2]:from+loc[3]]
		closing := "</" + tag + ">"
		end := strings.Index(message[open:], closing)
		if end < 0 { // never closed: the tag stays text
			from = open
			continue
		}
		body := message[open : open+end]
		run.WriteString(message[pos:start])
		pos = open + end + len(closing)
		from = pos
		switch tag {
		case "command-message", "local-command-caveat":
		case "command-args":
			if argsFor >= 0 && strings.TrimSpace(run.String()) == "" {
				if args := strings.TrimSpace(body); args != "" {
					parts[argsFor] += " " + args
				}
				argsFor = -1
				run.Reset()
			} else {
				run.WriteString(body)
			}
		case "command-name", "bash-input":
			name := strings.TrimSpace(body)
			if name == "" {
				continue
			}
			endRun()
			if tag == "bash-input" {
				parts = append(parts, "!"+name)
			} else {
				parts = append(parts, name)
				argsFor = len(parts) - 1
			}
		default: // output, which nobody typed
			if strings.TrimSpace(ansiColour.ReplaceAllString(body, "")) != "" {
				endRun()
			}
		}
	}
	run.WriteString(message[pos:])
	if strings.TrimSpace(run.String()) != "" {
		parts = append(parts, run.String())
	}
	return strings.Join(parts, "\n")
}

var ansiColour = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func fold(s string) string { return strings.Join(strings.Fields(s), " ") }
