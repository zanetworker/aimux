package main

import "testing"

func TestVersionVar(t *testing.T) {
	// version is set via ldflags at build time; default is "dev"
	if version == "" {
		t.Fatal("version should not be empty")
	}
	if version != "dev" {
		t.Logf("version set to %s (via ldflags)", version)
	}
}
