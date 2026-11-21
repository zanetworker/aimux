package toolreg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/secretref"
)

// writeExe is an executable file holding content.
func writeExe(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	return p
}

func mustIdentity(t *testing.T, s Server) string {
	t.Helper()
	id, err := s.Identity(HashFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "id:") || len(id) != len("id:")+64 {
		t.Fatalf("identity %q is not id:<sha256 hex>", id)
	}
	return id
}

func TestIdentity_ChangesWithArgs(t *testing.T) {
	exe := writeExe(t, t.TempDir(), "srv", "v1")
	a := Server{Command: []string{exe, "--port", "1"}, URL: "http://127.0.0.1:1/mcp"}
	b := a
	b.Command = []string{exe, "--port", "2"}
	if mustIdentity(t, a) == mustIdentity(t, b) {
		t.Fatal("changing args must change the identity")
	}
	c := a
	c.Cwd = "/tmp"
	if mustIdentity(t, a) == mustIdentity(t, c) {
		t.Fatal("changing cwd must change the identity")
	}
	d := a
	d.URL = "http://127.0.0.1:2/mcp"
	if mustIdentity(t, a) == mustIdentity(t, d) {
		t.Fatal("changing the url must change the identity")
	}
	if again := a; mustIdentity(t, a) != mustIdentity(t, again) {
		t.Fatal("identity is not stable")
	}
}

func TestIdentity_ChangesWithBinaryContent(t *testing.T) {
	dir := t.TempDir()
	exe := writeExe(t, dir, "srv", "v1")
	s := Server{Command: []string{exe}, URL: "http://127.0.0.1:1/mcp"}
	before := mustIdentity(t, s)
	writeExe(t, dir, "srv", "v2")
	if mustIdentity(t, s) == before {
		t.Fatal("a new binary at the same path must change the identity")
	}
}

// A bare program name is found on the server's PATH, as exec does; a
// relative path is found under cwd.
func TestIdentity_ResolvesOnServerPath(t *testing.T) {
	dir := t.TempDir()
	writeExe(t, dir, "srv", "v1")
	s := Server{Command: []string{"srv"}, Env: map[string]string{"PATH": dir}}
	before := mustIdentity(t, s)
	writeExe(t, dir, "srv", "v2")
	if mustIdentity(t, s) == before {
		t.Fatal("the PATH-resolved binary must be hashed")
	}
	if _, err := (Server{Command: []string{"srv"}}).Identity(HashFile); err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("a bare name with no env PATH: err = %v, want one asking for it", err)
	}
	s.Env["PATH"] = t.TempDir()
	if _, err := s.Identity(HashFile); err == nil || !strings.Contains(err.Error(), "srv") {
		t.Fatalf("a program not on PATH: err = %v, want one naming it", err)
	}
	for _, cwd := range []string{"", "rel"} {
		if _, err := (Server{Command: []string{"./srv"}, Cwd: cwd}).Identity(HashFile); err == nil || !strings.Contains(err.Error(), "cwd") {
			t.Errorf("relative program with cwd %q: err = %v, want one asking for an absolute cwd", cwd, err)
		}
	}
	rel := Server{Command: []string{"./srv"}, Cwd: dir}
	id1 := mustIdentity(t, rel)
	writeExe(t, dir, "srv", "v3")
	if mustIdentity(t, rel) == id1 {
		t.Fatal("a relative program must be hashed under cwd")
	}
}

// A remote server has no executable: the hasher is never called.
func TestIdentity_RemoteNeedsNoHash(t *testing.T) {
	fail := func(string) (string, error) { return "", errors.New("must not hash") }
	if _, err := (Server{URL: "http://127.0.0.1:1/mcp"}).Identity(fail); err != nil {
		t.Fatal(err)
	}
	a, _ := (Server{URL: "HTTP://LocalHost:1/mcp"}).Identity(fail)
	b, _ := (Server{URL: "http://localhost:1/mcp"}).Identity(fail)
	if a != b {
		t.Fatal("scheme and host case must not change the identity")
	}
}

