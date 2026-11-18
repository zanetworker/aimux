package remoteapp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakeOpenshell puts an openshell on PATH that exits with code.
func fakeOpenshell(t *testing.T, code int) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nexit %d\n", code)
	if err := os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o700); err != nil { //nolint:gosec // G306: an executable test script
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// remote serve's sandbox engine checks the OpenShell gateway with the real
// openshell CLI after a failure: down when it exits non-zero.
func TestProfileWiring_SandboxGatewayCheckRunsOpenshell(t *testing.T) {
	p, closeAll, err := buildProfileWiring(t.TempDir(), noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	check := p.sandbox.GatewayCheck
	if check == nil {
		t.Fatal("remote serve does not wire the OpenShell gateway check")
	}
	fakeOpenshell(t, 1)
	if err := check(context.Background()); err == nil {
		t.Fatal("openshell status exiting 1 read as up")
	}
	fakeOpenshell(t, 0)
	if err := check(context.Background()); err != nil {
		t.Fatalf("openshell status exiting 0: %v", err)
	}
	// No openshell to run: the gateway is not known to be down, so the
	// failure keeps its own (PATH or install) reason.
	t.Setenv("PATH", t.TempDir())
	if err := check(context.Background()); err != nil {
		t.Fatalf("openshell missing read as the gateway down: %v", err)
	}
}
