package harnessconfig

import (
	"errors"
	"testing"
)

func TestFor_CodexNotSupported(t *testing.T) {
	_, err := For("codex")
	if !errors.Is(err, ErrHarnessNotSupported) {
		t.Fatalf("err = %v, want ErrHarnessNotSupported", err)
	}
	if err.Error() != "harness not supported yet" {
		t.Fatalf("message = %q", err.Error())
	}
	a, err := For("claude-code")
	if err != nil || a.Name() != "claude-code" {
		t.Fatalf("claude-code: %v %v", a, err)
	}
}
