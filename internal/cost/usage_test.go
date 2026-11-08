package cost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendUsage_WritesOneLinePerCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "usage.jsonl")
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	if err := AppendUsage(path, Usage{At: at, Label: "naming", Model: "claude-haiku-5-5", InputTokens: 700, OutputTokens: 120}); err != nil {
		t.Fatal(err)
	}
	if err := AppendUsage(path, Usage{At: at, Label: "naming", Model: "haiku", InputTokens: 10, OutputTokens: 5, Estimated: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a test temp file
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	var first, second Usage
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if first.Label != "naming" || first.InputTokens != 700 || first.OutputTokens != 120 || first.Estimated || !first.At.Equal(at) {
		t.Errorf("first = %+v", first)
	}
	if !second.Estimated {
		t.Error("estimated flag lost")
	}
	// haiku 4.5 alias pricing: 10/1M*$1 + 5/1M*$5
	if second.CostUSD == nil || !almostEqual(*second.CostUSD*1e6, 35) {
		t.Errorf("cost filled from pricing = %v, want 0.000035", second.CostUSD)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestAppendUsage_KeepsGivenCost(t *testing.T) {
	given := 0.25
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := AppendUsage(path, Usage{Label: "naming", Model: "unknown-model", InputTokens: 1, CostUSD: &given}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path) // #nosec G304 -- a test temp file
	var u Usage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatal(err)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.25 || u.CostUnknown || u.At.IsZero() {
		t.Errorf("u = %+v, want the given cost and a time", u)
	}
}

func TestAppendUsage_RejectsMissingLabel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := AppendUsage(path, Usage{Model: "haiku"}); err == nil {
		t.Fatal("want an error for a usage without a label")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("nothing should be written")
	}
}

func TestAppendUsage_UnwritablePathErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := AppendUsage(filepath.Join(blocker, "usage.jsonl"), Usage{Label: "naming"})
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("err = %v, want a usage error with context", err)
	}
}

func TestAppendUsage_UnpricedModelIsMarkedUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := AppendUsage(path, Usage{Label: "naming", Model: "claude-haiku-5-5", InputTokens: 700, OutputTokens: 90}); err != nil {
		t.Fatal(err)
	}
	if err := AppendUsage(path, Usage{Label: "naming", Model: "claude-haiku-4-5", InputTokens: 700}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path) // #nosec G304 -- a test temp file
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var unpriced, priced map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &unpriced)
	_ = json.Unmarshal([]byte(lines[1]), &priced)
	if unpriced["cost_unknown"] != true || unpriced["cost_usd"] != nil || unpriced["input_tokens"].(float64) != 700 {
		t.Errorf("unpriced = %v, want tokens, cost_unknown and no cost_usd", unpriced)
	}
	if priced["cost_unknown"] != nil || priced["cost_usd"].(float64) == 0 {
		t.Errorf("priced = %v", priced)
	}
	if Priced("claude-haiku-5-5") || !Priced("claude-haiku-4-5") || !Priced("haiku") {
		t.Error("Priced")
	}
}
