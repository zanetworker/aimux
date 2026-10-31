// Package iterm2 is the terminput driver for iTerm2. It drives iTerm2 through
// Apple Events from one long-lived `osascript -l JavaScript` helper per
// process, speaking one JSON object per line each way. Text travels as JSON
// data on the helper's stdin, never inside script source.
package iterm2

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/zanetworker/aimux/internal/terminput"
)

//go:embed helper.js
var helperJS string

const (
	name           = "iterm2"
	defaultTimeout = 3 * time.Second
	closeWait      = time.Second
	stderrCap      = 4096
)

// ErrPaneGone means the pane was not found again after iTerm2 reported it
// missing.
var ErrPaneGone = errors.New("iterm2: the session's pane is gone")

var errMissing = errors.New("iterm2: session missing")

// Options configures a Driver. Zero values use the defaults.
type Options struct {
	// Command builds the helper process; ctx ends when the Driver closes.
	// Default: osascript -l JavaScript -e <the embedded helper>.
	Command func(ctx context.Context) *exec.Cmd
	// Timeout bounds one request; on expiry the helper is killed and
	// restarted on next use. Default 3 s.
	Timeout time.Duration
}

type loc struct {
	Window  int    `json:"window"`
	Session string `json:"session"`
	TTY     string `json:"tty"`
}

type request struct {
	ID      int    `json:"id"`
	Op      string `json:"op"`
	Window  int    `json:"window,omitempty"`
	Session string `json:"session,omitempty"`
	Text    string `json:"text,omitempty"`
	Paste   bool   `json:"paste,omitempty"`
	Key     string `json:"key,omitempty"`
}

type response struct {
	ID        int    `json:"id"`
	Error     string `json:"error"`
	Sessions  []loc  `json:"sessions"`
	Text      string `json:"text"`
	Frontmost bool   `json:"frontmost"`
	TTY       string `json:"tty"`
}

// Driver implements terminput.Driver for iTerm2. Requests are serialised.
// A Pane's ID is the session's tty; window and session ids stay inside.
type Driver struct {
	opts Options

	mu    sync.Mutex
	h     *helper
	cache map[string]loc // tty -> where iTerm2 shows it
	seq   int
}

var (
	_ terminput.Driver        = (*Driver)(nil)
	_ terminput.FocusReporter = (*Driver)(nil)
)

// New returns a Driver; the helper starts on first use.
func New(opts Options) *Driver {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.Command == nil {
		opts.Command = func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", helperJS) // #nosec G204 -- fixed, embedded script
		}
	}
	return &Driver{opts: opts, cache: map[string]loc{}}
}

// Name is "iterm2".
func (d *Driver) Name() string { return name }

// Probe lists iTerm2's sessions once, so macOS asks for Automation access
// while someone is at the Mac. It reports terminput.ErrAutomationDenied when
// access is refused.
func (d *Driver) Probe(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.relocate(ctx)
}

// Focused reports whether iTerm2 is the frontmost app and the tty of the
// focused session of its current window ("" when it has no window). While
// iTerm2 is not running it reports false and "". It never activates or
// focuses anything.
func (d *Driver) Focused(ctx context.Context) (bool, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, err := d.call(ctx, request{Op: "focus"})
	if err != nil {
		return false, "", fmt.Errorf("iterm2: focus: %w", err)
	}
	return r.Frontmost, r.TTY, nil
}

// Locate finds the iTerm2 session whose tty is t.TTY, from the cache when it
// has one; it reports false when no iTerm2 session has that tty.
func (d *Driver) Locate(ctx context.Context, t terminput.Target) (terminput.Pane, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t.TTY == "" {
		return terminput.Pane{}, false, nil
	}
	if _, ok := d.cache[t.TTY]; !ok {
		if err := d.relocate(ctx); err != nil {
			return terminput.Pane{}, false, err
		}
		if _, ok := d.cache[t.TTY]; !ok {
			return terminput.Pane{}, false, nil
		}
	}
	return terminput.Pane{Driver: name, ID: t.TTY}, true, nil
}

// Type types text without Enter. Text for which terminput.PasteText is true
// (a newline or tab, or over 200 characters) goes in as a bracketed paste.
// Text with control characters is refused with terminput.ErrBadText (a
// second check; the service refuses it first).
func (d *Driver) Type(ctx context.Context, p terminput.Pane, text string) error {
	if err := terminput.CheckText(text); err != nil {
		return err
	}
	_, err := d.onPane(ctx, p, request{Op: "send", Text: text, Paste: terminput.PasteText(text)})
	return err
}

var validKey = regexp.MustCompile(`^([1-9]|escape|enter|clear)$`)

// Key presses one key: a digit 1 to 9, escape or enter.
func (d *Driver) Key(ctx context.Context, p terminput.Pane, k terminput.Key) error {
	if !validKey.MatchString(string(k)) {
		return fmt.Errorf("iterm2: unsupported key %q", string(k))
	}
	_, err := d.onPane(ctx, p, request{Op: "key", Key: string(k)})
	return err
}

// Screen returns the session's visible text.
func (d *Driver) Screen(ctx context.Context, p terminput.Pane) (string, error) {
	r, err := d.onPane(ctx, p, request{Op: "screen"})
	return r.Text, err
}

// Close ends the helper: it closes stdin, waits up to 1 s, then kills.
// The Driver stays usable; the next call starts a new helper.
func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.h == nil {
		return nil
	}
	d.h.close(closeWait)
	d.h = nil
	return nil
}

