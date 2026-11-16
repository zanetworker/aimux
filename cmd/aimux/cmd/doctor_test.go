package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

type doctorDoc struct {
	SchemaVersion string        `json:"schema_version"`
	Healthy       bool          `json:"healthy"`
	Problems      int           `json:"problems"`
	Checks        []doctorCheck `json:"checks"`
}

func (r *frontRig) doctor(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runFront(t, newDoctorCmdWith(r.env), append([]string{"doctor"}, args...)...)
}

func (r *frontRig) doctorDoc(t *testing.T) (doctorDoc, error) {
	t.Helper()
	out, _, err := r.doctor(t, "--json")
	var d doctorDoc
	if jerr := json.Unmarshal([]byte(out), &d); jerr != nil {
		t.Fatalf("doctor --json is not JSON: %v\n%s", jerr, out)
	}
	return d, err
}

func checkByID(d doctorDoc, id string) (doctorCheck, bool) {
	for _, c := range d.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return doctorCheck{}, false
}

// lstart is t as ps -o lstart prints it.
func lstart(t time.Time) string { return t.Local().Format("Mon Jan _2 15:04:05 2006") }

// A healthy install: every check is ok, exit 0, nothing written, only
// read-only probes run.
func TestDoctor_HealthyExitsZeroAndWritesNothing(t *testing.T) {
	r := newFrontRig(t)
	before := snapshotTree(t, r.env.Home)
	d, err := r.doctorDoc(t)
	if err != nil {
		t.Fatalf("healthy doctor failed: %v", err)
	}
	if !d.Healthy || d.Problems != 0 || d.SchemaVersion != "1" || len(d.Checks) == 0 {
		t.Fatalf("doc = %+v", d)
	}
	for _, c := range d.Checks {
		if c.Status != "ok" {
			t.Errorf("check %+v", c)
		}
	}
	for _, id := range []string{"tools.registry", "tools.pins", "tools.review", "tools.no_set", "tools.omitted", "tools.legacy",
		"router.key", "port.7390", "gateway", "wingman.serve", "sandbox.policy"} {
		if _, ok := checkByID(d, id); !ok {
			t.Errorf("no %s check", id)
		}
	}
	out, _, err := r.doctor(t)
	if err != nil || !strings.Contains(out, "✓ ") {
		t.Fatalf("text: %v\n%s", err, out)
	}
	if after := snapshotTree(t, r.env.Home); after != before {
		t.Fatalf("doctor changed home:\nbefore %s\nafter  %s", before, after)
	}
	assertOnlyReadOnly(t, r.f)
}

