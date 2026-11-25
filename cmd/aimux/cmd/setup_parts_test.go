package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/parts"
)

// plainJob is a job kept in a real file, whose service is "up" after any reload.
type plainJob struct {
	path string
	mu   sync.Mutex
	at   time.Time
	n    int
}

func (j *plainJob) Read() ([]byte, error)   { return os.ReadFile(j.path) } // #nosec G304 -- the test's own file
func (j *plainJob) Write(data []byte) error { return os.WriteFile(j.path, data, 0o600) }
func (j *plainJob) Reload() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.at, j.n = time.Now(), j.n+1
	return nil
}

func (j *plainJob) Loaded() (bool, error) { return true, nil }

func (j *plainJob) reloads() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.n
}

// partsFixture is a scratch home with the model proxy's real job file (as a fixture) and a config file.
func partsFixture(t *testing.T, config string) (configPath string, services []controller.PartService, job *plainJob) {
	t.Helper()
	dir := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "parts", "testdata", "com.aimux.model-proxy.plist"))
	if err != nil {
		t.Fatal(err)
	}
	job = &plainJob{path: filepath.Join(dir, "job.plist"), at: time.Now().Add(-time.Hour)}
	if err := os.WriteFile(job.path, fixture, 0o600); err != nil { //nolint:gosec // G703: a path under the test's own temp folder
		t.Fatal(err)
	}
	configPath = filepath.Join(dir, "config.yaml")
	if config != "" {
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	probe := controller.StatusProbe{Name: "model proxy", Run: func(context.Context) controller.ServiceStatus {
		job.mu.Lock()
		defer job.mu.Unlock()
		at := job.at
		return controller.ServiceStatus{Name: "model proxy", State: controller.ServiceOK, StartedAt: &at}
	}}
	return configPath, []controller.PartService{{Service: parts.MustService("model-proxy"), Dir: filepath.Join(dir, "services", "model-proxy"), Job: job, Probe: probe}}, job
}

const anthropicOn = "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic: {}\n"

func TestSetupParts_NothingStated(t *testing.T) {
	for _, config := range []string{"", "shell: /bin/zsh\n"} {
		path, services, job := partsFixture(t, config)
		before, _ := job.Read()
		var out bytes.Buffer
		for _, apply := range []bool{false, true} {
			if err := runSetupParts(context.Background(), path, services, setupPartsOpts{apply: apply, yes: true}, &out); err != nil {
				t.Fatal(err)
			}
		}
		after, _ := job.Read()
		if !strings.Contains(out.String(), "manages no service") || string(before) != string(after) || job.reloads() != 0 {
			t.Errorf("with nothing stated: %s", out.String())
		}
	}
}

func TestSetupParts_CheckSaysAndChangesNothing(t *testing.T) {
	path, services, job := partsFixture(t, anthropicOn)
	before, _ := job.Read()
	var out bytes.Buffer
	err := runSetupParts(context.Background(), path, services, setupPartsOpts{}, &out)
	after, _ := job.Read()
	if !errors.Is(err, errPartsWouldChange) || string(before) != string(after) || job.reloads() != 0 {
		t.Fatalf("err = %v; a check changes nothing", err)
	}
	for _, want := range []string{"model proxy", "restart", "to:   model proxy serve --anthropic", "Nothing was changed. To do this: aimux setup --apply"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	_ = runSetupParts(context.Background(), path, services, setupPartsOpts{asJSON: true}, &out)
	var report struct {
		Changes bool                   `json:"changes"`
		Plan    []controller.JobChange `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.Changes || len(report.Plan) != 1 || report.Plan[0].Action != controller.ActionChange {
		t.Errorf("json: %v\n%s", err, out.String())
	}
}

func TestSetupParts_ApplyAsksBeforeARestart(t *testing.T) {
	path, services, job := partsFixture(t, anthropicOn)
	before, _ := job.Read()
	var out bytes.Buffer
	// Nobody to ask and no --yes: refused, with how to agree.
	err := runSetupParts(context.Background(), path, services, setupPartsOpts{apply: true}, &out)
	if err == nil || !strings.Contains(err.Error(), "--yes") || job.reloads() != 0 {
		t.Fatalf("err = %v", err)
	}
	// Asked, and the answer is no.
	var asked string
	err = runSetupParts(context.Background(), path, services, setupPartsOpts{apply: true, ask: func(q string) bool { asked = q; return false }}, &out)
	if after, _ := job.Read(); err == nil || string(after) != string(before) || job.reloads() != 0 || !strings.Contains(asked, "Restart the model proxy now?") {
		t.Fatalf("err = %v, asked %q", err, asked)
	}
	// Asked, and the answer is yes.
	out.Reset()
	if err := runSetupParts(context.Background(), path, services, setupPartsOpts{apply: true, ask: func(string) bool { return true }}, &out); err != nil {
		t.Fatal(err)
	}
	after, _ := job.Read()
	if !strings.Contains(string(after), "<string>--anthropic</string>") || job.reloads() != 1 || !strings.Contains(out.String(), "model proxy: changed") {
		t.Errorf("after yes (%d reloads):\n%s", job.reloads(), out.String())
	}
	// And then there is nothing left to do, for a check or an apply.
	out.Reset()
	if err := runSetupParts(context.Background(), path, services, setupPartsOpts{}, &out); err != nil || !strings.Contains(out.String(), "Nothing to change.") {
		t.Errorf("a check afterwards: %v\n%s", err, out.String())
	}
}

func TestSetupParts_ProblemsStopEverything(t *testing.T) {
	path, services, job := partsFixture(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://gw.example\n        key: sk-live-pasted-by-mistake\n")
	var out bytes.Buffer
	err := runSetupParts(context.Background(), path, services, setupPartsOpts{apply: true, yes: true}, &out)
	if err == nil || job.reloads() != 0 || !strings.Contains(out.String(), "line 8") {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "sk-live-pasted-by-mistake") {
		t.Error("the output repeats what was written where a key reference belongs")
	}
}

func TestSetupParts_AdoptNeedsNoQuestion(t *testing.T) {
	path, services, job := partsFixture(t, "parts:\n  inference-proxy:\n    enabled: true\n")
	before, _ := job.Read()
	var out bytes.Buffer
	if err := runSetupParts(context.Background(), path, services, setupPartsOpts{apply: true}, &out); err != nil {
		t.Fatal(err)
	}
	after, _ := job.Read()
	if string(before) != string(after) || job.reloads() != 0 || !strings.Contains(out.String(), "model proxy: adopted") {
		t.Errorf("taking the job under management:\n%s", out.String())
	}
}

func TestSetupParts_TheFileChangedWhileTheQuestionStood(t *testing.T) {
	path, services, job := partsFixture(t, anthropicOn)
	before, _ := job.Read()
	var out bytes.Buffer
	err := runSetupParts(context.Background(), path, services, setupPartsOpts{apply: true, ask: func(string) bool {
		// The owner edits the file, then answers yes to what he was shown before.
		_ = os.WriteFile(path, []byte("parts:\n  inference-proxy:\n    enabled: true\n    port: 7400\n"), 0o600)
		return true
	}}, &out)
	if after, _ := job.Read(); !errors.Is(err, controller.ErrStalePlan) || string(before) != string(after) || job.reloads() != 0 {
		t.Fatalf("err = %v; a yes to an old plan was applied to a new file", err)
	}
}
