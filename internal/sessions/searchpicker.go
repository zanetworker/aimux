package sessions

import (
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/search"
)

// The index-backed picker runs fzf as a split view: one line per session on
// the left, a preview on the right. fzf does no filtering itself; every
// keystroke re-queries the index through `aimux sessions rows`, and the
// preview comes from `aimux sessions preview`. Toggles (semantic ranking,
// automated sessions) live in a small state dir shared with those
// subprocesses through PickerStateEnv.

// PickerStateEnv names the env var that points subprocesses at the state dir.
const PickerStateEnv = "AIMUX_PICKER_STATE"

const (
	ansiBold   = "\033[1m"
	ansiYellow = "\033[33m"
)

// PickerState holds the picker's toggles in files so the short-lived rows and
// preview processes see the same settings.
type PickerState struct{ Dir string }

// PickerStateFromEnv returns the state named by PickerStateEnv (zero if unset).
func PickerStateFromEnv() PickerState { return PickerState{Dir: os.Getenv(PickerStateEnv)} }

func (s PickerState) file(name string) string { return filepath.Join(s.Dir, name) }

// IncludeAutomated reports whether automated sessions are shown (default no).
func (s PickerState) IncludeAutomated() bool {
	if s.Dir == "" {
		return false
	}
	_, err := os.Stat(s.file("automated"))
	return err == nil
}

// Mode is "keyword" (default: instant while typing) or "hybrid".
func (s PickerState) Mode() string {
	if s.Dir != "" {
		if b, err := os.ReadFile(s.file("mode")); err == nil && strings.TrimSpace(string(b)) == "hybrid" {
			return "hybrid"
		}
	}
	return "keyword"
}

var errNoStateDir = fmt.Errorf("picker state: %s is not set (these commands run inside the session picker)", PickerStateEnv)

// Toggle flips "automated" or "semantic".
func (s PickerState) Toggle(name string) error {
	if s.Dir == "" {
		return errNoStateDir
	}
	switch name {
	case "automated":
		if s.IncludeAutomated() {
			return os.Remove(s.file("automated"))
		}
		return os.WriteFile(s.file("automated"), nil, 0o600)
	case "semantic":
		next := "hybrid"
		if s.Mode() == "hybrid" {
			next = "keyword"
		}
		return os.WriteFile(s.file("mode"), []byte(next), 0o600)
	default:
		return fmt.Errorf("unknown toggle %q: valid values are automated, semantic", name)
	}
}

// SetMode sets the ranking mode: "keyword" or "hybrid".
func (s PickerState) SetMode(mode string) error {
	if s.Dir == "" {
		return errNoStateDir
	}
	if mode != "keyword" && mode != "hybrid" {
		return fmt.Errorf("unknown mode %q: valid values are keyword, hybrid", mode)
	}
	return os.WriteFile(s.file("mode"), []byte(mode), 0o600)
}

// SetLive records which sessions are running in a terminal right now.
func (s PickerState) SetLive(ids []string) error {
	if s.Dir == "" {
		return errNoStateDir
	}
	return os.WriteFile(s.file("live"), []byte(strings.Join(ids, "\n")), 0o600)
}

// Live returns the session ids recorded by SetLive.
func (s PickerState) Live() map[string]bool {
	out := map[string]bool{}
	if s.Dir == "" {
		return out
	}
	b, err := os.ReadFile(s.file("live"))
	if err != nil {
		return out
	}
	for _, id := range strings.Fields(string(b)) {
		out[id] = true
	}
	return out
}

// Header is the status and key-hint line shown above the list.
func (s PickerState) Header() string {
	mode := "keyword"
	if s.Mode() == "hybrid" {
		mode = "semantic"
	}
	auto := "automated hidden"
	if s.IncludeAutomated() {
		auto = "automated shown"
	}
	return fmt.Sprintf("%s · %s   │   enter/double-click open · ^s semantic · ^a automated · ^/ preview", mode, auto)
}

// projectPalette holds 256-color codes that stay readable on dark and light
// backgrounds; each project keeps one color so rows group visually.
var projectPalette = [...]int{81, 114, 179, 175, 141, 209, 117, 150, 216, 110}

func projectColor(project string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(project))
	return fmt.Sprintf("\033[38;5;%dm", projectPalette[h.Sum32()%uint32(len(projectPalette))]) // len is a small constant
}

// ageColor: bright within the hour, warm within the day, plain this week, dim after.
func ageColor(t time.Time) string {
	switch d := time.Since(t); {
	case d < time.Hour:
		return "\033[38;5;84m"
	case d < 24*time.Hour:
		return "\033[38;5;221m"
	case d < 7*24*time.Hour:
		return "\033[38;5;250m"
	default:
		return "\033[38;5;242m"
	}
}

