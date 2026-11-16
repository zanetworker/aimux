package clipack

import "testing"

// A host a credentialed builtin pack reaches is bound to that pack's
// provider: CredentialedHost names the pack, case-insensitively.
func TestCredentialedHost(t *testing.T) {
	for host, want := range map[string]string{
		"api.github.com": "gh",
		"API.GitHub.com": "gh",
		"github.com":     "",
		"example.com":    "",
		"":               "",
	} {
		got, ok := CredentialedHost(host)
		if got != want || ok != (want != "") {
			t.Errorf("CredentialedHost(%q) = %q, %v; want %q", host, got, ok, want)
		}
	}
}
