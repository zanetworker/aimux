package views

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/zanetworker/aimux/internal/controller"
)

func systemSnapshot(now time.Time) controller.SystemSnapshot {
	yes := true
	started, last := now.Add(-40*time.Minute), now.Add(-10*time.Second)
	return controller.SystemSnapshot{
		At: now,
		Services: controller.StatusOverview{CLIBuild: "v0.14.0-332-g8c63cdd7", Services: []controller.ServiceStatus{
			{Name: "model proxy", State: controller.ServiceOK, Build: "v0.14.0-326-g2b30bc09", StartedAt: &started, Address: "127.0.0.1:7385", LastActivity: &last, ActivityNote: "turn"},
			{Name: "collector", State: controller.ServiceDown, Reason: "nothing answers", Address: "127.0.0.1:4318",
				Attention: []controller.Attention{{Problem: "it does not answer", Fix: "aimux upgrade collector"}}},
		}},
		Doctor: controller.ProxyReport{Port: 7385, Verdict: controller.FindingWarn,
			Allowance: []string{"Claude allowance: 53% of the week used, starts again in 3d13h (seen 3m ago)"}, Findings: []controller.ProxyFinding{
				{ID: "service", Level: controller.FindingOK, Detail: "answers on 127.0.0.1:7385"},
				{ID: "calls", Level: controller.FindingWarn, Detail: "2 calls hit anthropic's rate limit", Fix: "wait for the limit to pass"},
			}},
		EnginesPort: 7385,
		Engines: []controller.EngineRow{
			{Name: "codex", On: true, Type: "codex-app-server", Stated: true, Running: &yes},
			{Name: "anthropic", On: false, Type: "messages", Stated: true, Running: &yes},
		},
		Roles:     []controller.RoleRow{{Role: "main"}, {Role: "subagent", Entry: "fast", Model: "codex:gpt-6.2-sol", Engine: "codex"}},
		Subagents: []controller.SubagentRow{{Name: "scout", DefinedIn: "~/.claude/agents", RunsOn: "codex", FromRole: true}},
		Meter: controller.MeterReport{Files: 2, NoUsage: 3, Rows: []controller.MeterRow{
			{Key: "main", Requests: 425, Failed: 45, InputTokens: 5_500_000, CachedTokens: 45_000_000, OutputTokens: 142_000, Engines: []string{"anthropic", "codex"}, Estimated: true},
			{Key: "subagent", Requests: 134, InputTokens: 1_800_000, OutputTokens: 33_000, Engines: []string{"codex"}},
		}},
	}
}

// The System view shows, on one screen, what aimux status, aimux model proxy doctor, aimux engine list and aimux
// cost --by role print, with the command that fixes each thing that is wrong.
func TestSystemView_ShowsTheMachine(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	v := NewSystemView()
	v.SetSize(140, 200)
	v.SetNow(func() time.Time { return now })
	v.SetSnapshot(systemSnapshot(now))
	out := v.View()
	for _, want := range []string{
		// Services, as aimux status shows them, and the fix for the one that is down.
		"SERVICES", "model proxy", "ok", "326", "40m", "127.0.0.1:7385", "turn 10s ago",
		"collector", "down", "nothing answers", "collector: it does not answer", "aimux upgrade collector",
		// The diagnosis: its headline, what is worth a look and what to do; a passing check is counted, not listed.
		"MODEL PROXY", "1 thing to look at; nothing is failing.", "2 calls hit anthropic's rate limit", "wait for the limit to pass", "1 check passes",
		"Claude allowance: 53% of the week used, starts again in 3d13h (seen 3m ago)",
		// Engines, with the difference between the file and the running proxy said.
		"ENGINES", "codex", "anthropic", "off", "yes", "The running proxy and the file differ. To make them match: aimux setup --apply",
		// Roles and subagent definitions.
		"subagent", "fast", "codex:gpt-6.2-sol", "chosen in the session", "scout", "codex (the subagent role)",
		// Model calls by role, with what the figures leave out.
		"MODEL CALLS", "425", "45", "~5.5M", "~45.0M", "134", "1.8M", "anthropic, codex",
		"3 of the calls counted carried no token figures.", "~ marks tokens that are partly estimated.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "answers on 127.0.0.1:7385") {
		t.Error("a passing check is listed; only its count belongs on the screen")
	}
}

