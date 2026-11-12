package sandboxlaunch

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func statusRunner(version string) *fakeRunner {
	return &fakeRunner{respond: func(argv []string) (string, error) {
		if argv[3] == "status" {
			return "Server Status\n\n  Gateway: work\n  Server: http://127.0.0.1:8090\n  Status: Connected\n  Version: " + version + "\n", nil
		}
		return listHeader, nil
	}}
}

func TestConnect_RefusesUnknownMajor(t *testing.T) {
	g := Gateway{Name: "work", Endpoint: "http://127.0.0.1:8090", AuthMode: "plaintext"}
	_, err := connect(context.Background(), g, statusRunner("1.0.0").run, t.TempDir())
	if err == nil || err.Error() != "unsupported OpenShell gateway version 1.0.0" {
		t.Fatalf("err = %v, want unsupported OpenShell gateway version 1.0.0", err)
	}

	f := statusRunner("0.1.7")
	home := t.TempDir()
	b, err := connect(context.Background(), g, f.run, home)
	if err != nil {
		t.Fatalf("0.1.7: %v", err)
	}
	if !slices.Equal(f.calls[0], []string{"openshell", "-g", "work", "status"}) {
		t.Fatalf("status argv = %q", f.calls[0])
	}
	_ = b.Download(context.Background(), "aimux-x", "/a", filepath.Join(home, "x"))
	if last := f.calls[len(f.calls)-1]; last[2] != "work" {
		t.Fatalf("backend gateway = %q, want work", last[2])
	}
	if cb := b.(*cliBackend); cb.dir != filepath.Join(home, ".aimux", "sandboxes") {
		t.Fatalf("policy dir = %s", cb.dir)
	}
}

func TestConnect_UnreachableOrNoVersion(t *testing.T) {
	g := Gateway{Name: "work", Endpoint: "http://127.0.0.1:1", AuthMode: "plaintext"}
	down := &fakeRunner{respond: func([]string) (string, error) { return "", errExit }}
	if _, err := connect(context.Background(), g, down.run, t.TempDir()); err == nil || !strings.Contains(err.Error(), "work") {
		t.Fatalf("err = %v, want the gateway named", err)
	}
	if _, err := connect(context.Background(), g, statusRunner("").run, t.TempDir()); err == nil {
		t.Fatal("a status without a version was accepted")
	}
	if _, err := connect(context.Background(), Gateway{Name: "-x"}, statusRunner("0.1.7").run, t.TempDir()); err == nil {
		t.Fatal("a flag-like gateway name was accepted")
	}
}