func TestIdentity_IgnoresSecretValues(t *testing.T) {
	a := Server{URL: "http://127.0.0.1:1/mcp", Secrets: secretref.Refs{Env: map[string]secretref.Ref{"TOKEN": {Env: &secretref.EnvRef{Name: "A"}}}}}
	b := Server{URL: "http://127.0.0.1:1/mcp", Secrets: secretref.Refs{Env: map[string]secretref.Ref{"TOKEN": {Keychain: &secretref.KeychainRef{Item: "aimux/x", Field: "TOKEN"}}}}}
	if mustIdentity(t, a) != mustIdentity(t, b) {
		t.Fatal("where a secret comes from must not change the identity")
	}
}

func TestIdentity_ChangesWithSecretNames(t *testing.T) {
	ref := secretref.Ref{Env: &secretref.EnvRef{Name: "A"}}
	a := Server{URL: "http://127.0.0.1:1/mcp", Secrets: secretref.Refs{Env: map[string]secretref.Ref{"TOKEN": ref}}}
	b := Server{URL: "http://127.0.0.1:1/mcp", Secrets: secretref.Refs{Env: map[string]secretref.Ref{"TOKEN2": ref}}}
	c := Server{URL: "http://127.0.0.1:1/mcp", Secrets: secretref.Refs{Headers: map[string]secretref.Ref{"TOKEN": ref}}}
	if mustIdentity(t, a) == mustIdentity(t, b) || mustIdentity(t, a) == mustIdentity(t, c) {
		t.Fatal("secret names (and env versus header) must change the identity")
	}
}

