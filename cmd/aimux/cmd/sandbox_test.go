package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// statusMachine is what sandbox status sees: which of image, provider and
// gateway are there, and which sandboxes exist.
type statusMachine struct {
	image     error // podman image exists: nil present, exit 1 missing, other unknown
	gatewayUp bool
	providers string // provider list output
	sandboxes []string
	listErr   error // sandbox list fails while status works
}

func (m statusMachine) answer(argv []string) (string, error) {
	s := strings.Join(argv, " ")
	switch {
	case strings.HasPrefix(s, "podman image exists "+wingmanImage):
		return "", m.image
	case s == "openshell -g local status":
		if !m.gatewayUp {
			return "", exitCodeErr{1, "Error: connection refused"}
		}
		return "Server Status\n\n  Gateway: local\n  Status: Connected\n", nil
	case s == "openshell -g local provider list":
		return m.providers, nil
	case s == "openshell -g local sandbox list":
		if m.listErr != nil {
			return "", m.listErr
		}
		out := "NAME  CREATED  PHASE\n"
		for _, n := range m.sandboxes {
			out += "\x1b[1m" + n + "\x1b[0m  2026-11-11 20:10:09  Ready\n"
		}
		return out, nil
	}
	return "", errors.New("unexpected command " + s)
}

const providersWithWingman = "NAME            TYPE               CREDENTIAL_KEYS   CONFIG_KEYS\nwingman-claude  claude-code-oauth  1  0\n"

// statusRig is a home whose work-readonly compiles (pins and registry as in
// verify), an empty profile dir, and a fake machine.
func statusRig(t *testing.T, m statusMachine) (browserEnv, *fakeRun) {
	t.Helper()
	t.Setenv("AIMUX_PROFILE_DIR", t.TempDir())
	env := verifyEnv(t, "", nil).env
	f := &fakeRun{answer: m.answer}
	env.Run = f.run
	env.Health = func(context.Context) error {
		t.Error("status must not start the gateway (Health)")
		return nil
	}
	return env, f
}

// compiledDigest is the digest the profile compiles to from home right now.
func compiledDigest(t *testing.T, home, name string) string {
	t.Helper()
	m, err := profile.Resolve(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := toolgateway.NewStore(home).Reload()
	if err != nil {
		t.Fatal(err)
	}
	c, err := profile.CompileAt(m, snap, profile.GatewayAddr{})
	if err != nil {
		t.Fatal(err)
	}
	return c.PolicyDigest
}

func writeDigest(t *testing.T, home, sandbox, digest string) {
	t.Helper()
	dir := filepath.Join(home, ".aimux", "sandboxes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sandbox+".digest"), []byte(digest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runSandboxStatus(t *testing.T, env browserEnv, asJSON bool) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newSandboxCmdWith(env)
	args := []string{"status"}
	if asJSON {
		args = append(args, "--json")
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "")
	t.Cleanup(func() { jsonOutput = false })
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sandbox status: %v (exit 0 always)\n%s", err, errOut.String())
	}
	return out.String(), errOut.String()
}

type statusDoc struct {
	SchemaVersion string `json:"schema_version"`
	Image         struct {
		Name, Status, Error string
	} `json:"image"`
	Provider struct {
		Name, Status, Error string
	} `json:"provider"`
	Gateway struct {
		Name, Status, Error string
	} `json:"gateway"`
	Profiles []struct {
		Profile        string `json:"profile"`
		Sandbox        string `json:"sandbox"`
		Exists         *bool  `json:"exists"`
		State          string `json:"state"`
		RecordedDigest string `json:"recorded_digest"`
		CompiledDigest string `json:"compiled_digest"`
		Error          string `json:"error"`
	} `json:"profiles"`
	Next []string `json:"next"`
}

func decodeStatus(t *testing.T, s string) statusDoc {
	t.Helper()
	var d statusDoc
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, s)
	}
	return d
}

