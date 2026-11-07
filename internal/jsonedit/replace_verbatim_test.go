package jsonedit

import (
	"encoding/json"
	"testing"
)

func TestReplaceMemberVerbatim_KeepsTheValueBytesExactly(t *testing.T) {
	obj := json.RawMessage("{\n    \"a\": 1,\n    \"b\":{\"x\":  [1,2]},\n    \"c\": 3\n}")
	val := json.RawMessage(`{"command": "npx",  "args": [ "-y" ]}`)
	got, err := ReplaceMemberVerbatim(obj, "b", val)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n    \"a\": 1,\n    \"b\":{\"command\": \"npx\",  \"args\": [ \"-y\" ]},\n    \"c\": 3\n}"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReplaceMemberVerbatim_AbsentMemberRefused(t *testing.T) {
	if _, err := ReplaceMemberVerbatim(json.RawMessage(`{"a": 1}`), "b", json.RawMessage(`2`)); err == nil {
		t.Fatal("absent member replaced")
	}
}

func TestReplaceMemberVerbatim_InvalidValueRefused(t *testing.T) {
	for _, v := range []string{`{"x":`, ``} {
		if _, err := ReplaceMemberVerbatim(json.RawMessage(`{"a": 1}`), "a", json.RawMessage(v)); err == nil {
			t.Fatalf("value %q accepted", v)
		}
	}
}
