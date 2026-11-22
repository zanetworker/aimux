package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Shape of account/rateLimits/read from Codex 0.170 for a ChatGPT account (identifiers removed).
const sampleRateLimits = `{"ordinaryUsageAllowed":true,
 "rateLimits":{"limitId":"codex","primary":{"usedPercent":19,"windowDurationMins":10080,"resetsAt":1795606397},
  "secondary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1795000000},
  "credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"planType":"prolite","rateLimitReachedType":null},
 "rateLimitResetCredits":{"availableCount":3,"credits":[]}}`

func TestParseRateLimits(t *testing.T) {
	got, err := parseRateLimits(json.RawMessage(sampleRateLimits))
	if err != nil {
		t.Fatal(err)
	}
	want := CodexUsage{
		Plan: "prolite", UsageAllowed: true, ResetCredits: 3,
		Windows: []UsageWindow{
			{Minutes: 10080, UsedPercent: 19, ResetsAt: time.Unix(1795606397, 0).UTC()},
			{Minutes: 300, UsedPercent: 42, ResetsAt: time.Unix(1795000000, 0).UTC()},
		},
	}
	if got.Plan != want.Plan || got.UsageAllowed != want.UsageAllowed || got.ResetCredits != want.ResetCredits ||
		len(got.Windows) != 2 || got.Windows[0] != want.Windows[0] || got.Windows[1] != want.Windows[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseRateLimitsOnlyPrimaryAndErrors(t *testing.T) {
	got, err := parseRateLimits(json.RawMessage(`{"rateLimits":{"primary":{"usedPercent":5,"windowDurationMins":10080},"secondary":null}}`))
	if err != nil || len(got.Windows) != 1 || got.Windows[0].UsedPercent != 5 || !got.Windows[0].ResetsAt.IsZero() {
		t.Fatalf("got %+v, err %v; want one window without a reset time", got, err)
	}
	for name, in := range map[string]string{"not json": `{`, "no rateLimits": `{"accountId":"x"}`} {
		if _, err := parseRateLimits(json.RawMessage(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestWriteUsageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "codex-usage.json")
	at := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	u := CodexUsage{Plan: "pro", UsageAllowed: true, Windows: []UsageWindow{{Minutes: 10080, UsedPercent: 19}}, UpdatedAt: at}
	if err := WriteUsageFile(path, u); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, err %v; want 0600", info.Mode().Perm(), err)
	}
	var back CodexUsage
	data, _ := os.ReadFile(path) // #nosec G304 -- test temp file
	if err := json.Unmarshal(data, &back); err != nil || back.Plan != "pro" || !back.UpdatedAt.Equal(at) || back.Windows[0].UsedPercent != 19 {
		t.Fatalf("read back %+v, err %v from %s", back, err, data)
	}
}

func TestCodexBackendReadUsage(t *testing.T) {
	backend, _ := newFakeBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u, err := backend.ReadUsage(ctx)
	if err != nil || u.Plan != "prolite" || len(u.Windows) != 1 || u.Windows[0].UsedPercent != 19 || u.UpdatedAt.IsZero() {
		t.Fatalf("usage %+v, err %v", u, err)
	}
}

func TestPollUsageWritesTheFile(t *testing.T) {
	backend, _ := newFakeBackend(t)
	path := filepath.Join(t.TempDir(), "codex-usage.json")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { PollUsage(ctx, backend.ReadUsage, path, time.Hour, os.Stderr); close(done) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 { // #nosec G304 -- test temp file
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PollUsage did not write the file on its first read")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestPollUsageKeepsTheLastFileOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-usage.json")
	if err := os.WriteFile(path, []byte(`{"plan":"last-good"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	read := func(context.Context) (CodexUsage, error) {
		calls++
		if calls >= 2 {
			cancel()
		}
		return CodexUsage{}, errors.New("not signed in")
	}
	PollUsage(ctx, read, path, 10*time.Millisecond, &stderr)
	data, _ := os.ReadFile(path) // #nosec G304 -- test temp file
	if string(data) != `{"plan":"last-good"}` || !strings.Contains(stderr.String(), "not signed in") || calls < 2 {
		t.Fatalf("file %s, stderr %q, calls %d", data, stderr.String(), calls)
	}
}