// All present: work-readonly current, research stale (an old digest).
func TestSandboxStatus_AllPresent(t *testing.T) {
	env, f := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman,
		sandboxes: []string{"aimux-work-readonly", "aimux-research", "wingman-work"}})
	wr := compiledDigest(t, env.Home, "work-readonly")
	writeDigest(t, env.Home, "aimux-work-readonly", wr)
	writeDigest(t, env.Home, "aimux-research", "sha256:old")

	out, errOut := runSandboxStatus(t, env, false)
	for _, want := range []string{
		"Image:     " + wingmanImage + "  present",
		"Provider:  wingman-claude  present",
		"Gateway:   local  reachable",
		"research",
		"aimux-research",
		"stale (applies on next turn)",
		"work-readonly",
		"current",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "page") {
		t.Errorf("page runs over acp, it has no sandbox:\n%s", out)
	}
	if strings.Contains(errOut, "Next:") {
		t.Errorf("nothing needs action, got a Next hint: %s", errOut)
	}
	assertReadOnly(t, f)

	d := decodeStatus(t, mustJSON(t, env))
	if d.SchemaVersion != "1" || d.Image.Status != "present" || d.Image.Name != wingmanImage ||
		d.Provider.Status != "present" || d.Provider.Name != "wingman-claude" || d.Gateway.Status != "reachable" || d.Gateway.Name != "local" {
		t.Fatalf("components = %+v", d)
	}
	if len(d.Profiles) != 2 || d.Profiles[0].Profile != "research" || d.Profiles[1].Profile != "work-readonly" {
		t.Fatalf("profiles = %+v, want research and work-readonly", d.Profiles)
	}
	r, w := d.Profiles[0], d.Profiles[1]
	if r.State != "stale" || r.RecordedDigest != "sha256:old" || r.CompiledDigest == "" || r.Exists == nil || !*r.Exists {
		t.Errorf("research = %+v", r)
	}
	if w.State != "current" || w.RecordedDigest != wr || w.CompiledDigest != wr || w.Sandbox != "aimux-work-readonly" {
		t.Errorf("work-readonly = %+v", w)
	}
	if d.Next == nil || len(d.Next) != 0 {
		t.Errorf("next = %#v, want []", d.Next)
	}
}

func mustJSON(t *testing.T, env browserEnv) string {
	t.Helper()
	out, _ := runSandboxStatus(t, env, true)
	return out
}

// A sandbox that exists without a recorded digest gets its policy set on
// the next turn: stale. One that does not exist is missing.
func TestSandboxStatus_MissingAndNoDigest(t *testing.T) {
	env, _ := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman, sandboxes: []string{"aimux-work-readonly"}})
	writeDigest(t, env.Home, "aimux-research", "sha256:left-over") // the sandbox was deleted
	out, _ := runSandboxStatus(t, env, false)
	if !strings.Contains(out, "missing (created on first use)") || !strings.Contains(out, "stale (applies on next turn)") {
		t.Fatalf("out:\n%s", out)
	}
	d := decodeStatus(t, mustJSON(t, env))
	r, w := d.Profiles[0], d.Profiles[1]
	if r.State != "missing" || r.Exists == nil || *r.Exists {
		t.Errorf("research = %+v", r)
	}
	if w.State != "stale" || w.RecordedDigest != "" {
		t.Errorf("work-readonly = %+v", w)
	}
}

// Nothing set up: image and provider missing, Next names sandbox setup.
func TestSandboxStatus_ImageAndProviderMissing(t *testing.T) {
	env, _ := statusRig(t, statusMachine{image: exitCodeErr{1, ""}, gatewayUp: true,
		providers: "NAME    TYPE  CREDENTIAL_KEYS\nvertex  google-vertex-ai  1\n"})
	out, errOut := runSandboxStatus(t, env, false)
	if !strings.Contains(out, wingmanImage+"  missing") || !strings.Contains(out, "wingman-claude  missing") {
		t.Fatalf("out:\n%s", out)
	}
	if strings.Count(errOut, "Next: aimux sandbox setup") != 1 {
		t.Fatalf("errOut = %q, want one Next: aimux sandbox setup", errOut)
	}
	d := decodeStatus(t, mustJSON(t, env))
	if d.Image.Status != "missing" || d.Provider.Status != "missing" || len(d.Next) != 1 || d.Next[0] != "aimux sandbox setup" {
		t.Fatalf("doc = %+v", d)
	}
}

// podman itself failing (machine stopped) is unknown, with the reason.
func TestSandboxStatus_PodmanFails(t *testing.T) {
	env, _ := statusRig(t, statusMachine{image: exitCodeErr{125, "Cannot connect to Podman"}, gatewayUp: true, providers: providersWithWingman})
	d := decodeStatus(t, mustJSON(t, env))
	if d.Image.Status != "unknown" || !strings.Contains(d.Image.Error, "Cannot connect to Podman") {
		t.Fatalf("image = %+v", d.Image)
	}
	if len(d.Next) != 1 || !strings.Contains(d.Next[0], "podman machine start") {
		t.Fatalf("next = %v", d.Next)
	}
}

