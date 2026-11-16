//go:build linux

package keyring

import (
	"context"
	"testing"
)

func TestSystem_IsSecretService(t *testing.T) {
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	lookPath = func(string) (string, error) { return "/usr/bin/secret-tool", nil }
	calls := fakeRun(t, out("k", nil))
	if _, err := System("svc").Lookup(context.Background()); err != nil || (*calls)[0].bin != "/usr/bin/secret-tool" {
		t.Fatalf("%v %+v", err, *calls)
	}
	if !Exists("svc") {
		t.Fatal("want true")
	}
}

// On Linux Probe is the Secret Service probe.
func TestProbe_Linux(t *testing.T) {
	old := lookPath
	t.Cleanup(func() { lookPath = old })
	lookPath = func(string) (string, error) { return "/usr/bin/secret-tool", nil }
	fakeRun(t, out("", exitErr(t, 1)))
	if ok, err := Probe("svc"); ok || err != nil {
		t.Fatalf("Probe = %v %v", ok, err)
	}
}
