package profile

import (
	"reflect"
	"testing"
)

// BuiltinNames lists every embedded builtin, sorted, each resolvable.
func TestBuiltinNames(t *testing.T) {
	got := BuiltinNames()
	want := []string{"base", "coding", "page", "research", "research-with-work", "work-readonly"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuiltinNames() = %v, want %v", got, want)
	}
	for _, n := range got {
		if _, err := Resolve("builtin:"+n, nil); err != nil {
			t.Errorf("builtin %s does not resolve: %v", n, err)
		}
	}
}
