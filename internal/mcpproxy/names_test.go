package mcpproxy

import (
	"strings"
	"testing"
)

func TestExposedName_ShortNameUnchanged(t *testing.T) {
	if got := ExposedName("slack", "post_message", 50); got != "slack__post_message" {
		t.Fatalf("got %q, want slack__post_message", got)
	}
}

func TestExposedName_ExactlyMaxUnchanged(t *testing.T) {
	tool := strings.Repeat("t", 50-len("srv__"))
	want := "srv__" + tool
	if got := ExposedName("srv", tool, 50); got != want || len(got) != 50 {
		t.Fatalf("got %q (len %d), want %q", got, len(got), want)
	}
}

func TestExposedName_LongNameShortenedDeterministically(t *testing.T) {
	a := ExposedName("notebooklm-mcp", strings.Repeat("x", 60)+"_alpha", 50)
	b := ExposedName("notebooklm-mcp", strings.Repeat("x", 60)+"_beta", 50)
	if len(a) != 50 || len(b) != 50 {
		t.Fatalf("lengths %d and %d, want 50", len(a), len(b))
	}
	if a == b {
		t.Fatalf("two different long names collapsed to %q", a)
	}
	if again := ExposedName("notebooklm-mcp", strings.Repeat("x", 60)+"_alpha", 50); again != a {
		t.Fatalf("not deterministic: %q then %q", a, again)
	}
	if !strings.HasPrefix(a, "notebooklm-mcp__") {
		t.Fatalf("lost the server prefix: %q", a)
	}
}

func TestMaxToolNameLen(t *testing.T) {
	// Claude Code exposes tools as mcp__<server>__<tool>, capped at 64 characters.
	if got := MaxToolNameLen("aimux"); got != 64-len("mcp__aimux__") {
		t.Fatalf("got %d", got)
	}
}
