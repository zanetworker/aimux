package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/waiting"
)

// hooksExecutable is the running aimux binary and hooksLookPath finds
// aimux on PATH; hooksForbiddenDirs are where a build does not last (the
// temp dir, the Go build cache). Tests replace them.
var (
	hooksExecutable    = os.Executable
	hooksLookPath      = exec.LookPath
	hooksForbiddenDirs = func() []string {
		dirs := []string{os.TempDir()}
		if c := os.Getenv("GOCACHE"); c != "" {
			dirs = append(dirs, c)
		} else if c, err := os.UserCacheDir(); err == nil {
			dirs = append(dirs, filepath.Join(c, "go-build"))
		}
		return dirs
	}
)

// hookBinary is the aimux the hooks should run: the one on PATH when it is
// this same file (a stable name, such as a Homebrew link), else this one.
// A binary in the temp dir or the Go build cache (go run, go test) is
// refused: it disappears, and every tool call would then fail the hook.
func hookBinary() (string, error) { return stableBinary("aimux remote hooks install") }

// stableBinary is hookBinary's rule for anything that runs aimux later (a
// hook, a launchd agent); rerun is the command to name in the refusal.
func stableBinary(rerun string) (string, error) {
	bin, err := hooksExecutable()
	if err != nil {
		return "", fmt.Errorf("find the aimux binary: %w", err)
	}
	if bin, err = filepath.Abs(bin); err != nil {
		return "", err
	}
	for _, dir := range hooksForbiddenDirs() {
		if dir != "" && within(bin, dir) {
			return "", fmt.Errorf("%s is a temporary build (under %s); install aimux (make install, or go install) and run `%s` from there", bin, dir, rerun)
		}
	}
	if onPath, err := hooksLookPath("aimux"); err == nil {
		if abs, err := filepath.Abs(onPath); err == nil && sameFile(abs, bin) {
			return abs, nil
		}
	}
	return bin, nil
}

// within: path is dir or below it, symlinks resolved where they exist
// (macOS's /var is /private/var).
func within(path, dir string) bool {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			return filepath.Join(r, filepath.Base(p))
		}
		return filepath.Clean(p)
	}
	for _, pair := range [][2]string{{filepath.Clean(path), filepath.Clean(dir)}, {resolve(path), resolve(dir)}} {
		if rel, err := filepath.Rel(pair[1], pair[0]); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func newRemoteHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Claude Code hooks that tell remote serve when a terminal session waits for permission",
		Long: "Install hooks in ~/.claude/settings.json so a Claude Code session open in a terminal that " +
			"shows a permission prompt sends a push notification and lists under Needs you on the phone. " +
			"The hooks only write and remove marker files in ~/.aimux/waiting; they make no network call.",
	}
	cmd.AddCommand(newRemoteHooksInstallCmd(), newRemoteHooksUninstallCmd(), newRemoteHooksStatusCmd(), newRemoteHooksNotifyCmd())
	return cmd
}

func newRemoteHooksInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Add aimux's hooks to ~/.claude/settings.json (backed up first; your hooks are kept)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := claudeSettingsPath()
			if err != nil {
				return err
			}
			bin, err := hookBinary()
			if err != nil {
				return err
			}
			command := waiting.HookCommand(bin)
			ch, err := waiting.Install(path, command, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(ch.Added)+len(ch.Updated) == 0 {
				_, _ = fmt.Fprintf(out, "aimux's hooks are already installed in %s.\n", path)
				return nil
			}
			if ch.Backup != "" {
				_, _ = fmt.Fprintf(out, "Backed up %s to %s.\n", path, ch.Backup)
			}
			if len(ch.Added) > 0 {
				_, _ = fmt.Fprintf(out, "Added hooks for %s to %s.\n", strings.Join(ch.Added, ", "), path)
			}
			if len(ch.Updated) > 0 {
				_, _ = fmt.Fprintf(out, "Updated the command of %s.\n", strings.Join(ch.Updated, ", "))
			}
			_, _ = fmt.Fprintf(out, "Each runs: %s\nSessions started from now on use them; restart open sessions to pick them up.\n", command)
			return nil
		},
	}
}

func newRemoteHooksUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove only aimux's hooks from ~/.claude/settings.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := claudeSettingsPath()
			if err != nil {
				return err
			}
			ch, err := waiting.Uninstall(path, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(ch.Removed) == 0 {
				_, _ = fmt.Fprintf(out, "No aimux hooks in %s.\n", path)
				return nil
			}
			_, _ = fmt.Fprintf(out, "Backed up %s to %s.\nRemoved aimux's hooks for %s.\n", path, ch.Backup, strings.Join(ch.Removed, ", "))
			return nil
		},
	}
}

func newRemoteHooksStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Say whether aimux's hooks are installed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := claudeSettingsPath()
			if err != nil {
				return err
			}
			st, err := waiting.Status(path)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case len(st.Installed) == 0:
				_, _ = fmt.Fprintf(out, "aimux's hooks are not installed in %s. Run `aimux remote hooks install`.\n", path)
				return nil
			case len(st.Missing) > 0:
				_, _ = fmt.Fprintf(out, "aimux's hooks are partly installed in %s: missing %s. Run `aimux remote hooks install`.\n", path, strings.Join(st.Missing, ", "))
			default:
				_, _ = fmt.Fprintf(out, "aimux's hooks are installed in %s (%s).\n", path, strings.Join(st.Installed, ", "))
			}
			want := ""
			if bin, err := hookBinary(); err == nil {
				want = waiting.HookCommand(bin)
			}
			for _, c := range st.Commands {
				if bin, ok := waiting.CommandBinary(c); ok {
					if _, err := os.Stat(bin); err != nil {
						_, _ = fmt.Fprintf(out, "A hook runs %s, which does not exist, so no alerts are sent; run `aimux remote hooks install` from the aimux you keep.\n", bin)
						continue
					}
				}
				if want != "" && c != want {
					_, _ = fmt.Fprintf(out, "A hook runs %s, not this binary; run `aimux remote hooks install` to point it here.\n", c)
				}
			}
			return nil
		},
	}
}

// newRemoteHooksNotifyCmd is what the hooks run. It never fails Claude Code:
// it always exits 0 and prints nothing; a failure is one line in
// ~/.aimux/logs/hooks.log that never holds the hook's text.
func newRemoteHooksNotifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "notify",
		Short:  "Read one Claude Code hook call on stdin (run by the installed hooks)",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil //nolint:nilerr // nowhere to write; Claude Code must not see a failure
			}
			if err := waiting.HandleHook(waiting.Dir(home), cmd.InOrStdin(), time.Now()); err != nil {
				logHookFailure(home)
			}
			return nil
		},
	}
}

// logHookFailure appends one line that says only that a call failed: the
// error's text could quote the hook's input.
func logHookFailure(home string) {
	dir := filepath.Join(home, ".aimux", "logs")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "hooks.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // fixed path under the owner's home
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s remote hooks notify: a hook call was not applied\n", time.Now().UTC().Format(time.RFC3339))
}