// Each problem is one check with its status and a fix; doctor exits 1.
func TestDoctor_Problems(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(t *testing.T, r *frontRig)
		id     string
		status string
		detail string // substring
		fix    string // substring
	}{
		{"changed tool", func(_ *testing.T, r *frontRig) { r.live["jira"] = []json.RawMessage{rawTool("search", "number")} },
			"tools.review.jira:readonly/search", "warn", "changed", "aimux tools diff jira:readonly/search, then aimux tools approve jira:readonly/search"},
		{"unpinned tool", func(t *testing.T, r *frontRig) {
			r.live["slack"] = append(r.live["slack"], rawTool("thread", "string"))
			reg := frontRegistry(r.env.Home)
			reg.Sets["slack:readonly"] = toolreg.Set{Source: "slack", Tools: []string{"history", "thread"}}
			rewriteRegistry(t, r, reg)
		}, "tools.review.slack:readonly/thread", "warn", "unpinned", "aimux tools approve slack:readonly/thread"},
		{"source changed", func(t *testing.T, r *frontRig) {
			reg := frontRegistry(r.env.Home)
			reg.Sources["jira"] = toolreg.Source{Type: toolreg.TypeMCPHTTP, URL: "http://127.0.0.1:10/mcp/jira"}
			rewriteRegistry(t, r, reg)
		}, "tools.review.jira:readonly/search", "warn", "source_changed", "aimux tools approve jira:readonly/search"},
		{"tool in no set and set omitted", func(t *testing.T, r *frontRig) {
			reg := frontRegistry(r.env.Home)
			delete(reg.Sets, "google:readonly")
			rewriteRegistry(t, r, reg)
		}, "tools.no_set.google/get_doc", "warn", "no set", "tool-sources.yaml"},
		{"set omitted from a compile names the profile", func(t *testing.T, r *frontRig) {
			reg := frontRegistry(r.env.Home)
			delete(reg.Sets, "google:readonly")
			rewriteRegistry(t, r, reg)
		}, "tools.omitted.work-readonly.google:readonly", "warn", "work-readonly", "google:readonly"},
		{"per-set registry error names the profile", func(t *testing.T, r *frontRig) {
			reg := frontRegistry(r.env.Home)
			reg.Sets["google:readonly"] = toolreg.Set{Source: "gdrive", Tools: []string{"get_doc"}}
			rewriteRegistry(t, r, reg)
		}, "tools.registry.set.google:readonly", "warn", "requested by work-readonly", "tool-sources.yaml"},
		{"pins unreadable", func(t *testing.T, r *frontRig) {
			if err := os.WriteFile(toolgateway.DefaultPinsPath(r.env.Home), []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "tools.pins", "fail", "pins.json", "aimux tools approve"},
		{"legacy readonly.json not migrated", func(t *testing.T, r *frontRig) {
			if err := os.Remove(toolreg.DefaultPath(r.env.Home)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(toolreg.LegacyPath(r.env.Home), []byte(legacyFrontJSON(r.env.Home)), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "tools.legacy", "warn", "readonly.json", "aimux setup"},
		{"backend down", func(_ *testing.T, r *frontRig) { r.down = map[string]error{"jira": errors.New("connection refused")} },
			"tools.backend.jira", "warn", "connection refused", "start"},
		{"router key missing", func(_ *testing.T, r *frontRig) { r.keyFound = false },
			"router.key", "warn", "no OpenRouter key", "security add-generic-password"},
		{"router key only in the shell", func(t *testing.T, r *frontRig) {
			r.keyFound = false
			r.env.AutoKey.LookupEnv = func(n string) (string, bool) { return "sk-or-x", n == "OPENROUTER_API_KEY" }
		}, "router.key", "warn", "shell environment", "security add-generic-password"},
		{"router config invalid", func(t *testing.T, r *frontRig) {
			p := filepath.Join(r.env.Home, ".config", "aimux", "deciders.yaml")
			if err := os.WriteFile(p, []byte("thresold: 2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "router.config", "fail", "deciders.yaml", "deciders.yaml"},
		{"port held by another program", func(_ *testing.T, r *frontRig) {
			r.m.lsof = "4242\n"
			r.m.ps = fmt.Sprintf("4242 %s /usr/bin/python3 -m http.server 7390\n", lstart(frontNow))
		}, "port.7390", "fail", "pid 4242 (/usr/bin/python3 -m http.server 7390)", "kill 4242"},
		{"gateway down", func(_ *testing.T, r *frontRig) { r.m.gatewayUp = false },
			"gateway", "fail", "unreachable", "openshell-gateway"},
		{"gateway answers but sandbox list fails", func(_ *testing.T, r *frontRig) {
			r.m.listErr = exitCodeErr{1, "Error: Unknown gateway 'local'"}
		}, "gateway", "warn", "sandbox list failed", "openshell gateway add http://<bind_address of ~/.config/openshell/gateway-podman.toml> --name local"},
		{"serve on an older binary", func(_ *testing.T, r *frontRig) {
			r.m.ps = fmt.Sprintf("555 %s %s remote serve --local-only\n", lstart(frontNow.Add(-2*time.Hour)), rigBinary(r.env.Home))
		}, "wingman.serve", "warn", "pid 555", "kill 555; the next Wingman panel starts serve from " + "<bin>"},
		{"serve on another binary", func(_ *testing.T, r *frontRig) {
			r.m.ps = fmt.Sprintf("556 %s /opt/old/aimux remote serve --local-only\n", lstart(frontNow))
		}, "wingman.serve", "warn", "/opt/old/aimux", "kill 556"},
		{"sandbox policy stale", func(t *testing.T, r *frontRig) { writeDigest(t, r.env.Home, workSandbox, "sha256:old") },
			"sandbox.policy.work-readonly", "warn", "stale", "applies on the next Workspace question; or run aimux sandbox verify"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newFrontRig(t)
			c.break_(t, r)
			before := snapshotTree(t, r.env.Home)
			d, err := r.doctorDoc(t)
			// Only a fail changes the exit code; a warn is reported, exit 0.
			if (err != nil) != hasFail(d) || d.Healthy == hasFail(d) || d.Problems == 0 {
				t.Fatalf("err = %v with checks %+v", err, d.Checks)
			}
			got, ok := checkByID(d, c.id)
			if !ok {
				t.Fatalf("no check %s in %+v", c.id, d.Checks)
			}
			c.fix = strings.ReplaceAll(c.fix, "<bin>", rigBinary(r.env.Home))
			if got.Status != c.status || !strings.Contains(got.Detail, c.detail) || !strings.Contains(got.Fix, c.fix) {
				t.Fatalf("check = %+v, want status %s detail ~%q fix ~%q", got, c.status, c.detail, c.fix)
			}
			out, _, _ := r.doctor(t)
			if !strings.Contains(out, c.id) || !strings.Contains(out, got.Fix) {
				t.Fatalf("text lacks %s and its fix:\n%s", c.id, out)
			}
			if strings.Contains(out, "not_allowed") {
				t.Fatal("not_allowed is not a status")
			}
			if after := snapshotTree(t, r.env.Home); after != before {
				t.Fatalf("doctor changed home:\nbefore %s\nafter  %s", before, after)
			}
			assertOnlyReadOnly(t, r.f)
		})
	}
}

// A serve on the installed binary started after it was installed, or
// holding :7390, is healthy.
func TestDoctor_CurrentServeIsHealthy(t *testing.T) {
	r := newFrontRig(t)
	bin := rigBinary(r.env.Home)
	// Started a second after the replace (ps shows whole seconds): the same build.
	if err := os.Chtimes(bin, frontNow.Add(1500*time.Millisecond), frontNow.Add(1500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	// A grep and a shell wrapper that mention the serve are not serves.
	r.m.ps = fmt.Sprintf("  77 %s %s remote serve --local-only\n  90 %s grep remote serve --local-only\n  91 %s sh -c '%s remote serve --local-only'\n",
		lstart(frontNow), bin, lstart(frontNow), lstart(frontNow), bin)
	r.m.lsof = "77\n"
	d, err := r.doctorDoc(t)
	if err != nil {
		t.Fatalf("%v %+v", err, d)
	}
	if c, _ := checkByID(d, "port.7390"); c.Status != "ok" || !strings.Contains(c.Detail, "pid 77") {
		t.Fatalf("port = %+v", c)
	}
	if c, _ := checkByID(d, "wingman.serve"); c.Status != "ok" || !strings.Contains(c.Detail, "pid 77") {
		t.Fatalf("serve = %+v", c)
	}
}

func rewriteRegistry(t *testing.T, r *frontRig, reg toolreg.Registry) {
	t.Helper()
	data, err := toolreg.Marshal(reg) // WriteNew refuses a registry with errors; doctor must see one
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolreg.DefaultPath(r.env.Home), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// A rewrite within the same second must still look changed to a reload.
	if err := os.Chtimes(toolreg.DefaultPath(r.env.Home), time.Now().Add(time.Second), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func hasFail(d doctorDoc) bool {
	for _, c := range d.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

// A warn alone (a stale policy) is reported, but doctor exits 0.
func TestDoctor_WarnsDoNotFail(t *testing.T) {
	r := newFrontRig(t)
	writeDigest(t, r.env.Home, workSandbox, "sha256:old")
	d, err := r.doctorDoc(t)
	if err != nil || !d.Healthy || d.Problems != 1 {
		t.Fatalf("err %v doc %+v", err, d)
	}
}

// Without host.sh, Wingman is not installed and the serve check is skipped.
func TestDoctor_NoWrapperSkipsServeCheck(t *testing.T) {
	r := newFrontRig(t)
	if err := os.Remove(filepath.Join(r.env.Home, ".aimux", "wingman", "host.sh")); err != nil {
		t.Fatal(err)
	}
	r.m.ps = fmt.Sprintf("556 %s /opt/old/aimux remote serve --local-only\n", lstart(frontNow))
	d, _ := r.doctorDoc(t)
	if c, _ := checkByID(d, "wingman.serve"); c.Status != "ok" || !strings.Contains(c.Detail, "Wingman not installed") {
		t.Fatalf("serve = %+v", c)
	}
}

// Reading an unmigrated readonly.json writes nothing, not even a temp copy.
func TestDoctor_LegacyReadWritesNoTempFiles(t *testing.T) {
	r := newFrontRig(t)
	if err := os.Remove(toolreg.DefaultPath(r.env.Home)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolreg.LegacyPath(r.env.Home), []byte(legacyFrontJSON(r.env.Home)), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chmod(tmp, 0o500); err != nil { //nolint:gosec // G302: a test dir made read-only so a temp copy cannot be made
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) }) //nolint:gosec // G302: restore the test dir so it can be removed
	t.Setenv("TMPDIR", tmp)
	d, _ := r.doctorDoc(t)
	if c, _ := checkByID(d, "tools.review"); c.Status != "ok" {
		t.Fatalf("legacy registry not read in memory: %+v", d.Checks)
	}
}

// hostWrapperExe reads back the path writeHostWrapper quoted, spaces and
// quotes included; a missing or foreign wrapper is an error.
func TestHostWrapperExe_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "host.sh")
	for _, exe := range []string{"/opt/aimux/bin/aimux", "/Users/o/App Bin/it's/aimux"} {
		if err := writeHostWrapper(p, exe, []string{"/opt/node/bin"}); err != nil {
			t.Fatal(err)
		}
		got, err := hostWrapperExe(p)
		if err != nil || got != exe {
			t.Fatalf("hostWrapperExe = %q, %v; want %q", got, err, exe)
		}
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hostWrapperExe(p); err == nil {
		t.Fatal("a wrapper with no exec line parsed")
	}
	if _, err := hostWrapperExe(filepath.Join(dir, "none")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing wrapper: %v", err)
	}
}

// A stale work policy suggests aimux sandbox verify only while port 7390
// is free: with remote serve holding it, verify cannot run, and the next
// Workspace question applies the policy anyway.
func TestDoctor_StaleWorkPolicyFixFollowsThePort(t *testing.T) {
	r := newFrontRig(t)
	writeDigest(t, r.env.Home, workSandbox, "sha256:old")
	d, _ := r.doctorDoc(t)
	if c, _ := checkByID(d, "sandbox.policy.work-readonly"); !strings.Contains(c.Fix, "aimux sandbox verify") {
		t.Fatalf("port free: fix %q, want verify offered", c.Fix)
	}
	bin := rigBinary(r.env.Home)
	if err := os.Chtimes(bin, frontNow, frontNow); err != nil {
		t.Fatal(err)
	}
	r.m.ps = fmt.Sprintf("  77 %s %s remote serve --local-only\n", lstart(frontNow), bin)
	r.m.lsof = "77\n"
	d, _ = r.doctorDoc(t)
	c, _ := checkByID(d, "sandbox.policy.work-readonly")
	if c.Fix != "applies on the next Workspace question" {
		t.Fatalf("port held by serve: fix %q, want only the next Workspace question", c.Fix)
	}
}

// Every problem's fix is also a Next: line on stderr, as the other nouns
// print them, in text and JSON mode; a healthy run prints none.
func TestDoctor_NextLinesOnStderr(t *testing.T) {
	r := newFrontRig(t)
	if _, errOut, _ := r.doctor(t); strings.Contains(errOut, "Next:") {
		t.Fatalf("healthy: stderr %q", errOut)
	}
	r.live["jira"] = []json.RawMessage{rawTool("search", "number")}
	r.keyFound = false
	for _, args := range [][]string{nil, {"--json"}} {
		out, errOut, _ := r.doctor(t, args...)
		if strings.Contains(out, "Next:") {
			t.Errorf("%v: Next: on stdout:\n%s", args, out)
		}
		for _, want := range []string{"Next: aimux tools diff jira:readonly/search, then aimux tools approve jira:readonly/search", "Next: security add-generic-password"} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%v: stderr lacks %q:\n%s", args, want, errOut)
			}
		}
	}
}

// Pins of a source aimux tools remove took out are kept, unused: doctor
// says so and points at tools add, not at a set of a source that is gone.
func TestDoctor_PinsOfARemovedSource(t *testing.T) {
	r := newFrontRig(t)
	reg := frontRegistry(r.env.Home)
	delete(reg.Sources, "google")
	delete(reg.Sets, "google:readonly")
	rewriteRegistry(t, r, reg)
	d, _ := r.doctorDoc(t)
	c, ok := checkByID(d, "tools.no_set.google/get_doc")
	if !ok || c.Status != "warn" || !strings.Contains(c.Detail, "kept but unused") || !strings.Contains(c.Fix, "aimux tools add") || strings.Contains(c.Fix, "to a set of source") {
		t.Fatalf("check = %+v", c)
	}
	if _, errOut, _ := r.doctor(t); strings.Contains(errOut, "Next: nothing to do") || !strings.Contains(errOut, "Next: to serve it again, add source google back with aimux tools add") {
		t.Fatalf("stderr:\n%s", errOut)
	}
}
