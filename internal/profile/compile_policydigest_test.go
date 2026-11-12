package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/zanetworker/aimux/internal/toolgateway"
)

// The policy digest covers the compiled policy, so re-pinning a tool changes
// it while the manifest digest (what sessions and audit record) stays put.
func TestCompile_PolicyDigestFollowsPins(t *testing.T) {
	m := workReadonly(t)
	a, err := Compile(m, testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(a.PolicyYAML)
	if want := "sha256:" + hex.EncodeToString(sum[:]); a.PolicyDigest != want {
		t.Fatalf("policy digest = %q, want %q", a.PolicyDigest, want)
	}
	repinned := PinnedSets(toolgateway.Pins{
		"wiki":   {"read_page": "a"},
		"jira":   {"search": "c", "get_issue": "d"},
		"slack":  {"history": "e"},
		"google": {"get_doc": "f"},
	})
	b, err := Compile(m, repinned, testGW)
	if err != nil {
		t.Fatal(err)
	}
	if b.Digest != a.Digest {
		t.Fatalf("manifest digest moved with pins: %q vs %q", b.Digest, a.Digest)
	}
	if b.PolicyDigest == a.PolicyDigest {
		t.Fatal("policy digest unchanged after re-pinning")
	}
}