// FormatRow renders one session as "<id>\t<display>"; fzf hides the id.
func FormatRow(r search.Result, live bool) string {
	marker, titleStyle := " ", ""
	if live {
		marker, titleStyle = ansiGreen+"●"+ansiReset, ansiBold
	}
	title := r.Title
	if title == "" {
		title = "(untitled)"
	}
	project := shortProject(r.CWD)
	return fmt.Sprintf("%s\t%s %s%-44s%s  %s%-14s%s  %s%s%s",
		r.SessionID, marker, titleStyle, clipRunes(title, 44), ansiReset,
		projectColor(project), clipRunes(project, 14), ansiReset,
		ageColor(r.ModTime), strings.TrimSuffix(shortAge(r.ModTime), " ago"), ansiReset)
}

var matchMark = regexp.MustCompile(`\[([^\]]+)\]`)

// FormatDetail renders the preview pane for one session.
func FormatDetail(d search.Detail, snippet string, live bool) string {
	var b strings.Builder
	title := d.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&b, "%s%s%s\n", ansiBold, title, ansiReset)
	fmt.Fprintf(&b, "%s%s%s\n", projectColor(shortProject(d.CWD)), d.CWD, ansiReset)
	status := shortAge(d.ModTime)
	if live {
		status = ansiGreen + "● live in iTerm" + ansiReset + " · " + status
	}
	fmt.Fprintf(&b, "%s%s%s · %s · %d exchanges\n", ansiCyan, d.SessionID, ansiReset, status, d.Exchanges)
	if d.Automated {
		fmt.Fprintf(&b, "%sautomated session%s\n", ansiDim, ansiReset)
	}
	if snippet != "" {
		fmt.Fprintf(&b, "\n%sMATCH%s\n%s\n", ansiYellow+ansiBold, ansiReset,
			matchMark.ReplaceAllString(snippet, ansiYellow+ansiBold+"$1"+ansiReset))
	}
	fmt.Fprintf(&b, "\n%sSTARTED WITH%s\n%s\n", ansiCyan+ansiBold, ansiReset, d.FirstPrompt)
	if len(d.RecentPrompts) > 0 {
		fmt.Fprintf(&b, "\n%sRECENTLY%s\n", "\033[38;5;175m"+ansiBold, ansiReset)
		for _, p := range d.RecentPrompts {
			fmt.Fprintf(&b, "› %s\n", p)
		}
	}
	return b.String()
}

// FzfArgs builds the split-view fzf invocation; self is the aimux binary.
// A non-zero listenPort lets background work push a reload into fzf.
func FzfArgs(self, query, header string, listenPort int) []string {
	bin := shellQuote(self)
	rows := bin + " sessions rows -- {q}"
	args := []string{
		"--ansi", "--no-multi", "--disabled", "--no-sort",
		"--delimiter=\t", "--with-nth=2..",
		"--layout=reverse", "--info=inline-right", "--prompt=session> ",
		"--query=" + query,
		"--header=" + header,
		"--bind=start:reload(" + rows + ")",
		// debounce: fzf cancels a pending reload when the next key arrives,
		// so only the query you pause on runs (and, in hybrid mode, embeds)
		"--bind=change:reload(sleep 0.15; " + rows + ")+first",
		"--bind=ctrl-s:transform-header(" + bin + " sessions picker-toggle semantic)+reload(" + rows + ")",
		"--bind=ctrl-a:transform-header(" + bin + " sessions picker-toggle automated)+reload(" + rows + ")",
		"--bind=ctrl-/:toggle-preview",
		"--preview=" + bin + " sessions preview {1} -- {q}",
		// side by side when wide; preview below the list under 110 columns
		"--preview-window=right,55%,wrap,border-left,<110(down,50%,wrap,border-top)",
	}
	if listenPort > 0 {
		args = append(args, fmt.Sprintf("--listen=127.0.0.1:%d", listenPort))
	}
	return args
}

// ReloadAction is the fzf action that re-runs the rows query.
func ReloadAction(self string) string {
	return "reload(" + shellQuote(self) + " sessions rows -- {q})"
}

// postAction sends an action to an fzf started with --listen.
func postAction(port int, action string) error {
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d", port), "text/plain", strings.NewReader(action)) // #nosec G107 -- loopback only
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// SearchPick runs the split-view picker and returns the chosen session id.
// The picker opens straight away on the cached index; background (index
// refresh, live-session discovery) runs meanwhile and its results are pushed
// into the open picker with a reload.
func SearchPick(self, query string, st PickerState, background func()) (string, error) {
	fzfBin, err := exec.LookPath("fzf")
	if err != nil {
		return "", fmt.Errorf("the session picker needs fzf (brew install fzf)")
	}
	port := freePort()
	cmd := exec.Command(fzfBin, FzfArgs(self, query, st.Header(), port)...) // #nosec G204
	cmd.Env = append(os.Environ(), PickerStateEnv+"="+st.Dir)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	if background != nil {
		go func() {
			background()
			if port > 0 {
				// fzf may still be starting; a few quick retries cover that
				for i := 0; i < 20 && postAction(port, ReloadAction(self)) != nil; i++ {
					time.Sleep(100 * time.Millisecond)
				}
			}
		}()
	}
	out, err := cmd.Output()
	if err != nil {
		return "", ErrCancelled
	}
	id := ParseSelectedID(string(out))
	if id == "" {
		return "", ErrCancelled
	}
	return id, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
