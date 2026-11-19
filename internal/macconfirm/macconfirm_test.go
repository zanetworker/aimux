package macconfirm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOsascript records the script it was given and answers with out/err.
type fakeOsascript struct {
	name   string
	args   []string
	out    string
	err    error
	called int
}

func (f *fakeOsascript) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.called++
	f.name, f.args = name, args
	return []byte(f.out), f.err
}

func TestConfirm_OnlyApproveCounts(t *testing.T) {
	for name, c := range map[string]struct {
		out  string
		err  error
		want Answer
	}{
		"approve":  {out: "button returned:Approve, gave up:false\n", want: Approved},
		"cancel":   {out: "", err: errors.New("execution error: User canceled. (-128)"), want: Cancelled},
		"timeout":  {out: "button returned:, gave up:true\n", want: TimedOut},
		"no gui":   {out: "", err: errors.New("execution error: No user interaction allowed. (-1713)"), want: Unavailable},
		"gibberish": {out: "button returned:Approve, gave up:true\n", want: TimedOut},
	} {
		f := &fakeOsascript{out: c.out, err: c.err}
		d := Dialog{Run: f.run}
		got, _ := d.Confirm(context.Background(), "aimux: approve tools", "Set jira:readonly")
		if got != c.want {
			t.Errorf("%s: answer %v, want %v", name, got, c.want)
		}
		if f.name != "osascript" {
			t.Errorf("%s: ran %q, want osascript (no shell)", name, f.name)
		}
	}
}

// The script is one -e argument for osascript, never a shell line; it has
// Approve and Cancel, Cancel the default, and gives up after 60 seconds.
func TestConfirm_Script(t *testing.T) {
	f := &fakeOsascript{out: "button returned:Approve, gave up:false"}
	if _, err := (Dialog{Run: f.run}).Confirm(context.Background(), "aimux: approve tools", "x"); err != nil {
		t.Fatal(err)
	}
	if len(f.args) != 2 || f.args[0] != "-e" {
		t.Fatalf("args %q, want -e <script>", f.args)
	}
	script := f.args[1]
	for _, want := range []string{`with title "aimux: approve tools"`, `buttons {"Cancel", "Approve"}`, `default button "Cancel"`, `cancel button "Cancel"`, `giving up after 60`} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %s:\n%s", want, script)
		}
	}
}

// Quotes and backslashes in the text cannot end the AppleScript string;
// control characters other than newlines are dropped, and long text is cut.
func TestQuote(t *testing.T) {
	if got := Quote(`say "hi" \ bye`); got != `"say \"hi\" \\ bye"` {
		t.Fatalf("Quote = %s", got)
	}
	if got := Quote("a\x07b\nc"); got != "\"ab\nc\"" {
		t.Fatalf("control characters: %q", got)
	}
	long := Quote(strings.Repeat("x", MaxText+500))
	if len(long) > MaxText+20 || !strings.HasSuffix(long, `…"`) {
		t.Fatalf("long text: %d bytes, suffix %q", len(long), long[len(long)-8:])
	}
}

func TestConfirm_ContextEndsTheDialog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeOsascript{err: context.Canceled}
	if got, _ := (Dialog{Run: f.run}).Confirm(ctx, "t", "x"); got == Approved {
		t.Fatal("a cancelled request approved")
	}
}

// The real AppleScript parser reads a quoted tool name back unchanged (no
// dialog is shown: the script only returns the string).
func TestQuote_RealOsascriptRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("osascript"); err != nil {
		t.Skip("no osascript on this system")
	}
	in := `tool "evil" \ with "quotes" & end`
	out, err := exec.Command("osascript", "-e", "return "+Quote(in)).Output() //nolint:gosec // G204: fixed command, the test's own string
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimRight(string(out), "\n"); got != in {
		t.Fatalf("osascript read %q, want %q", got, in)
	}
}

// A real subprocess that cancels the way osascript does (exit 1, "(-128)"
// on stderr) reads as Cancelled, not as a dialog that could not be shown.
func TestConfirm_CancelOnStderrOfARealProcess(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "osascript")
	body := "#!/bin/sh\necho '0:91: execution error: User canceled. (-128)' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { //nolint:gosec // G306: an executable test script
		t.Fatal(err)
	}
	got, err := (Dialog{Command: script}).Confirm(context.Background(), "t", "x")
	if got != Cancelled {
		t.Fatalf("answer %v (err %v), want Cancelled", got, err)
	}
	// Another failure (no GUI session) keeps its stderr in the error.
	body = "#!/bin/sh\necho 'execution error: No user interaction allowed. (-1713)' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { //nolint:gosec // G306: an executable test script
		t.Fatal(err)
	}
	got, err = (Dialog{Command: script}).Confirm(context.Background(), "t", "x")
	if got != Unavailable || err == nil || !strings.Contains(err.Error(), "-1713") {
		t.Fatalf("answer %v err %v, want Unavailable naming -1713", got, err)
	}
}

func TestFits(t *testing.T) {
	if !Fits(strings.Repeat("x", MaxText)) || Fits(strings.Repeat("x", MaxText+1)) {
		t.Fatal("Fits does not match MaxText")
	}
}
