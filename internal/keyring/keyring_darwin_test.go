//go:build darwin

package keyring

import (
	"context"
	"testing"
)

func TestExists_Darwin(t *testing.T) {
	calls := fakeRun(t, out("attributes", nil))
	if !Exists("svc") {
		t.Fatal("want true")
	}
	for _, a := range (*calls)[0].args {
		if a == "-w" {
			t.Fatalf("Exists must not ask for the secret: %v", (*calls)[0].args)
		}
	}
	fakeRun(t, out("", exitErr(t, 44)))
	if Exists("svc") {
		t.Fatal("want false")
	}
}

func TestSystem_IsKeychain(t *testing.T) {
	calls := fakeRun(t, out("k", nil))
	if _, err := System("svc").Lookup(context.Background()); err != nil || (*calls)[0].bin != "/usr/bin/security" {
		t.Fatalf("%v %+v", err, *calls)
	}
}
