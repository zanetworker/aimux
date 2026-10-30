package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/terminput"
)

func TestTerminalDrivers_ITerm2OnlyOnMac(t *testing.T) {
	drivers, closeAll := terminalDrivers()
	defer closeAll()
	if runtime.GOOS == "darwin" {
		if len(drivers) != 1 || drivers[0].Name() != "iterm2" {
			t.Fatalf("drivers %v, want iterm2 on macOS", drivers)
		}
		return
	}
	if len(drivers) != 0 {
		t.Fatalf("drivers %v, want none off macOS", drivers)
	}
}

type noSessions struct{}

func (noSessions) TerminalPID(string) (int, bool) { return 0, false }

func TestBuildTerminalInput_OffOpensNoLog(t *testing.T) {
	home := t.TempDir()
	svc, closeLog, err := buildTerminalInput(config.TerminalInputConfig{}, home, noSessions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()
	if st := svc.Status(context.Background(), "s1", "idle"); st.CanType || st.Reason != "disabled" {
		t.Fatalf("status %+v, want disabled", st)
	}
	if _, err := os.Stat(filepath.Join(home, ".aimux", "logs")); !os.IsNotExist(err) {
		t.Fatalf("log dir exists while off: %v", err)
	}
}

func TestBuildTerminalInput_OnLogsAttemptsWithoutText(t *testing.T) {
	home := t.TempDir()
	svc, closeLog, err := buildTerminalInput(config.TerminalInputConfig{Enabled: true}, home, noSessions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Send(context.Background(), "s1", "secret words", ""); !errors.Is(err, terminput.ErrNotInTerminal) {
		t.Fatalf("send: %v, want not in terminal", err)
	}
	closeLog()
	path := filepath.Join(home, ".aimux", "logs", "terminal-input.log")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v, want 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(path) // #nosec G304 -- test temp dir
	if len(data) == 0 || strings.Contains(string(data), "secret") {
		t.Fatalf("log %q, want one line without the text", data)
	}
}

func TestBuildTerminalInput_UnwritableLogIsAnError(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".aimux"), []byte("a file, not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildTerminalInput(config.TerminalInputConfig{Enabled: true}, home, noSessions{}); err == nil {
		t.Fatal("want an error when the log cannot be opened")
	}
}
