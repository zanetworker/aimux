package cmd

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/profile"
)

// Status checks the tool runner image like the sandbox image, and names
// each source left out of the runner with why (here gh, whose provider
// aimux-github the gateway lacks).
func TestSandboxStatus_RunnerImageAndDroppedSources(t *testing.T) {
	env, f := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman,
		runnerImg: exitCodeErr{1, ""}, sandboxes: []string{"aimux-work-readonly", "aimux-tools"}})
	ghRunnerHome(t, env)
	out, _ := runSandboxStatus(t, env, false)
	for _, want := range []string{"Image:     localhost/aimux-tools", "missing", "Left out of the tool runner:", "github: "} {
		if !strings.Contains(out, want) {
			t.Fatalf("out lacks %q:\n%s", want, out)
		}
	}
	assertReadOnly(t, f)
	d := decodeStatus(t, mustJSON(t, env))
	if d.RunnerImage.Status != "missing" || !slices.Contains(d.Next, "aimux sandbox setup") {
		t.Fatalf("runner image %+v, next %q", d.RunnerImage, d.Next)
	}
	if len(d.RunnerDropped) != 1 || d.RunnerDropped[0].Source != "github" || !strings.Contains(d.RunnerDropped[0].Reason, runnerProvider) {
		t.Fatalf("dropped %+v, want github for its missing provider", d.RunnerDropped)
	}
	if !d.RunnerProvider.Needed {
		t.Fatalf("runner provider %+v: an approved source needs it", d.RunnerProvider)
	}

	// Present image, no gh source: nothing dropped, provider not needed.
	env, _ = statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman, sandboxes: []string{"aimux-work-readonly", "aimux-tools"}})
	d = decodeStatus(t, mustJSON(t, env))
	if d.RunnerImage.Status != "present" || len(d.RunnerDropped) != 0 || d.RunnerProvider.Needed {
		t.Fatalf("image %+v dropped %+v provider %+v", d.RunnerImage, d.RunnerDropped, d.RunnerProvider)
	}
}

func findCheck(cs []doctorCheck, id string) (doctorCheck, bool) {
	return checkByID(doctorDoc{Checks: cs}, id)
}

// Doctor fails a missing runner image and a missing runner provider an
// approved source needs, is fine without the provider when nothing needs
// it, and warns about each source left out of the runner.
func TestDoctor_RunnerImageProviderAndDropped(t *testing.T) {
	base := sandboxReport{Image: component{Name: wingmanImage, Status: "present"}, Gateway: component{Status: "reachable"},
		Provider: component{Name: wingmanProvider, Status: "present"}}
	r := base
	r.RunnerImage = component{Name: "localhost/aimux-tools:x", Status: "missing"}
	r.RunnerProvider = component{Name: runnerProvider, Status: "missing", Needed: true}
	r.RunnerDropped = []droppedSource{{Source: "github", Reason: "provider aimux-github is missing"}}
	cs := sandboxChecks(r, true)
	if c, _ := findCheck(cs, "sandbox.runner_image"); c.Status != checkFail || !strings.Contains(c.Fix, "sandbox setup") {
		t.Fatalf("runner image check %+v", c)
	}
	if c, _ := findCheck(cs, "sandbox.runner_provider"); c.Status != checkFail || !strings.Contains(c.Detail, "approved source needs it") {
		t.Fatalf("runner provider check %+v", c)
	}
	if c, _ := findCheck(cs, "sandbox.runner_sources"); c.Status != checkWarn || !strings.Contains(c.Detail, "github") {
		t.Fatalf("runner sources check %+v", c)
	}

	r = base
	r.RunnerImage = component{Name: "localhost/aimux-tools:x", Status: "present"}
	r.RunnerProvider = component{Name: runnerProvider, Status: "missing"}
	cs = sandboxChecks(r, true)
	if c, _ := findCheck(cs, "sandbox.runner_image"); c.Status != checkOK {
		t.Fatalf("runner image check %+v", c)
	}
	if c, _ := findCheck(cs, "sandbox.runner_provider"); c.Status != checkOK {
		t.Fatalf("an unneeded missing provider: %+v", c)
	}
	if _, found := findCheck(cs, "sandbox.runner_sources"); found {
		t.Fatal("a sources check with nothing left out")
	}
}

