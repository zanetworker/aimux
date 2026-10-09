package jump

import (
	"errors"
	"strings"
	"testing"
)

func stubOsascript(t *testing.T, out string, err error) *[]string {
	t.Helper()
	var gotArgs []string
	orig := runOsascript
	runOsascript = func(script string, args ...string) (string, error) {
		gotArgs = append([]string{script}, args...)
		return out, err
	}
	t.Cleanup(func() { runOsascript = orig })
	return &gotArgs
}

func TestITerm2FocusByTTY(t *testing.T) {
	got := stubOsascript(t, "ok\n", nil)
	found, err := ITerm2FocusByTTY("/dev/ttys031")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want found", found, err)
	}
	script, args := (*got)[0], (*got)[1:]
	if len(args) != 1 || args[0] != "/dev/ttys031" {
		t.Errorf("tty must be passed as an argument, got args %v", args)
	}
	if strings.Contains(script, "ttys031") {
		t.Error("tty interpolated into the script (injection risk)")
	}
	// window references by id: index/loop-variable references select the wrong pane
	if !strings.Contains(script, "window id") || strings.Contains(script, "repeat with w in windows") {
		t.Errorf("script must address windows by id:\n%s", script)
	}
}

func TestITerm2FocusByTTY_NotFoundAndErrors(t *testing.T) {
	stubOsascript(t, "missing\n", nil)
	if found, err := ITerm2FocusByTTY("/dev/ttys099"); found || err != nil {
		t.Errorf("missing pane: found=%v err=%v", found, err)
	}

	stubOsascript(t, "", errors.New("iTerm not running"))
	if _, err := ITerm2FocusByTTY("/dev/ttys001"); err == nil {
		t.Error("osascript failure: want error")
	}

	calls := stubOsascript(t, "ok", nil)
	if _, err := ITerm2FocusByTTY(""); err == nil || len(*calls) != 0 {
		t.Errorf("empty tty: err=%v calls=%d, want error and no osascript call", err, len(*calls))
	}
}

func TestParsePSTTY(t *testing.T) {
	cases := map[string]string{
		"ttys031\n": "/dev/ttys031",
		"  ttys002": "/dev/ttys002",
		"??\n":      "", // no controlling terminal (background process)
		"":          "",
	}
	for in, want := range cases {
		if got := parsePSTTY(in); got != want {
			t.Errorf("parsePSTTY(%q) = %q, want %q", in, got, want)
		}
	}
}
