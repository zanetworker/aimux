//go:build darwin

package keyring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

type stdinCall struct {
	bin   string
	args  []string
	stdin string
}

// fakeRunStdin replaces the stdin seam; fn decides the result.
func fakeRunStdin(t *testing.T, fn func() ([]byte, error)) *[]stdinCall {
	t.Helper()
	var calls []stdinCall
	old := runStdin
	runStdin = func(_ context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		calls = append(calls, stdinCall{bin, args, string(stdin)})
		return fn()
	}
	t.Cleanup(func() { runStdin = old })
	return &calls
}

const storeSecret = "Bearer s3cr3t-value-xyz"

func TestStore_ValueNeverInArgv(t *testing.T) {
	calls := fakeRunStdin(t, func() ([]byte, error) { return nil, nil })
	reads := fakeRun(t, out(storeSecret+"\n", nil))
	if err := Store("aimux-mcp-proxy/x", storeSecret); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v", *calls)
	}
	c := (*calls)[0]
	if c.bin != "/usr/bin/security" || strings.Join(c.args, " ") != "-i" {
		t.Fatalf("argv = %s %v", c.bin, c.args)
	}
	want := "add-generic-password -U -a aimux -s aimux-mcp-proxy/x -X " + hex.EncodeToString([]byte(storeSecret)) + "\n"
	if c.stdin != want {
		t.Fatalf("stdin = %q, want %q", c.stdin, want)
	}
	if strings.Contains(c.stdin, storeSecret) {
		t.Fatal("plain value on stdin")
	}
	if got := strings.Join((*reads)[0].args, " "); got != "find-generic-password -a aimux -s aimux-mcp-proxy/x -w" {
		t.Fatalf("read-back argv = %s (must name account aimux)", got)
	}
	for _, r := range *reads {
		for _, a := range r.args {
			if strings.Contains(a, storeSecret) {
				t.Fatalf("value in read-back argv: %v", r.args)
			}
		}
	}
}

func TestStore_ReadsBackAndComparesValue(t *testing.T) {
	fakeRunStdin(t, func() ([]byte, error) { return nil, nil })
	fakeRun(t, out("other-value\n", nil))
	err := Store("aimux-mcp-proxy/x", storeSecret)
	if err == nil || err.Error() != "keychain item aimux-mcp-proxy/x did not take the new value" {
		t.Fatalf("err = %v", err)
	}
}

func TestStore_RejectsServiceWithSpaceOrQuote(t *testing.T) {
	calls := fakeRunStdin(t, func() ([]byte, error) { return nil, nil })
	for _, svc := range []string{"a b", `a"b`, "a'b", "a\nb", ""} {
		if err := Store(svc, storeSecret); err == nil {
			t.Fatalf("Store(%q) accepted", svc)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("ran security: %+v", *calls)
	}
}

func TestStore_ToolFailureKeepsOnlyExitStatus(t *testing.T) {
	code := exitErr(t, 45)
	fakeRunStdin(t, func() ([]byte, error) { return []byte("oops " + storeSecret), code })
	err := Store("aimux-mcp-proxy/x", storeSecret)
	if err == nil || !strings.Contains(err.Error(), "exit status 45") || strings.Contains(err.Error(), storeSecret) || strings.Contains(err.Error(), "oops") {
		t.Fatalf("err = %v", err)
	}
}

func TestStore_RefusesValueTooLongForOneLine(t *testing.T) {
	calls := fakeRunStdin(t, func() ([]byte, error) { return nil, nil })
	fakeRun(t, out("x", nil))
	err := Store("aimux-mcp-proxy/x", strings.Repeat("v", 2000))
	if err == nil || !strings.Contains(err.Error(), "value too long") || len(*calls) != 0 {
		t.Fatalf("err = %v, calls = %d", err, len(*calls))
	}
	big := strings.Repeat("v", 1900)
	fakeRun(t, out(big, nil))
	if err := Store("aimux-mcp-proxy/x", big); err != nil {
		t.Fatalf("1900 bytes refused: %v", err)
	}
}

func TestDelete_MissingItemIsNil(t *testing.T) {
	fakeRun(t, out("", exitErr(t, 44)))
	if err := Delete("aimux-mcp-proxy/x"); err != nil {
		t.Fatal(err)
	}
}

func TestDelete_ArgvHasNoValue(t *testing.T) {
	calls := fakeRun(t, out("", nil))
	if err := Delete("aimux-mcp-proxy/x"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join((*calls)[0].args, " ")
	if (*calls)[0].bin != "/usr/bin/security" || got != "delete-generic-password -a aimux -s aimux-mcp-proxy/x" {
		t.Fatalf("argv = %s %s", (*calls)[0].bin, got)
	}
	fakeRun(t, out("", exitErr(t, 51)))
	if err := Delete("aimux-mcp-proxy/x"); err == nil || !strings.Contains(err.Error(), "exit status 51") {
		t.Fatalf("err = %v", err)
	}
}

// TestStore_LiveKeychain writes, reads and deletes a random item in the real
// Keychain. Opt in with AIMUX_KEYCHAIN_IT=1.
func TestStore_LiveKeychain(t *testing.T) {
	if os.Getenv("AIMUX_KEYCHAIN_IT") != "1" {
		t.Skip("set AIMUX_KEYCHAIN_IT=1 to touch the real Keychain")
	}
	b := make([]byte, 900)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	value := hex.EncodeToString(b) // 1800 bytes, printable, near the one-line limit
	svc := "aimux-mcp-proxy/it-" + hex.EncodeToString(b[:6])
	t.Cleanup(func() { _ = Delete(svc) })
	if err := Store(svc, value); err != nil {
		t.Fatal(err)
	}
	got, err := Stored(svc).Lookup(context.Background())
	if err != nil || got != value {
		t.Fatalf("read back: err=%v equal=%v", err, got == value)
	}
	if err := Delete(svc); err != nil {
		t.Fatal(err)
	}
	if ok, err := Probe(svc); ok || err != nil {
		t.Fatalf("after delete: %v %v", ok, err)
	}
}