func TestFloating(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want bool
	}{
		{[]string{"go", "run", "."}, true},
		{[]string{"/usr/local/go/bin/go", "run", "./cmd/mcp"}, true},
		{[]string{"go", "run", "example.com/mcp@v1.2.3"}, false},
		{[]string{"go", "run", "example.com/mcp@latest"}, true},
		{[]string{"npx", "-y", "drawio-mcp-server"}, true},
		{[]string{"npx", "-y", "drawio-mcp-server@2.4.0", "--transport", "http"}, false},
		{[]string{"npx", "-y", "drawio-mcp-server@latest"}, true},
		{[]string{"npx", "@scope/pkg"}, true},
		{[]string{"npx", "@scope/pkg@1.0.0"}, false},
		{[]string{"/opt/homebrew/bin/npx", "-y", "mcp-thing"}, true},
		{[]string{"pnpm", "dlx", "mcp-thing"}, true},
		{[]string{"pnpm", "dlx", "mcp-thing@3.1.0"}, false},
		{[]string{"uvx", "workspace-mcp"}, true},
		{[]string{"uvx", "workspace-mcp==1.2.3", "--transport", "streamable-http"}, false},
		{[]string{"uvx", "workspace-mcp@1.2.3"}, false},
		{[]string{"uvx", "--from", "workspace-mcp==1.2.3", "workspace-mcp"}, false},
		{[]string{"uvx", "--from", "workspace-mcp", "workspace-mcp"}, true},
		{[]string{"/Users/me/.aimux/bin/sauron-mcp"}, false},
		{[]string{"python3", "server.py"}, false},
		{nil, false},
		// Ranges and wildcards are not fixed versions.
		{[]string{"npx", "-y", "drawio-mcp-server@^2.4.0"}, true},
		{[]string{"npx", "-y", "drawio-mcp-server@~2.4.0"}, true},
		{[]string{"npx", "-y", "drawio-mcp-server@2"}, true},
		{[]string{"npx", "-y", "drawio-mcp-server@2.4.0-beta.1"}, false},
		{[]string{"uvx", "workspace-mcp==1.*"}, true},
		{[]string{"uvx", "workspace-mcp>=1.2"}, true},
		{[]string{"uvx", "workspace-mcp==1.2,<2"}, true},
		{[]string{"go", "run", "example.com/mcp@v1"}, true},
		{[]string{"go", "run", "example.com/mcp@v0.0.0-20261101120000-abcdefabcdef"}, false},
		// Launcher options and their values are not the package.
		{[]string{"go", "run", "-tags", "integration", "example.com/mcp@v1.2.3"}, false},
		{[]string{"go", "run", "-tags=integration", "."}, true},
		{[]string{"npx", "-p", "a@1.0.0", "-p", "b", "tool"}, true},
		{[]string{"npx", "-p", "a@1.0.0", "-p", "b@2.0.0", "tool"}, false},
		{[]string{"npx", "--package=a@1.0.0", "tool"}, false},
		{[]string{"npx", "-c", "tool"}, true},
		{[]string{"pnpm", "dlx", "--package=a@1.0.0", "--package=b", "tool"}, true},
		{[]string{"pnpm", "dlx", "--shell-mode", "pkg@1.2.3"}, false},
		{[]string{"pnpm", "dlx", "-c", "pkg@1.2.3 --port 4000"}, false},
		{[]string{"pnpm", "dlx", "-c", "pkg --port 4000"}, true},
		// Round 4: attached short values, git refs, npx option values.
		{[]string{"uvx", "-wextra", "workspace-mcp==1.2.3"}, true},
		{[]string{"uvx", "-wextra==2.0", "workspace-mcp==1.2.3"}, false},
		{[]string{"npx", "github:me/tool#feature@1.2.3"}, true},
		{[]string{"npx", "git+https://example.com/tool.git#v@1.2.3"}, true},
		{[]string{"npx", "me/tool@1.2.3"}, true},
		{[]string{"npx", "--cache", "/tmp/npm-cache", "pkg@1.2.3"}, false},
		{[]string{"npx", "--registry", "https://r.example", "pkg"}, true},
		{[]string{"npx", "-c", "pkg@1.2.3"}, true},
		{[]string{"uvx", "--with", "extra", "workspace-mcp==1.2.3"}, true},
		{[]string{"uvx", "--with", "extra==2.0", "workspace-mcp==1.2.3"}, false},
		{[]string{"uvx", "--python", "3.12", "workspace-mcp==1.2.3"}, false},
		// Round 2: forms that must not slip through, and exact pins that must.
		{[]string{"go", "-C", "/src/mcp", "run", "."}, true},
		{[]string{"go", "-C=/src/mcp", "run", "example.com/mcp@v1.2.3"}, false},
		{[]string{"go", "run", "-buildvcs", "example.com/mcp@v1.2.3"}, false},
		{[]string{"go", "run", "-buildvcs=false", "."}, true},
		{[]string{"pnpm", "--package", "a", "dlx", "tool"}, true},
		{[]string{"pnpm", "--package=a@1.0.0", "dlx", "tool"}, false},
		{[]string{"uvx", "-w", "extra", "workspace-mcp==1.2.3"}, true},
		{[]string{"uvx", "-w", "extra==2.0", "workspace-mcp==1.2.3"}, false},
		{[]string{"uvx", "-w", "extra==2.0", "workspace-mcp"}, true},
		{[]string{"uvx", "--with-requirements", "req.txt", "workspace-mcp==1.2.3"}, true},
		{[]string{"uvx", "--with-editable", "./x", "workspace-mcp==1.2.3"}, true},
		{[]string{"uvx", "workspace-mcp==1.2.3+corp.1"}, false},
		{[]string{"uvx", "workspace-mcp==1!2.0"}, false},
		{[]string{"uvx", "workspace-mcp==1.2.3.post1.dev2"}, false},
	} {
		got, reason := Floating(c.argv)
		if got != c.want {
			t.Errorf("Floating(%q) = %v, want %v", c.argv, got, c.want)
		}
		if got && reason != FloatingReason {
			t.Errorf("Floating(%q) reason = %q", c.argv, reason)
		}
	}
}

// A set naming a floating server is refused; a local-only floating server
// loads.
func TestLoad_FloatingSetServerIsSetError(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n"+
		"  drawio:\n    command: [npx, -y, drawio-mcp-server]\n    url: http://127.0.0.1:4000/mcp\n"+
		"  mermaid:\n    command: [npx, -y, mcp-mermaid]\n    url: http://127.0.0.1:3033/mcp\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  drawio:readonly:\n    server: drawio\n    tools: [read]\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Sets["drawio:readonly"]; ok {
		t.Fatal("a floating server's set must be dropped")
	}
	if e := reg.SetErrors["drawio:readonly"]; e == nil || !strings.Contains(e.Error(), FloatingReason) {
		t.Fatalf("set error = %v, want %q", e, FloatingReason)
	}
	if _, ok := reg.Sources["drawio"]; ok {
		t.Fatal("a floating server must not reach Sources")
	}
}

