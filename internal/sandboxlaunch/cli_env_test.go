package sandboxlaunch

import (
	"context"
	"io"
	"strings"
	"testing"
)

// The gateway worker's runner gives its host children (openshell) only the
// variables they need: a secret in the worker's environment never reaches
// them.
func TestMinimalEnvRunner_DropsOtherVariables(t *testing.T) {
	environ := []string{"PATH=/usr/bin:/bin", "HOME=/Users/me", "LANG=en_US.UTF-8", "TMPDIR=/tmp/x",
		"OPENSHELL_GATEWAY=local", "XDG_CONFIG_HOME=/Users/me/.config",
		"AWS_SECRET_ACCESS_KEY=sentinel-secret", "OPENSHELL_OIDC_CLIENT_SECRET=sentinel-oidc", "GITHUB_TOKEN=sentinel-gh"}
	rc, err := MinimalEnvRunner(environ)(context.Background(), nil, "/usr/bin/env")
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	got := string(out)
	if strings.Contains(got, "sentinel") {
		t.Fatalf("a secret reached the child:\n%s", got)
	}
	for _, want := range []string{"PATH=/usr/bin:/bin", "HOME=/Users/me", "LANG=en_US.UTF-8", "TMPDIR=/tmp/x", "OPENSHELL_GATEWAY=local", "XDG_CONFIG_HOME=/Users/me/.config"} {
		if !strings.Contains(got, want) {
			t.Errorf("child lacks %s:\n%s", want, got)
		}
	}
}
