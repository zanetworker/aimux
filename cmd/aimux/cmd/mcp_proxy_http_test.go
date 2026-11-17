package cmd

import (
	"runtime"
	"strings"
	"testing"
)

func TestInstallHelp_MentionsHTTPAndKeychain(t *testing.T) {
	c := newMCPProxyInstallCmd()
	for _, want := range []string{"HTTP server", "Keychain", "needs OAuth (later)"} {
		if !strings.Contains(c.Long, want) {
			t.Fatalf("install help lacks %q", want)
		}
	}
}

func TestSkipFlagHelp_NotStdioOnly(t *testing.T) {
	f := newMCPProxyInstallCmd().Flags().Lookup("skip")
	if f == nil || strings.HasPrefix(f.Usage, "Stdio") || !strings.Contains(f.Usage, "HTTP") {
		t.Fatalf("skip usage %q", f.Usage)
	}
}

func TestProxyInstaller_VaultWired(t *testing.T) {
	in := proxyInstaller(t.TempDir(), 7381, nil)
	if (runtime.GOOS == "darwin") != (in.Vault != nil) {
		t.Fatalf("GOOS %s: vault %v", runtime.GOOS, in.Vault)
	}
}
