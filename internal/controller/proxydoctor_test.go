package controller

import (
	"strings"
	"testing"
	"time"
)

var doctorNow = time.Date(2026, 11, 26, 16, 0, 0, 0, time.UTC)

func healthyProxy() ProxyFacts {
	yes := true
	ok := doctorNow.Add(-2 * time.Minute)
	return ProxyFacts{
		Port: 7385, Now: doctorNow, CLIBuild: "v0.14.0-289-gabc", Answers: ProxyAnswers, Build: "v0.14.0-289-gabc", Status: "ok",
		Engines: []string{"codex"}, KeyStored: true, KeyProven: &yes, HealthRead: true, CodexRunning: true, CodexCheckOKAt: &ok,
		Calls: 12,
	}
}

func finding(t *testing.T, fs []ProxyFinding, id string) ProxyFinding {
	t.Helper()
	for _, f := range fs {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no finding %q in %+v", id, fs)
	return ProxyFinding{}
}

func TestDiagnoseProxy_HealthyHasNothingToFix(t *testing.T) {
	fs := DiagnoseProxy(healthyProxy())
	for _, f := range fs {
		if f.Level != FindingOK || f.Fix != "" {
			t.Errorf("%+v", f)
		}
	}
	for _, id := range []string{"service", "build", "key", "codex", "engines", "ledger", "calls", "shell"} {
		finding(t, fs, id)
	}
}

// Nothing answers: that is the finding, and what cannot be known without an answer is not guessed at.
func TestDiagnoseProxy_NothingAnswers(t *testing.T) {
	f := healthyProxy()
	f.Answers, f.HealthRead = ProxyNoAnswer, false
	fs := DiagnoseProxy(f)
	if s := finding(t, fs, "service"); s.Level != FindingFail || !strings.Contains(s.Detail, "7385") || s.Fix == "" {
		t.Fatalf("%+v", s)
	}
	for _, x := range fs {
		if x.ID == "codex" || x.ID == "build" || x.ID == "engines" || x.ID == "ledger" {
			t.Errorf("guessed at %s with no answer: %+v", x.ID, x)
		}
	}
	f.Answers = ProxyOtherListener
	if s := finding(t, DiagnoseProxy(f), "service"); s.Level != FindingFail || !strings.Contains(s.Detail, "not the model proxy") {
		t.Fatalf("%+v", s)
	}
}

func TestDiagnoseProxy_EachThingThatBreaks(t *testing.T) {
	no := false
	old := doctorNow.Add(-3 * time.Hour)
	for name, tc := range map[string]struct {
		change func(*ProxyFacts)
		id     string
		level  string
		detail string
		fix    string
	}{
		"older than the CLI":         {func(f *ProxyFacts) { f.Build = "v0.14.0-283-gdef" }, "build", FindingWarn, "283", "aimux upgrade model-proxy"},
		"no key stored":              {func(f *ProxyFacts) { f.KeyStored, f.KeyProven = false, nil }, "key", FindingFail, "no proxy key", "aimux model proxy serve"},
		"a listener without the key": {func(f *ProxyFacts) { f.KeyProven = &no }, "key", FindingFail, "does not hold", ""},
		"degraded":                   {func(f *ProxyFacts) { f.Status, f.Reason = "degraded", "codex turns are failing" }, "service", FindingWarn, "codex turns are failing", ""},
		"the Codex check fails": {func(f *ProxyFacts) {
			at := doctorNow.Add(-time.Minute)
			f.CodexCheckErrorAt, f.CodexCheckError = &at, "not logged in"
		}, "codex", FindingFail, "not logged in", "codex login"},
		"the Codex check is stale":                     {func(f *ProxyFacts) { f.CodexCheckOKAt = &old }, "codex", FindingWarn, "3h", ""},
		"an engine the file wants and the proxy lacks": {func(f *ProxyFacts) { f.Stated = map[string]bool{"codex": true, "anthropic": true} }, "engines", FindingWarn, "anthropic", "aimux setup --apply"},
		"an engine the file turned off still running": {func(f *ProxyFacts) {
			f.Engines, f.Stated = []string{"codex", "anthropic"}, map[string]bool{"anthropic": false}
		}, "engines", FindingWarn, "anthropic", "aimux setup --apply"},
		"the ledger is off":      {func(f *ProxyFacts) { f.LedgerOff = true }, "ledger", FindingFail, "not being written", ""},
		"ledger lines were lost": {func(f *ProxyFacts) { f.LedgerFailed = 3 }, "ledger", FindingWarn, "3", ""},
		"rate limited upstream": {func(f *ProxyFacts) {
			f.Failures = []CallFailure{{Engine: "anthropic", Status: 429, Count: 2, LastAt: doctorNow.Add(-5 * time.Minute)}}
		}, "calls", FindingWarn, "rate limit", ""},
		"the login is refused": {func(f *ProxyFacts) {
			f.Failures = []CallFailure{{Engine: "anthropic", Status: 401, Count: 4, LastAt: doctorNow}}
		}, "calls", FindingFail, "refused the login", "/login"},
		"the upstream cannot be reached": {func(f *ProxyFacts) {
			f.Failures = []CallFailure{{Engine: "anthropic", Error: "upstream_unreachable", Count: 4, LastAt: doctorNow}}
		}, "calls", FindingFail, "upstream_unreachable", ""},
		"this shell needs an engine that is off": {func(f *ProxyFacts) { f.ShellThrough, f.ShellEngine, f.ShellEngineOn = true, "anthropic", &no }, "shell", FindingFail, "anthropic", "aimux engine enable anthropic"},
	} {
		f := healthyProxy()
		tc.change(&f)
		got := finding(t, DiagnoseProxy(f), tc.id)
		if got.Level != tc.level || !strings.Contains(got.Detail, tc.detail) || !strings.Contains(got.Fix, tc.fix) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// What a finding says never depends on a value that could be a secret, and the worst finding decides the verdict.
func TestProxyVerdict(t *testing.T) {
	if got := ProxyVerdict(DiagnoseProxy(healthyProxy())); got != FindingOK {
		t.Errorf("healthy = %s", got)
	}
	f := healthyProxy()
	f.Build, f.LedgerOff = "v0.14.0-283-gdef", true
	if got := ProxyVerdict(DiagnoseProxy(f)); got != FindingFail {
		t.Errorf("warn and fail together = %s", got)
	}
}

func TestDiagnoseProxy_WhatCouldNotBeReadIsSaidNotGuessed(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*ProxyFacts)
		id     string
		level  string
		detail string
		fix    string
	}{
		"the detailed health could not be read": {func(f *ProxyFacts) { f.HealthRead, f.HealthError = false, "timed out" }, "codex", FindingWarn, "timed out", ""},
		"a proxy too old to prove the key":      {func(f *ProxyFacts) { f.KeyProven, f.ProofUnsupported = nil, true }, "key", FindingWarn, "cannot prove", "aimux upgrade model-proxy"},
		"the config file has a problem":         {func(f *ProxyFacts) { f.ConfigProblem = "the parts section has a problem" }, "engines", FindingWarn, "problem", "aimux setup --check"},
		"the history could not be read":         {func(f *ProxyFacts) { f.Calls, f.HistoryError = 0, "permission denied" }, "calls", FindingWarn, "permission denied", ""},
		"the history is incomplete":             {func(f *ProxyFacts) { f.HistoryIncomplete = true }, "calls", FindingWarn, "not all of the history", ""},
		"the proxy could not reach the upstream, told as a 502": {func(f *ProxyFacts) {
			f.Failures = []CallFailure{{Engine: "anthropic", Status: 502, Error: "upstream_unreachable", Count: 3, LastAt: doctorNow}}
		}, "calls", FindingFail, "upstream_unreachable", ""},
	} {
		f := healthyProxy()
		tc.change(&f)
		got := finding(t, DiagnoseProxy(f), tc.id)
		if got.Level != tc.level || !strings.Contains(got.Detail, tc.detail) || !strings.Contains(got.Fix, tc.fix) {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// The ledger is known from the keyless answer, so it is judged even when the detailed health is not read.
	f := healthyProxy()
	f.HealthRead, f.LedgerOff = false, true
	if got := finding(t, DiagnoseProxy(f), "ledger"); got.Level != FindingFail {
		t.Errorf("ledger without the detailed health: %+v", got)
	}
}

// A turn the owner stopped is not a failure of the proxy.
func TestDiagnoseProxy_AStoppedTurnIsNotAFailure(t *testing.T) {
	f := healthyProxy()
	f.Failures = []CallFailure{{Engine: "codex", Error: "client_gone", Count: 2, LastAt: doctorNow}}
	got := finding(t, DiagnoseProxy(f), "calls")
	if got.Level != FindingOK || !strings.Contains(got.Detail, "2 stopped by the session") {
		t.Fatalf("%+v", got)
	}
}

// A conversation too long for its model is the session's to shorten, not a failure of the proxy.
func TestDiagnoseProxy_AConversationTooLongIsNotAProxyFailure(t *testing.T) {
	f := healthyProxy()
	f.Failures = []CallFailure{{Engine: "codex", Status: 400, Error: "context_too_long", Count: 3, LastAt: doctorNow}}
	got := finding(t, DiagnoseProxy(f), "calls")
	if got.Level != FindingWarn || !strings.Contains(got.Detail, "did not fit") || !strings.Contains(got.Fix, "/compact") {
		t.Fatalf("%+v", got)
	}
}