// Gateway down: provider and sandboxes are unknown, nothing else is asked
// of openshell, the gateway is not started, and Next says how to start it.
func TestSandboxStatus_GatewayDown(t *testing.T) {
	env, f := statusRig(t, statusMachine{gatewayUp: false})
	out, errOut := runSandboxStatus(t, env, false)
	if !strings.Contains(out, "Gateway:   local  unreachable") || !strings.Contains(out, "wingman-claude  unknown") || !strings.Contains(out, "unknown (gateway unreachable)") {
		t.Fatalf("out:\n%s", out)
	}
	if !strings.Contains(errOut, "Next: start the OpenShell gateway") {
		t.Fatalf("errOut = %q", errOut)
	}
	for _, c := range f.calls {
		if c.argv[0] == "openshell" && strings.Join(c.argv, " ") != "openshell -g local status" {
			t.Fatalf("called %q with the gateway down", c.argv)
		}
	}
	d := decodeStatus(t, mustJSON(t, env))
	if d.Gateway.Status != "unreachable" || d.Gateway.Error == "" || d.Provider.Status != "unknown" {
		t.Fatalf("doc = %+v", d)
	}
	for _, p := range d.Profiles {
		if p.State != "unknown" || p.Exists != nil {
			t.Errorf("%s = %+v, want unknown with exists null", p.Profile, p)
		}
	}
}

// A profile that does not compile (no pins) is an error with the reason
// and a Next hint to review tools; the others still report.
func TestSandboxStatus_CompileError(t *testing.T) {
	env, _ := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman, sandboxes: []string{"aimux-work-readonly"}})
	if err := os.Remove(toolgateway.DefaultPinsPath(env.Home)); err != nil {
		t.Fatal(err)
	}
	out, errOut := runSandboxStatus(t, env, false)
	if !strings.Contains(out, "error: ") || !strings.Contains(out, "aimux tools approve") {
		t.Fatalf("out:\n%s", out)
	}
	if !strings.Contains(errOut, "Next: aimux tools status") {
		t.Fatalf("errOut = %q", errOut)
	}
	d := decodeStatus(t, mustJSON(t, env))
	r, w := d.Profiles[0], d.Profiles[1]
	if r.State != "missing" || w.State != "error" || w.Error == "" || w.CompiledDigest != "" || w.Exists == nil || !*w.Exists {
		t.Fatalf("profiles = %+v", d.Profiles)
	}
}

// A user profile that runs in a sandbox is listed too.
func TestSandboxStatus_UserProfile(t *testing.T) {
	env, _ := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman})
	dir := os.Getenv("AIMUX_PROFILE_DIR")
	if err := os.WriteFile(filepath.Join(dir, "deep.yaml"), []byte("name: deep\nextends: research\n"), 0o600); err != nil { //nolint:gosec // G703: dir is the test temp profile dir
		t.Fatal(err)
	}
	d := decodeStatus(t, mustJSON(t, env))
	var names []string
	for _, p := range d.Profiles {
		names = append(names, p.Profile)
	}
	if strings.Join(names, ",") != "deep,research,work-readonly" {
		t.Fatalf("profiles = %v", names)
	}
}

// Status writes nothing: no file under home changes and only read-only
// commands run.
func assertReadOnly(t *testing.T, f *fakeRun) {
	t.Helper()
	allowed := []string{"podman image exists", "openshell -g local status", "openshell -g local provider list", "openshell -g local sandbox list"}
	for _, c := range f.calls {
		s := strings.Join(c.argv, " ")
		ok := false
		for _, a := range allowed {
			if s == a || strings.HasPrefix(s, a+" ") {
				ok = true
			}
		}
		if !ok {
			t.Errorf("status ran %q", s)
		}
	}
}

func TestSandboxStatus_WritesNothing(t *testing.T) {
	env, _ := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman, sandboxes: []string{"aimux-research"}})
	before := snapshotTree(t, env.Home)
	runSandboxStatus(t, env, false)
	runSandboxStatus(t, env, true)
	if after := snapshotTree(t, env.Home); after != before {
		t.Fatalf("status changed home:\nbefore %s\nafter  %s", before, after)
	}
}

