package jsonedit

import (
	"encoding/json"
	"strings"
	"testing"
)

// In an inline object written with ", " and ": ", the new value gets spaces
// after its structural commas only: commas inside strings (a permission rule
// such as "Bash(echo a,b)") keep their exact bytes.
func TestSetMember_InlineSpacingLeavesStringsAlone(t *testing.T) {
	obj := json.RawMessage(`{"deny": ["x"], "allow": ["old"]}`)
	val := json.RawMessage(`["Bash(echo a,b)","q\",r","plain"]`)
	out, err := SetMember(obj, "allow", val)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, keep := range []string{`"Bash(echo a,b)"`, `"q\",r"`} {
		if !strings.Contains(s, keep) {
			t.Fatalf("string bytes changed: %s (want %s intact)", s, keep)
		}
	}
	if !strings.Contains(s, `"Bash(echo a,b)", "q\",r", "plain"`) {
		t.Fatalf("structural commas not spaced like the object: %s", s)
	}
	var v map[string][]string
	if err := json.Unmarshal(out, &v); err != nil || v["allow"][0] != "Bash(echo a,b)" || v["allow"][1] != `q",r` {
		t.Fatalf("decoded wrong: %v %v", v, err)
	}
}