// A set server whose executable cannot be found is held: no identity, no
// set.
func TestLoad_SetServerWithMissingBinaryIsSetError(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  jira:\n    command: [/nonexistent/jira-mcp]\n    url: http://127.0.0.1:8001/mcp\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  jira:readonly:\n    server: jira\n    tools: [search]\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if e := reg.SetErrors["jira:readonly"]; e == nil || !strings.Contains(e.Error(), "identity") {
		t.Fatalf("set error = %v, want an identity error", e)
	}
	if _, ok := reg.Sources["jira"]; ok {
		t.Fatal("a server with no identity must not reach Sources")
	}
}

// Split mode records each set server's identity; a pin matches it, or (v2)
// the server's endpoint; anything else is changed.
func TestRegistry_PinSourceIsIdentity(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n  jira:\n    url: http://127.0.0.1:8001/mcp\n    secrets:\n      headers:\n        Authorization: {env: {name: JIRA_AUTH}}\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n  jira:readonly:\n    server: jira\n    tools: [search]\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	id := reg.PinSource("jira")
	if !strings.HasPrefix(id, "id:") {
		t.Fatalf("PinSource = %q, want an identity", id)
	}
	for _, c := range []struct {
		recorded string
		changed  bool
	}{
		{id, false},
		{"http://127.0.0.1:8001/mcp", false}, // a v2 endpoint pin still matches
		{"", false},                          // v1: unknown
		{"id:" + strings.Repeat("0", 64), true},
		{"http://127.0.0.1:9/mcp", true},
	} {
		if got := reg.SourceChanged("jira", c.recorded); got != c.changed {
			t.Errorf("SourceChanged(%q) = %v, want %v", c.recorded, got, c.changed)
		}
	}
	if !reg.SourceChanged("gone", id) {
		t.Error("an identity pin on a server the registry dropped is changed")
	}
	// Legacy registries have no identity: the endpoint is recorded.
	legacy := Registry{Sources: map[string]Source{"jira": {Type: TypeMCPHTTP, URL: "http://127.0.0.1:8001/mcp"}}}
	if got := legacy.PinSource("jira"); got != "http://127.0.0.1:8001/mcp" {
		t.Fatalf("legacy PinSource = %q", got)
	}
	// The identity is part of the snapshot version and survives a clone.
	other := cloneRegistry(reg)
	if other.PinSource("jira") != id {
		t.Fatal("clone dropped the identity")
	}
	other.Identities["jira"] = "id:" + strings.Repeat("1", 64)
	if reg.PinSource("jira") != id {
		t.Fatal("clone shares the identity map")
	}
	if NewSnapshot(reg, nil, nil).Version() == NewSnapshot(other, nil, nil).Version() {
		t.Fatal("a changed identity must change the snapshot version")
	}
}