// Setup builds a missing runner image and creates the runner provider
// when an approved source needs it, never when nothing does.
func TestSetup_SandboxStepCoversTheRunner(t *testing.T) {
	r := sandboxReport{Image: component{Status: "present"}, Provider: component{Status: "present"}, Gateway: component{Status: "reachable"},
		RunnerImage:    component{Name: "localhost/aimux-tools:x", Status: "missing"},
		RunnerProvider: component{Name: runnerProvider, Status: "missing", Needed: true}}
	got := sandboxPrepare(r)
	if !slices.Contains(got, "build localhost/aimux-tools:x") || !slices.Contains(got, "create provider "+runnerProvider) {
		t.Fatalf("prepare %q", got)
	}
	r.RunnerImage.Status = "present"
	r.RunnerProvider.Needed = false
	if got := sandboxPrepare(r); len(got) != 0 {
		t.Fatalf("nothing needed, prepare %q", got)
	}
	// Gateway down: whether the provider exists is unknown; a needed one is
	// checked (and created) once setup started the gateway.
	r.Gateway.Status, r.RunnerProvider.Status, r.RunnerProvider.Needed = "unreachable", "unknown", true
	if got := sandboxPrepare(r); !slices.Contains(got, "create provider "+runnerProvider+" if missing") {
		t.Fatalf("gateway down, provider needed: prepare %q", got)
	}
}

// The setup summary counts the runner image and a needed runner provider.
func TestSandboxSummary_CoversTheRunner(t *testing.T) {
	r := sandboxReport{Image: component{Name: wingmanImage, Status: "present"}, Provider: component{Name: wingmanProvider, Status: "present"},
		Gateway: component{Status: "reachable"}, RunnerImage: component{Name: "localhost/aimux-tools:x", Status: "missing"},
		RunnerProvider: component{Name: runnerProvider, Status: "missing", Needed: true}}
	l := sandboxSummary(r)
	if l.Status != checkFail || !strings.Contains(l.Detail, "localhost/aimux-tools:x missing") || !strings.Contains(l.Detail, runnerProvider+" missing") {
		t.Fatalf("summary %+v", l)
	}
	r.RunnerImage.Status, r.RunnerProvider.Needed = "present", false
	if l := sandboxSummary(r); l.Status != checkOK {
		t.Fatalf("summary %+v, want ok when nothing needs the runner provider", l)
	}
}

// Needed comes from the compiler: a gh source needs aimux-github whether
// or not the gateway has it, and is not left out when it has.
func TestSandboxStatus_RunnerProviderNeededWhenPresent(t *testing.T) {
	env, _ := statusRig(t, statusMachine{gatewayUp: true, providers: providersWithWingman + "aimux-github    github             1  0\n",
		sandboxes: []string{"aimux-work-readonly", "aimux-tools"}})
	ghRunnerHome(t, env)
	d := decodeStatus(t, mustJSON(t, env))
	if d.RunnerProvider.Status != "present" || !d.RunnerProvider.Needed || len(d.RunnerDropped) != 0 {
		t.Fatalf("provider %+v dropped %+v", d.RunnerProvider, d.RunnerDropped)
	}
}

// An image-only repair from aimux setup builds the runner image and never
// touches the runner provider (no secret store read) when nothing needs it.
func TestSandboxSetupFor_ImageOnlyLeavesTheRunnerProvider(t *testing.T) {
	env, _ := statusRig(t, statusMachine{})
	f := &fakeRun{answer: func(argv []string) (string, error) {
		s := strings.Join(argv, " ")
		switch {
		case s == "podman image exists "+profile.RunnerImage:
			return "", exitCodeErr{1, ""}
		case strings.HasPrefix(s, "openshell -g local provider list-profiles"):
			return "NAME  TYPE  CATEGORY\nclaude-code-oauth  provider  AGENT\n", nil
		case strings.HasPrefix(s, "openshell -g local provider list"):
			return providersWithWingman, nil
		}
		return "", nil
	}}
	env.Run = f.run
	env.Health = func(context.Context) error { return nil }
	probed := false
	env.RunnerKey.Probe = func(string) (bool, error) { probed = true; return false, errors.New("locked") }
	env.RunnerKey.Lookup = func(context.Context, string) (string, error) { probed = true; return "", errors.New("locked") }
	if _, err := sandboxSetupFor(context.Background(), env, t.TempDir(), "aimux setup", func(string, ...any) {}, false); err != nil {
		t.Fatalf("image-only repair failed: %v", err)
	}
	built := false
	for _, c := range f.calls {
		if strings.Join(c.argv, " ") == "podman build -t "+profile.RunnerImage+" -f "+c.argv[len(c.argv)-2]+" "+c.argv[len(c.argv)-1] {
			built = true
		}
	}
	if !built || probed {
		t.Fatalf("built %v, secret store probed %v (calls %v)", built, probed, f.calls)
	}
}
