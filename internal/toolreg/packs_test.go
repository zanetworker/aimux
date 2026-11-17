package toolreg_test

import (
	"slices"
	"testing"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// BuiltinPacks and the packs clipack embeds must be the same set.
func TestBuiltinPacks_MatchClipack(t *testing.T) {
	for _, n := range toolreg.BuiltinPacks {
		if _, err := clipack.Builtin(n); err != nil {
			t.Errorf("pack %q: %v", n, err)
		}
	}
	got, want := slices.Sorted(slices.Values(clipack.Names())), slices.Sorted(slices.Values(toolreg.BuiltinPacks))
	if !slices.Equal(got, want) {
		t.Errorf("clipack.Names() = %v, toolreg.BuiltinPacks = %v", got, want)
	}
	if _, err := clipack.Builtin("nope"); err == nil {
		t.Error("clipack accepts an unknown pack")
	}
}
