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

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/parts"
)

func engineIn(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	cmd := newEngineCmdWith(frontEnv{browserEnv: browserEnv{Home: home}})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestEngineCommands_ChangeTheFile(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, ".aimux", "config.yaml")
	read := func() string {
		data, _ := os.ReadFile(config) // #nosec G304 -- the test's own file
		return string(data)
	}
	out, err := engineIn(t, home, "enable", "anthropic", "--no-apply")
	if err != nil || !strings.Contains(out, "Changed") || !strings.Contains(out, "Not applied") {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        enabled: true\n"; read() != want {
		t.Fatalf("file:\n%s", read())
	}
	if out, err = engineIn(t, home, "enable", "anthropic", "--no-apply"); err != nil || !strings.Contains(out, "already says that") {
		t.Errorf("a second time: %v\n%s", err, out)
	}
	if _, err = engineIn(t, home, "add", "enmaas=https://gw.example", "--no-apply"); err != nil || !strings.Contains(read(), "url: https://gw.example") {
		t.Fatalf("add: %v\n%s", err, read())
	}
	if _, err = engineIn(t, home, "disable", "enmaas", "--no-apply"); err != nil {
		t.Fatal(err)
	}
	out, err = engineIn(t, home, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"codex", "anthropic  on", "enmaas     off", "https://gw.example", "secret store: enmaas-key"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if _, err = engineIn(t, home, "remove", "enmaas", "--no-apply"); err != nil || strings.Contains(read(), "enmaas") {
		t.Errorf("remove: %v\n%s", err, read())
	}
	for name, args := range map[string][]string{
		"codex cannot be turned off":       {"disable", "codex", "--no-apply"},
		"anthropic is not added":           {"add", "anthropic=https://x.example", "--no-apply"},
		"an address is needed":             {"add", "lab", "--no-apply"},
		"an upstream that was never added": {"enable", "lab", "--no-apply"},
	} {
		before := read()
		if _, err := engineIn(t, home, args...); err == nil || read() != before {
			t.Errorf("%s: err = %v; want a refusal and the file untouched", name, err)
		}
	}
}

func TestPartsChange_MakesTheServicesMatchAfterTheEdit(t *testing.T) {
	path, services, job := partsFixture(t, "")
	change := partsChange{configPath: path, services: func() []controller.PartService { return services }, ask: func(string) bool { return true }}
	var out bytes.Buffer
	err := change.run(context.Background(), func(file string) error { return parts.EnableEngine(file, "anthropic") }, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	after, _ := job.Read()
	if !strings.Contains(string(after), "<string>--anthropic</string>") || job.reloads() != 1 {
		t.Errorf("the job after enabling the engine (%d reloads):\n%s", job.reloads(), after)
	}
	for _, want := range []string{"Changed " + path, "model proxy", "restart", "model proxy: changed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// Off again: asked, answered no. The file says off; the service was not touched, and the owner is told both.
	out.Reset()
	change.ask = func(string) bool { return false }
	err = change.run(context.Background(), func(file string) error { return parts.DisableEngine(file, "anthropic") }, &out)
	if err == nil || !strings.Contains(err.Error(), "the file holds the change and the service was not restarted") || job.reloads() != 1 {
		t.Fatalf("err = %v, %d reloads", err, job.reloads())
	}
	if s, _ := parts.Load(path); s.Parts["inference-proxy"].Engine("anthropic").On {
		t.Error("the file does not say the engine is off")
	}
}

func TestEngineRows(t *testing.T) {
	views := []parts.EngineView{
		{Name: "codex", Type: parts.TypeCodexAppServer, On: true},
		{Name: "anthropic", Type: parts.TypeMessages, Auth: parts.AuthCaller, URL: parts.AnthropicURL, On: true},
		{Name: "enmaas", Type: parts.TypeMessages, Auth: parts.AuthKey, URL: "https://gw.example", KeyItem: "enmaas-key"},
	}
	rows := engineRows(views, true, []string{"codex", "lab"}, proxySays)
	if len(rows) != 4 || !*rows[0].Running || *rows[1].Running || *rows[2].Running || rows[3].Name != "lab" || !*rows[3].Running {
		t.Fatalf("rows = %+v", rows)
	}
	var out bytes.Buffer
	if err := writeEngines(&out, rows, 7385, proxySays, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"anthropic  on           no", "lab        -            yes", "The running proxy and the file differ"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
	// A proxy that does not answer: only the file is shown, and said so.
	out.Reset()
	_ = writeEngines(&out, engineRows(views, true, nil, proxySilent), 7385, proxySilent, false)
	if !strings.Contains(out.String(), "does not answer on port 7385") {
		t.Errorf("no proxy:\n%s", out.String())
	}
	// The file lists engines and the proxy runs one more: that one should be off, so they differ.
	out.Reset()
	extra := engineRows(views[:2], true, []string{"codex", "anthropic", "lab"}, proxySays)
	_ = writeEngines(&out, extra, 7385, proxySays, false)
	if !strings.Contains(out.String(), "differ") {
		t.Errorf("an engine running that the file does not list:\n%s", out.String())
	}
	// An older proxy answers and does not say which engines it has: unknown, not "no".
	out.Reset()
	_ = writeEngines(&out, engineRows(views, true, nil, proxyOlder), 7385, proxyOlder, false)
	if strings.Contains(out.String(), "codex      on           no") || !strings.Contains(out.String(), "aimux upgrade model-proxy") {
		t.Errorf("an older proxy:\n%s", out.String())
	}
	// A file that says nothing about engines differs from nothing, whatever the proxy runs.
	out.Reset()
	unstated := engineRows(views[:2], false, []string{"codex", "anthropic"}, proxySays)
	_ = writeEngines(&out, unstated, 7385, proxySays, false)
	if strings.Contains(out.String(), "differ") || !strings.Contains(strings.Join(strings.Fields(out.String()), " "), "anthropic not stated yes") || !strings.Contains(out.String(), "says nothing about engines") {
		t.Errorf("nothing stated:\n%s", out.String())
	}
	out.Reset()
	_ = writeEngines(&out, rows, 7385, proxySays, true)
	var report struct {
		Port    int         `json:"port"`
		Engines []engineRow `json:"engines"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Port != 7385 || len(report.Engines) != 4 {
		t.Errorf("json: %v\n%s", err, out.String())
	}
}

func TestPartSwitchCommands(t *testing.T) {
	home := t.TempDir()
	run := func(on bool, args ...string) (string, error) {
		cmd := newPartSwitchCmd(frontEnv{browserEnv: browserEnv{Home: home}}, on)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if out, err := run(true, "mcp-proxy", "--no-apply"); err != nil || !strings.Contains(out, "Changed") {
		t.Fatalf("%v\n%s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".aimux", "config.yaml")) // #nosec G304 -- the test's own file
	if string(data) != "parts:\n  mcp-proxy:\n    enabled: true\n" {
		t.Errorf("file:\n%s", data)
	}
	if _, err := run(false, "mcp-proxy", "--no-apply"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(true, "evals", "--no-apply"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("a part not built yet: %v", err)
	}
	if _, err := run(true, "nope", "--no-apply"); err == nil || !strings.Contains(err.Error(), "inference-proxy") {
		t.Errorf("a part that does not exist should be answered with the ones there are: %v", err)
	}
}

func TestPartsChange_NobodyToAskPointsAtTheApply(t *testing.T) {
	path, services, job := partsFixture(t, "")
	change := partsChange{configPath: path, services: func() []controller.PartService { return services }}
	var out bytes.Buffer
	err := change.run(context.Background(), func(file string) error { return parts.AddEngine(file, "lab", "https://gw.example") }, &out)
	// The add is in the file; running the same command again would be refused as "already there".
	if err == nil || !strings.Contains(err.Error(), "the file holds the change") || !strings.Contains(err.Error(), "aimux setup --apply --yes") || job.reloads() != 0 {
		t.Fatalf("err = %v", err)
	}
	// --json with --no-apply still says what happened.
	out.Reset()
	change.noApply, change.asJSON = true, true
	if err := change.run(context.Background(), func(file string) error { return parts.DisableEngine(file, "lab") }, &out); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Changed, Applied bool
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.Changed || report.Applied {
		t.Errorf("json: %v\n%s", err, out.String())
	}
	// A refused edit is told apart, so nothing is suggested for a name that was not accepted.
	if err := change.run(context.Background(), func(file string) error { return parts.AddEngine(file, "Bad Name", "https://gw.example") }, &out); !errors.Is(err, errEditNotMade) {
		t.Errorf("a refused edit: %v", err)
	}
}

func TestPartsChange_AServiceThatCannotBeManagedIsNotASuccess(t *testing.T) {
	path, services, job := partsFixture(t, "")
	// The job starts something aimux does not recognise as the proxy: it is left alone.
	original, _ := job.Read()
	if err := job.Write([]byte(strings.Replace(string(original), "<string>serve</string>", "<string>status</string>", 1))); err != nil {
		t.Fatal(err)
	}
	change := partsChange{configPath: path, part: "inference-proxy", services: func() []controller.PartService { return services }, ask: func(string) bool { return true }}
	var out bytes.Buffer
	err := change.run(context.Background(), func(file string) error { return parts.EnableEngine(file, "anthropic") }, &out)
	if err == nil || !strings.Contains(err.Error(), "the file holds the change") || !strings.Contains(err.Error(), "left as it is") || job.reloads() != 0 {
		t.Fatalf("err = %v", err)
	}
	// A part with no service aimux manages: stated, and said to be only that.
	change = partsChange{configPath: path, part: "phone-access", needsService: true, services: func() []controller.PartService { return services }}
	if err := change.run(context.Background(), func(file string) error { return parts.EnablePart(file, "phone-access") }, &out); err == nil || !strings.Contains(err.Error(), "does not start or stop anything for this part yet") {
		t.Errorf("phone access: %v", err)
	}
}

func TestPartSwitch_DoesNotClaimWhatItDidNotDo(t *testing.T) {
	home := t.TempDir()
	// No job on this scratch home: the part is stated, and the service cannot be managed.
	cmd := newPartSwitchCmd(frontEnv{browserEnv: browserEnv{Home: home}}, true)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"inference-proxy"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "left as it is") {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	data, _ := os.ReadFile(filepath.Join(home, ".aimux", "config.yaml")) // #nosec G304 -- the test's own file
	if !strings.Contains(string(data), "enabled: true") {
		t.Errorf("the file was not changed:\n%s", data)
	}
}
