// Command fakeagent stands in for the claude binary when recording the README
// demo. Started by the demo entrypoint, it only has to exist so aimux discovers
// it by process name. Started by aimux as `claude --resume <id>` (the split
// view), it draws that session's transcript in a Claude-Code-like layout so the
// session pane shows real content.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	reset = "\x1b[0m"
	bold  = "\x1b[1m"
	red   = "\x1b[31m"
	green = "\x1b[32m"
	blue  = "\x1b[34m"
	gray  = "\x1b[90m"
)

type block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   map[string]any  `json:"input"`
	Content json.RawMessage `json:"content"`
}

type entry struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func main() {
	if b := os.Getenv("FAKE_BANNER"); b != "" {
		fmt.Println(b)
	}
	if id := resumeID(os.Args[1:]); id != "" {
		matches, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".claude", "projects", "*", id+".jsonl"))
		if len(matches) > 0 {
			fmt.Print(render(matches[0]))
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}

// resumeID returns the value of --resume, or "" when it is absent.
func resumeID(args []string) string {
	for i, a := range args {
		if a == "--resume" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// render turns a Claude Code session file into terminal text. It returns ""
// when the file cannot be read; lines that are not valid JSON are skipped.
func render(path string) string {
	f, err := os.Open(path) // #nosec G304 G703 -- demo only, path comes from a glob under $HOME
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }() // read-only file; a close error cannot lose data

	var out strings.Builder
	out.WriteString(bold + blue + "✻ Claude Code" + reset + gray + "  resumed session" + reset + "\n\n")
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		var prompt string
		if json.Unmarshal(e.Message.Content, &prompt) == nil {
			if e.Type == "user" {
				out.WriteString(gray + "> " + reset + bold + prompt + reset + "\n\n")
			}
			continue
		}
		var blocks []block
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				out.WriteString("⏺ " + b.Text + "\n\n")
			case "tool_use":
				out.WriteString(toolCall(b))
			case "tool_result":
				var res string
				_ = json.Unmarshal(b.Content, &res) // non-string results are shown as empty
				if first := strings.SplitN(res, "\n", 2)[0]; first != "" {
					out.WriteString(gray + "  ⎿  " + first + reset + "\n\n")
				}
			}
		}
	}
	bar := strings.Repeat("─", 50)
	out.WriteString(gray + "╭" + bar + "╮\n│ > " + strings.Repeat(" ", 47) + "│\n╰" + bar + "╯" + reset + "\n")
	return out.String()
}

func toolCall(b block) string {
	str := func(k string) string { s, _ := b.Input[k].(string); return s }
	arg := str("file_path")
	if arg == "" {
		arg = str("command")
	}
	s := green + "⏺ " + reset + bold + b.Name + reset + "(" + arg + ")\n"
	if b.Name == "Edit" {
		for _, l := range strings.Split(str("old_string"), "\n") {
			s += red + "    - " + l + reset + "\n"
		}
		for _, l := range strings.Split(str("new_string"), "\n") {
			s += green + "    + " + l + reset + "\n"
		}
	}
	return s
}
