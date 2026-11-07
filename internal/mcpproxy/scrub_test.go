package mcpproxy

import (
	"strings"
	"testing"
)

// Servers inherit the proxy's environment, so inherited secrets are scrubbed
// too: variables whose names look secret, and URLs that carry credentials.
// Ordinary inherited values (paths, the shell) stay readable.
func TestSecretEnv_PicksInheritedSecrets(t *testing.T) {
	environ := []string{
		"DB_PASSWORD=hunter22x",           // #nosec G101 -- fake value the test expects picked
		"MY_API_KEY=k3yvalue9",            // #nosec G101 -- fake
		"REDIS_URL=redis://u:pw9secret@h", // #nosec G101 -- fake
		"HOME=/Users/someone",
		"SHELL=/bin/zsh",
		"PATH=/usr/bin:/bin",
		"MALFORMED",
	}
	got := secretEnv(environ)
	for _, k := range []string{"DB_PASSWORD", "MY_API_KEY", "REDIS_URL"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s not picked: %v", k, envKeys(got))
		}
	}
	for _, k := range []string{"HOME", "SHELL", "PATH", "MALFORMED"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s picked, want left readable", k)
		}
	}
	sc := newScrubber(got)
	if out := sc.scrub("connect failed: password hunter22x rejected"); strings.Contains(out, "hunter22x") {
		t.Errorf("inherited password survived: %q", out)
	}
	if out := sc.scrub("reading /Users/someone/cfg"); !strings.Contains(out, "/Users/someone") {
		t.Errorf("a path was redacted: %q", out)
	}
}

func envKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