// onPane runs req against the pane's session, locating it when uncached and
// once more when iTerm2 reports it missing. Callers do not hold mu.
func (d *Driver) onPane(ctx context.Context, p terminput.Pane, req request) (response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tty := p.ID
	for attempt := 0; ; attempt++ {
		l, ok := d.cache[tty]
		if !ok {
			if err := d.relocate(ctx); err != nil {
				return response{}, err
			}
			if l, ok = d.cache[tty]; !ok {
				return response{}, ErrPaneGone
			}
		}
		req.Window, req.Session = l.Window, l.Session
		r, err := d.call(ctx, req)
		if errors.Is(err, errMissing) && attempt == 0 {
			delete(d.cache, tty)
			continue
		}
		if errors.Is(err, errMissing) {
			return response{}, ErrPaneGone
		}
		return r, err
	}
}

// relocate replaces the cache from one bulk listing. Caller holds mu.
func (d *Driver) relocate(ctx context.Context) error {
	r, err := d.call(ctx, request{Op: "locate"})
	if err != nil {
		return err
	}
	d.cache = make(map[string]loc, len(r.Sessions))
	for _, s := range r.Sessions {
		if s.TTY != "" {
			d.cache[s.TTY] = s
		}
	}
	return nil
}

// call sends one request and waits for its reply. Caller holds mu.
func (d *Driver) call(ctx context.Context, req request) (response, error) {
	if d.h == nil {
		h, err := startHelper(d.opts.Command)
		if err != nil {
			return response{}, err
		}
		d.h = h
	}
	d.seq++
	req.ID = d.seq
	line, err := asciiJSON(req)
	if err != nil {
		return response{}, err
	}
	h := d.h
	if _, err := h.stdin.Write(line); err != nil {
		d.drop()
		return response{}, fmt.Errorf("iterm2: write to helper: %w", err)
	}
	timer := time.NewTimer(d.opts.Timeout)
	defer timer.Stop()
	for {
		var b []byte
		select {
		case b = <-h.replies:
		case <-h.done:
			select {
			case b = <-h.replies: // a reply that raced the exit
			default:
				h.cancel()
				d.h = nil
				msg := h.stderrText()
				if msg == "" {
					msg = "exited"
				}
				return response{}, replyError("helper " + msg)
			}
		case <-timer.C:
			d.drop()
			return response{}, fmt.Errorf("iterm2: %s took longer than %v; helper restarted", req.Op, d.opts.Timeout)
		case <-ctx.Done():
			d.drop()
			return response{}, fmt.Errorf("iterm2: %s: %w", req.Op, ctx.Err())
		}
		var r response
		if err := json.Unmarshal(b, &r); err != nil {
			d.drop()
			return response{}, fmt.Errorf("iterm2: bad reply from helper: %w", err)
		}
		if r.ID != req.ID {
			continue // a reply to an earlier request that gave up
		}
		if r.Error != "" {
			return r, replyError(r.Error)
		}
		return r, nil
	}
}

// drop kills the helper; the next call starts a new one. Caller holds mu.
func (d *Driver) drop() {
	if d.h != nil {
		d.h.close(0)
		d.h = nil
	}
}

// replyError maps a helper error to an error value.
func replyError(msg string) error {
	switch {
	case msg == "missing":
		return errMissing
	case msg == "automation_denied", strings.Contains(msg, "-1743"), strings.Contains(msg, "Not authorized to send Apple events"):
		return terminput.ErrAutomationDenied
	default:
		return fmt.Errorf("iterm2: %s", msg)
	}
}

// asciiJSON encodes v as one JSON line with every non-ASCII character
// escaped, so the helper never sees a multi-byte character split by a read.
func asciiJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(b) + 1)
	for _, r := range string(b) {
		if r < utf8.RuneSelf {
			out.WriteByte(byte(r)) // #nosec G115 -- r < 0x80
			continue
		}
		if r1, r2 := utf16.EncodeRune(r); r1 != utf8.RuneError {
			_, _ = fmt.Fprintf(&out, `\u%04x\u%04x`, r1, r2)
		} else {
			_, _ = fmt.Fprintf(&out, `\u%04x`, r)
		}
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

type helper struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	stdin   io.WriteCloser
	replies chan []byte
	done    chan struct{} // closed once the process has exited and stdout drained
	stderr  *capped
}

func startHelper(command func(context.Context) *exec.Cmd) (*helper, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := command(ctx)
	h := &helper{cmd: cmd, cancel: cancel, replies: make(chan []byte, 8), done: make(chan struct{}), stderr: &capped{}}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("iterm2: helper stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("iterm2: helper stdout: %w", err)
	}
	cmd.Stderr = h.stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("iterm2: start helper: %w", err)
	}
	h.stdin = stdin
	go func() {
		defer close(h.done)
		r := bufio.NewReader(stdout)
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case h.replies <- line:
				case <-ctx.Done():
				}
			}
			if err != nil {
				break
			}
		}
		_ = cmd.Wait()
	}()
	return h, nil
}

// close ends stdin, waits up to wait for the helper to exit, then kills it.
func (h *helper) close(wait time.Duration) {
	_ = h.stdin.Close()
	if wait > 0 {
		select {
		case <-h.done:
			h.cancel()
			return
		case <-time.After(wait):
		}
	}
	h.cancel() // kills the process (exec.CommandContext)
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
}

func (h *helper) stderrText() string {
	h.stderr.mu.Lock()
	defer h.stderr.mu.Unlock()
	return strings.TrimSpace(h.stderr.buf.String())
}

// capped keeps the first few KB of the helper's stderr.
type capped struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if room := stderrCap - c.buf.Len(); room > 0 {
		if n > room {
			p = p[:room]
		}
		c.buf.Write(p)
	}
	return n, nil
}