func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(p + " " + fi.ModTime().String() + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// setupMachine answers sandbox setup: image and provider present or not.
func setupMachine(imagePresent, providerPresent bool, token string) func(argv []string) (string, error) {
	return func(argv []string) (string, error) {
		s := strings.Join(argv, " ")
		switch {
		case strings.HasPrefix(s, "podman image exists"):
			if imagePresent {
				return "", nil
			}
			return "", exitCodeErr{1, ""}
		case strings.HasPrefix(s, "openshell -g local provider list-profiles"):
			if providerPresent {
				return "NAME  TYPE  CATEGORY\nclaude-code-oauth  provider  AGENT\n", nil
			}
			return "NAME  TYPE  CATEGORY\n", nil
		case strings.HasPrefix(s, "openshell -g local provider list"):
			if providerPresent {
				return providersWithWingman, nil
			}
			return "NAME  TYPE  CREDENTIAL_KEYS\n", nil
		case strings.HasPrefix(s, "security find-generic-password"):
			return token + "\n", nil
		}
		return "", nil
	}
}

func runSandboxCmd(t *testing.T, env browserEnv, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newSandboxCmdWith(env)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// A fresh machine: setup builds the image and creates the provider, says
// so, and never touches a sandbox.
func TestSandboxSetup_Fresh(t *testing.T) {
	const token = "sk-ant-oat01-SETUPSECRET" //nolint:gosec // G101: fake token
	f := &fakeRun{answer: setupMachine(false, false, token)}
	env := testEnv(t, f)
	dir := filepath.Join(t.TempDir(), "sandbox")
	out, _, err := runSandboxCmd(t, env, "setup", "--sandbox-dir", dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"Building " + wingmanImage + " from " + dir, "Importing provider profile claude-code-oauth", "Creating provider wingman-claude", "aimux remote serve creates"} {
		if !strings.Contains(out, want) {
			t.Errorf("out lacks %q:\n%s", want, out)
		}
	}
	if len(f.find("podman", "build")) != 1 || len(f.find("openshell", "-g", "local", "provider", "create")) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
	if len(f.find("openshell", "-g", "local", "sandbox")) != 0 {
		t.Fatal("setup touched a sandbox")
	}
	if strings.Contains(out, token) {
		t.Fatal("token printed")
	}
	if _, err := os.Stat(filepath.Join(env.Home, ".config", "aimux")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sandbox setup wrote the tool or client config: %v", err)
	}
}

// Everything present: a no-op that says so.
func TestSandboxSetup_Idempotent(t *testing.T) {
	f := &fakeRun{answer: setupMachine(true, true, "")}
	env := testEnv(t, f)
	out, _, err := runSandboxCmd(t, env, "setup")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing to do: "+wingmanImage+" and provider wingman-claude are present") {
		t.Fatalf("out:\n%s", out)
	}
	if len(f.find("podman", "build")) != 0 || len(f.find("openshell", "-g", "local", "provider", "create")) != 0 ||
		len(f.find("openshell", "-g", "local", "provider", "profile")) != 0 || len(f.find("security")) != 0 {
		t.Fatalf("setup redid steps: %v", f.calls)
	}
}

// Without the Keychain token, the instructions name sandbox setup to rerun.
func TestSandboxSetup_MissingTokenNamesRerun(t *testing.T) {
	base := setupMachine(true, false, "")
	f := &fakeRun{answer: func(argv []string) (string, error) {
		if argv[0] == "security" {
			return "", exitCodeErr{44, "not found"}
		}
		return base(argv)
	}}
	out, _, err := runSandboxCmd(t, testEnv(t, f), "setup")
	if err == nil || !strings.Contains(out, "then run `aimux sandbox setup` again") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// sandbox verify is today's browser verify.
func TestSandboxVerifyCommand(t *testing.T) {
	r := verifyEnv(t, "internet 000\nauth 200\nnoauth 401\nhostport 403\nhostgw 000\n", nil)
	out, _, err := runSandboxCmd(t, r.env, "verify")
	if err != nil || strings.Count(out, "PASS") != 5 {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// browser verify delegates to sandbox verify and says it moved.
func TestBrowserVerifyDelegates(t *testing.T) {
	r := verifyEnv(t, "internet 000\nauth 200\nnoauth 401\nhostport 403\nhostgw 000\n", nil)
	var out, errOut bytes.Buffer
	cmd := newBrowserCmdWith(r.env)
	cmd.SetArgs([]string{"verify"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if strings.Count(out.String(), "PASS") != 5 || len(r.ensured) != 1 {
		t.Fatalf("out = %s", out.String())
	}
	if !strings.Contains(errOut.String(), "aimux browser verify is now aimux sandbox verify") {
		t.Fatalf("errOut = %q", errOut.String())
	}
}

// browser setup still prepares the sandbox parts and points at aimux sandbox.
func TestBrowserSetupPointsAtSandbox(t *testing.T) {
	f := &fakeRun{answer: setupMachine(true, true, "")}
	env := testEnv(t, f)
	var out bytes.Buffer
	if err := browserSetup(context.Background(), env, t.TempDir(), false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Sandbox image, provider and policies: aimux sandbox status") {
		t.Fatalf("out:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Nothing to do") {
		t.Fatalf("browser setup keeps its output; the no-op line is sandbox setup's:\n%s", out.String())
	}
}

// browser status: native host per Chrome, the pinned id and host.sh.
func TestBrowserStatus(t *testing.T) {
	f := &fakeRun{}
	env := testEnv(t, f)
	var out, errOut bytes.Buffer
	cmd := newBrowserCmdWith(env)
	cmd.SetArgs([]string{"status"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Chrome:", "Chrome Beta:", "not registered", "Extension id:", "not pinned", "Host wrapper:", "missing"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("fresh status lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(errOut.String(), "Next: aimux browser install --extension-id <id>") {
		t.Fatalf("errOut = %q", errOut.String())
	}

	chrome := filepath.Join(env.Home, "Library", "Application Support", "Google", "Chrome")
	if err := browserInstall(context.Background(), env, testExtID, []string{chrome}, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	cmd = newBrowserCmdWith(env)
	cmd.SetArgs([]string{"status"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var d struct {
		SchemaVersion string `json:"schema_version"`
		Hosts         []struct {
			Browser    string `json:"browser"`
			Manifest   string `json:"manifest"`
			Registered bool   `json:"registered"`
		} `json:"hosts"`
		ExtensionID string `json:"extension_id"`
		Wrapper     struct {
			Path    string `json:"path"`
			Present bool   `json:"present"`
		} `json:"wrapper"`
	}
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if d.SchemaVersion != "1" || len(d.Hosts) != 2 || !d.Hosts[0].Registered || d.Hosts[0].Browser != "Chrome" || d.Hosts[1].Registered ||
		d.ExtensionID != testExtID || !d.Wrapper.Present {
		t.Fatalf("doc = %+v", d)
	}
	if strings.Contains(errOut.String(), "Next:") {
		t.Fatalf("installed, got a Next hint: %q", errOut.String())
	}
}

// The gateway answers status but sandbox list fails: the gateway is
// reachable with the failure named, each profile is unknown because of the
// list, and Next says so.
func TestSandboxStatus_SandboxListFails(t *testing.T) {
	env, _ := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman, listErr: exitCodeErr{1, "Error: list timed out"}})
	out, errOut := runSandboxStatus(t, env, false)
	if !strings.Contains(out, "Gateway:   local  reachable, sandbox list failed: Error: list timed out") ||
		!strings.Contains(out, "unknown (sandbox list failed)") || strings.Contains(out, "gateway unreachable") {
		t.Fatalf("out:\n%s", out)
	}
	if !strings.Contains(errOut, "Next: openshell -g local sandbox list") {
		t.Fatalf("errOut = %q", errOut)
	}
	d := decodeStatus(t, mustJSON(t, env))
	if d.Gateway.Status != "reachable" || !strings.Contains(d.Gateway.Error, "sandbox list failed: Error: list timed out") {
		t.Fatalf("gateway = %+v", d.Gateway)
	}
	for _, p := range d.Profiles {
		if p.State != "unknown" || p.Exists != nil || p.Error != "sandbox list failed" {
			t.Errorf("%s = %+v", p.Profile, p)
		}
	}
}

// The sandbox name is the compiled one, not rebuilt from the profile name.
func TestSandboxStatus_UsesCompiledSandboxName(t *testing.T) {
	env, _ := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman})
	r := collectSandboxStatus(context.Background(), env, os.Getenv("AIMUX_PROFILE_DIR"))
	for _, p := range r.Profiles {
		m, _ := profile.Resolve(p.Profile, nil)
		snap, _, _ := toolgateway.NewStore(env.Home).Reload()
		c, err := profile.CompileAt(m, snap, profile.GatewayAddr{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Sandbox != c.Sandbox {
			t.Errorf("%s sandbox = %q, compiled %q", p.Profile, p.Sandbox, c.Sandbox)
		}
	}
}
