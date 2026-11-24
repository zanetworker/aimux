package modelproxy

import (
	"path/filepath"
	"testing"
)

func TestUsageFileKeepsDevOffStableHUD(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		port int
		file string
	}{
		{7385, "codex-usage.json"},
		{7386, ""},
		{0, ""},
	} {
		want := ""
		if tc.file != "" {
			want = filepath.Join(home, ".aimux", tc.file)
		}
		if got := UsageFile(home, tc.port); got != want {
			t.Errorf("port %d: usage file = %q, want %q", tc.port, got, want)
		}
	}
}
