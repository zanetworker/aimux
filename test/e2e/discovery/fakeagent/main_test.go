package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResumeID(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--resume", "abc"}, "abc"},
		{[]string{"--model", "opus"}, ""},
		{[]string{"--resume"}, ""}, // flag without a value
		{nil, ""},
	}
	for _, c := range cases {
		if got := resumeID(c.args); got != c.want {
			t.Errorf("resumeID(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestRender_DrawsPromptToolCallDiffAndResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	jsonl := `{"type":"user","message":{"content":"Add idempotency keys"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"charges.go","old_string":"func A() {","new_string":"func A() {\n\tkey := 1"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"Applied edit\nsecond line"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}
not json
`
	if err := os.WriteFile(path, []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}
	got := render(path)
	for _, want := range []string{"Add idempotency keys", "Edit", "(charges.go)", "- func A() {", "+ \tkey := 1", "⎿  Applied edit", "⏺ Done."} {
		if !strings.Contains(got, want) {
			t.Errorf("render output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "second line") {
		t.Errorf("tool result should show only its first line:\n%s", got)
	}
}

func TestRender_MissingFile_ReturnsEmpty(t *testing.T) {
	if got := render(filepath.Join(t.TempDir(), "nope.jsonl")); got != "" {
		t.Errorf("render(missing) = %q, want empty", got)
	}
}
