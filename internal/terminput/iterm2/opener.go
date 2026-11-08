package iterm2

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

//go:embed opentab.js
var opentabJS string

// ErrNotAuthorized means macOS refused aimux Apple events to iTerm2.
var ErrNotAuthorized = errors.New("iterm2: automation not allowed")

// MaxPanes is how many aimux panes one aimux window holds; the next open
// starts a new aimux window.
const MaxPanes = 10

// Opener opens panes in aimux's own iTerm2 windows: up to MaxPanes panes a
// window, in two columns, each marked with the session variable user.aimux
// = 1 and named "aimux · <title>". The script finds those windows by that
// variable every time; nothing is stored. It is separate from the Driver's
// helper, which never creates windows. Callers must pass OpenTab a context
// with a deadline: the script starts iTerm2 when it is not running, which
// can be slow.
type Opener struct {
	// Run runs the opener script; args are the script path and its three
	// arguments. Nil runs osascript -l JavaScript.
	Run func(ctx context.Context, args ...string) ([]byte, error)

	mu sync.Mutex // one open at a time: scan, split and reply
}

type openReply struct {
	Window int    `json:"window"`
	Pane   int    `json:"pane"`
	Error  string `json:"error"`
}

// OpenTab opens a pane named "aimux · title" in the newest aimux window with
// room (a new window when none has) and types ShellLine(argv, dir) and Enter
// into it. title and the line reach the script as arguments, never as
// source. Opens are serialized, so concurrent ones fill the same window. It
// returns ErrNotAuthorized when macOS denies automation.
func (o *Opener) OpenTab(ctx context.Context, title string, argv []string, dir string) error {
	_, err := o.open(ctx, title, argv, dir)
	return err
}

// open is OpenTab, also returning the script's reply.
func (o *Opener) open(ctx context.Context, title string, argv []string, dir string) (openReply, error) {
	var r openReply
	if len(argv) == 0 {
		return r, errors.New("iterm2: open pane: empty command")
	}
	script, err := os.CreateTemp("", "aimux-opentab-*.js")
	if err != nil {
		return r, fmt.Errorf("iterm2: open pane: %w", err)
	}
	defer func() { _ = os.Remove(script.Name()) }()
	if _, err := script.WriteString(opentabJS); err != nil {
		_ = script.Close()
		return r, fmt.Errorf("iterm2: open pane: write script: %w", err)
	}
	if err := script.Close(); err != nil {
		return r, fmt.Errorf("iterm2: open pane: write script: %w", err)
	}

	run := o.Run
	if run == nil {
		run = osascript
	}
	o.mu.Lock()
	out, err := run(ctx, script.Name(), strconv.Itoa(MaxPanes), title, ShellLine(argv, dir))
	o.mu.Unlock()
	if err != nil {
		return r, fmt.Errorf("iterm2: open pane: %w", err)
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &r); err != nil {
		return r, fmt.Errorf("iterm2: open pane: bad reply: %w", err)
	}
	switch {
	case r.Error == "not_authorized":
		return r, ErrNotAuthorized
	case r.Error != "":
		return r, fmt.Errorf("iterm2: open pane: %s", r.Error)
	case r.Window <= 0:
		return r, errors.New("iterm2: open pane: reply has no window id")
	case r.Pane < 1 || r.Pane > MaxPanes:
		return r, fmt.Errorf("iterm2: open pane: reply has pane %d, want 1..%d", r.Pane, MaxPanes)
	}
	return r, nil
}

func osascript(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "osascript", append([]string{"-l", "JavaScript"}, args...)...) // #nosec G204 -- embedded script; the rest is argv data
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ShellLine is `cd '<dir>' && '<argv0>' '<arg>'…`, every word in POSIX single
// quotes, so the shell runs it with no expansion.
func ShellLine(argv []string, dir string) string {
	words := make([]string, 0, len(argv))
	for _, a := range argv {
		words = append(words, shellQuote(a))
	}
	return "cd " + shellQuote(dir) + " && " + strings.Join(words, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