// Only a regular executable file is hashed: a FIFO would block the read
// and a data file is not a program.
func TestIdentity_RefusesNonExecutable(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.WriteFile(data, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := mkfifo(fifo); err != nil {
		t.Skip("no mkfifo:", err)
	}
	for _, p := range []string{data, fifo, dir} {
		if _, err := (Server{Command: []string{p}}).Identity(HashFile); err == nil {
			t.Errorf("%s: want an error", p)
		}
	}
}

// The zone of a link-local IPv6 address is case-sensitive.
func TestIdentity_KeepsIPv6ZoneCase(t *testing.T) {
	a := mustIdentity(t, Server{URL: "http://[fe80::1%25en0]:80/mcp"})
	b := mustIdentity(t, Server{URL: "http://[fe80::1%25EN0]:80/mcp"})
	if a == b {
		t.Fatal("zones en0 and EN0 must differ")
	}
	if mustIdentity(t, Server{URL: "http://[FE80::1%25en0]:80/mcp"}) != a {
		t.Fatal("the address part is case-insensitive")
	}
}

// Ruling (b): until aimux runs a managed server itself and can tie a
// listing to the process it started, a set naming one is held with that
// reason; a remote (url-only) server is not.
func TestLoad_ManagedSetHeld(t *testing.T) {
	dir := t.TempDir()
	exe := writeExe(t, dir, "jira-mcp", "v1")
	writeYAML(t, filepath.Join(dir, "servers.yaml"), "version: 1\nservers:\n"+
		"  jira:\n    command: ["+exe+"]\n    url: http://127.0.0.1:8001/mcp\n"+
		"  remote:\n    url: http://127.0.0.1:9000/mcp\n")
	writeYAML(t, filepath.Join(dir, "tool-sets.yaml"), "version: 1\nsets:\n"+
		"  jira:readonly:\n    server: jira\n    tools: [search]\n  remote:readonly:\n    server: remote\n    tools: [get]\n")
	reg, err := Load(filepath.Join(dir, "tool-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if e := reg.SetErrors["jira:readonly"]; e == nil || !strings.Contains(e.Error(), ManagedHeldReason) {
		t.Fatalf("set error = %v, want %q", e, ManagedHeldReason)
	}
	if _, ok := reg.Sources["jira"]; ok {
		t.Fatal("a held managed server must not reach Sources")
	}
	if _, ok := reg.Sets["remote:readonly"]; !ok || !strings.HasPrefix(reg.PinSource("remote"), "id:") {
		t.Fatalf("a remote server's set loads with an identity: %v %q", reg.SetErrors, reg.PinSource("remote"))
	}
}

// Why a set is held is part of the snapshot, so fixing one reason and
// hitting another (floating, then managed) refreshes what status says.
func TestSnapshot_SetErrorReasonChangesVersion(t *testing.T) {
	a := Registry{Version: 1, SetErrors: map[string]error{"drawio:readonly": errors.New(FloatingReason)}}
	b := Registry{Version: 1, SetErrors: map[string]error{"drawio:readonly": errors.New(ManagedHeldReason)}}
	if NewSnapshot(a, nil, nil).Version() == NewSnapshot(b, nil, nil).Version() {
		t.Fatal("a changed set error must change the version")
	}
}

// A relative program is resolved by the OS, not by cleaning the path: a
// symlinked directory followed by .. leads where exec would go.
func TestIdentity_RelativeProgramThroughSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real", "sub")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExe(t, filepath.Join(root, "real"), "srv", "the one exec runs")
	cwd := filepath.Join(root, "cwd")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(cwd, "link")); err != nil {
		t.Fatal(err)
	}
	s := Server{Command: []string{"link/../srv"}, Cwd: cwd} // exec: cwd/link -> real/sub, .. -> real, srv
	var hashed string
	if _, err := s.Identity(func(p string) (string, error) { hashed = p; return HashFile(p) }); err != nil {
		t.Fatalf("a valid relative program: %v", err)
	}
	if want := cwd + "/link/../srv"; hashed != want {
		t.Fatalf("hashed %q, want %q (not cleaned)", hashed, want)
	}
}

// A PATH entry this user cannot execute is skipped, as exec does.
func TestIdentity_SkipsPathEntryNotExecutableByUs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can execute anything")
	}
	first, second := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(first, "srv"), []byte("not ours to run"), 0o644|0o010); err != nil { //nolint:gosec // group-exec only: not runnable by its owner
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(first, "srv"), 0o654); err != nil { //nolint:gosec // group/other exec, owner not
		t.Fatal(err)
	}
	writeExe(t, second, "srv", "runnable")
	s := Server{Command: []string{"srv"}, Env: map[string]string{"PATH": first + ":" + second}}
	var hashed string
	if _, err := s.Identity(func(p string) (string, error) { hashed = p; return HashFile(p) }); err != nil {
		t.Fatal(err)
	}
	if hashed != filepath.Join(second, "srv") {
		t.Fatalf("hashed %s, want the one this user can run", hashed)
	}
}
