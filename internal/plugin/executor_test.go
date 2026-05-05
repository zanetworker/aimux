package plugin

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExecutor_RunsCommand(t *testing.T) {
	p := Plugin{
		Name:      "test",
		Command:   `echo '{"metrics":{"items":[{"label":"count","value":42}]}}'`,
		CacheSecs: 1,
	}
	exec := NewExecutor([]Plugin{p})

	data, err := exec.Execute("test")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	raw, ok := data["metrics"]
	if !ok {
		t.Fatal("expected metrics key")
	}

	var metrics struct {
		Items []MetricItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &metrics); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(metrics.Items) != 1 || metrics.Items[0].Label != "count" {
		t.Errorf("unexpected: %+v", metrics)
	}
}

func TestExecutor_CachesResult(t *testing.T) {
	p := Plugin{
		Name:      "ts",
		Command:   `python3 -c "import time; import json; print(json.dumps({'t': time.time_ns()}))"`,
		CacheSecs: 5,
	}
	e := NewExecutor([]Plugin{p})

	d1, _ := e.Execute("ts")
	d2, _ := e.Execute("ts")

	r1, _ := json.Marshal(d1)
	r2, _ := json.Marshal(d2)
	if string(r1) != string(r2) {
		t.Error("expected cached (identical) result")
	}
}

func TestExecutor_CacheExpires(t *testing.T) {
	p := Plugin{
		Name:      "ts",
		Command:   `python3 -c "import time; import json; print(json.dumps({'t': time.time_ns()}))"`,
		CacheSecs: 1,
	}
	e := NewExecutor([]Plugin{p})

	d1, _ := e.Execute("ts")
	time.Sleep(1100 * time.Millisecond)
	d2, _ := e.Execute("ts")

	r1, _ := json.Marshal(d1)
	r2, _ := json.Marshal(d2)
	if string(r1) == string(r2) {
		t.Error("cache should have expired")
	}
}

func TestExecutor_UnknownPlugin(t *testing.T) {
	e := NewExecutor(nil)
	_, err := e.Execute("nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestExecutor_CommandFailure(t *testing.T) {
	p := Plugin{Name: "bad", Command: "false"}
	e := NewExecutor([]Plugin{p})
	_, err := e.Execute("bad")
	if err == nil {
		t.Fatal("expected error")
	}
}