// Before the first reading it says it is reading; a part that could not be read says why and the rest stands.
func TestSystemView_ReadingAndErrors(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	v := NewSystemView()
	v.SetSize(140, 200)
	v.SetNow(func() time.Time { return now })
	if out := v.View(); !strings.Contains(out, "Reading this machine") {
		t.Fatalf("before the first reading:\n%s", out)
	}
	snap := systemSnapshot(now)
	snap.DoctorErr, snap.Doctor = "read the model proxy key: the secret store is locked", controller.ProxyReport{}
	snap.EnginesErr, snap.Engines, snap.Roles, snap.Subagents = "config.yaml: line 3: not a mapping", nil, nil, nil
	snap.MeterErr, snap.Meter = "read the ledger: permission denied", controller.MeterReport{}
	v.SetSnapshot(snap)
	out := v.View()
	for _, want := range []string{"model proxy", "Could not be read: read the model proxy key: the secret store is locked",
		"Could not be read: config.yaml: line 3: not a mapping", "Could not be read: read the ledger: permission denied"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}

	// No ledger yet, and no calls in the time, are said apart.
	snap = systemSnapshot(now)
	snap.Meter = controller.MeterReport{}
	v.SetSnapshot(snap)
	if out := v.View(); !strings.Contains(out, "No ledger has been written on this machine yet.") {
		t.Errorf("no ledger:\n%s", out)
	}
	snap.Meter = controller.MeterReport{Files: 1}
	v.SetSnapshot(snap)
	if out := v.View(); !strings.Contains(out, "No model calls passed the proxy in the last day.") {
		t.Errorf("no calls:\n%s", out)
	}
}

// A screen shorter than the page scrolls, and never past either end.
func TestSystemView_Scrolls(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	v := NewSystemView()
	v.SetNow(func() time.Time { return now })
	v.SetSnapshot(systemSnapshot(now))
	v.SetSize(140, 200)
	all := strings.Split(strings.TrimRight(v.View(), "\n"), "\n")
	v.SetSize(140, 10)
	if got := strings.Split(strings.TrimRight(v.View(), "\n"), "\n"); len(got) != 10 || got[0] != all[0] {
		t.Fatalf("the top of a short screen: %d lines, first %q", len(got), got[0])
	}
	v.Scroll(3)
	if got := strings.Split(v.View(), "\n"); got[0] != all[3] {
		t.Errorf("after three lines down: %q, want %q", got[0], all[3])
	}
	v.Scroll(10_000)
	if got := strings.Split(strings.TrimRight(v.View(), "\n"), "\n"); got[len(got)-1] != all[len(all)-1] {
		t.Errorf("at the bottom: last line %q, want %q", got[len(got)-1], all[len(all)-1])
	}
	v.Scroll(-10_000)
	if got := strings.Split(v.View(), "\n"); got[0] != all[0] {
		t.Errorf("back at the top: %q", got[0])
	}
}

// Text that comes from files and services is shown as text: a control character in a subagent's definition or a
// service's answer never reaches the terminal.
func TestSystemView_ControlCharactersAreNotWritten(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	snap := systemSnapshot(now)
	snap.Subagents = []controller.SubagentRow{{Name: "sc\x1b[2Jout", DefinedIn: ".claude/agents", Model: "codex:\x1b]0;owned\agpt", RunsOn: "codex"}}
	snap.Doctor.Findings[1].Detail = "calls \x1b[31mfailed\x07"
	snap.Doctor.Allowance = []string{"Claude allowance: \x1b[2Jodd"}
	snap.Services.Services[0].Reason = "odd\x1b[0m\r\nreason"
	snap.EnginesErr = ""
	snap.Meter.Rows[0].Key = "ma\x1bin"
	v := NewSystemView()
	v.SetSize(140, 200)
	v.SetNow(func() time.Time { return now })
	v.SetSnapshot(snap)
	out := v.View()
	for _, bad := range []string{"\x1b", "\a", "\r"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q reached the screen:\n%q", bad, out)
		}
	}
	for _, want := range []string{"out", "gpt", "failed", "reason"} {
		if !strings.Contains(out, want) {
			t.Errorf("the text around a control character was lost: no %q", want)
		}
	}
}

// On a narrow terminal no line is wider than the screen, and a long finding wraps instead of being cut.
func TestSystemView_FitsANarrowTerminal(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	snap := systemSnapshot(now)
	snap.Doctor.Findings[1].Detail = "of 128 in the last hour: 5 calls to codex did not fit the model's context window; the session was told so, and compacts; 2 calls hit the rate limit at the very end"
	snap.Doctor.Findings[1].Fix = "in a session that stays stuck: /compact, or /model to a model with a larger window and then /compact again"
	v := NewSystemView()
	v.SetSize(60, 400)
	v.SetNow(func() time.Time { return now })
	v.SetSnapshot(snap)
	out := v.View()
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("%d columns wide: %q", w, line)
		}
	}
	for _, want := range []string{"at the very end", "/compact again"} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), want) {
			t.Errorf("a long line lost its end: no %q in:\n%s", want, out)
		}
	}
}

// Calls that could not be given a session are not said to be no calls.
func TestSystemView_UncountedCallsAreNotNoCalls(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	snap := systemSnapshot(now)
	snap.Meter = controller.MeterReport{Files: 1, Unmatched: 4}
	v := NewSystemView()
	v.SetSize(140, 200)
	v.SetNow(func() time.Time { return now })
	v.SetSnapshot(snap)
	out := v.View()
	if strings.Contains(out, "No model calls passed the proxy") || !strings.Contains(out, "Not in the table: 4 with no session.") {
		t.Errorf("four calls with no session:\n%s", out)
	}
}
