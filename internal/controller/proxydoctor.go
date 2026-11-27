package controller

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// How bad a finding is.
const (
	FindingOK   = "ok"
	FindingWarn = "warn" // it works, and something needs attention
	FindingFail = "fail" // sessions that go through the proxy are, or will be, refused
)

// What answered on the proxy's port.
const (
	ProxyAnswers       = "aimux"
	ProxyOtherListener = "other"
	ProxyNoAnswer      = "none"
)

// ProxyFacts is what was read about one model proxy, for DiagnoseProxy to judge. It holds no secret: whether a
// key is stored and proven, never the key.
type ProxyFacts struct {
	Port     int
	Now      time.Time
	CLIBuild string

	Answers string // ProxyAnswers, ProxyOtherListener or ProxyNoAnswer
	Build   string
	Status  string // "ok" or "degraded", as the proxy says
	Reason  string
	Engines []string // the engines it answers with

	KeyStored bool
	KeyProven *bool // whether the listener proved it holds the stored key; nil when it was not asked
	// ProofUnsupported: the proxy is a build from before it could prove it holds the key.
	ProofUnsupported bool

	// HealthRead: the keyed health answer was read, so the Codex and ledger facts below are known.
	HealthRead bool
	// HealthError is why the keyed health answer could not be read, from a proxy that proved it holds the key.
	HealthError       string
	CodexRunning      bool
	CodexCheckOKAt    *time.Time
	CodexCheckErrorAt *time.Time
	CodexCheckError   string
	LedgerOff         bool
	LedgerFailed      uint64

	// Stated is what the config file says of each engine it names; nil when it names none.
	Stated map[string]bool
	// ConfigProblem is why the config file's engines could not be read; empty when they could.
	ConfigProblem string

	// Calls is how many requests the proxy answered in the window read, and Failures those that failed, grouped.
	Calls    int
	Failures []CallFailure
	// HistoryError is why the request history could not be read; HistoryIncomplete says part of it could not.
	HistoryError      string
	HistoryIncomplete bool

	// The shell the command runs in.
	ShellThrough  bool
	ShellEngine   string
	ShellEngineOn *bool
}

// CallFailure is one kind of failed request: who was asked, what came back, how often.
type CallFailure struct {
	Engine string
	Status int    // the upstream's HTTP status, 0 when there was none
	Error  string // the proxy's own word for it
	Count  int
	LastAt time.Time
}

// ProxyFinding is one thing checked: how it is, and what to run when it is not right.
type ProxyFinding struct {
	ID     string `json:"id"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// ProxyReport is a diagnosis as a frontend shows it: the port that was asked, the findings, and the worst level
// among them.
type ProxyReport struct {
	SchemaVersion int            `json:"schema_version"`
	Port          int            `json:"port"`
	Verdict       string         `json:"verdict"`
	Findings      []ProxyFinding `json:"findings"`
}

// codexCheckStale is how old the last good Codex check may be. The proxy checks every two minutes.
const codexCheckStale = 15 * time.Minute

// DiagnoseProxy judges what was read about a model proxy: one finding per thing that breaks in practice, each
// with the command that fixes it. When nothing answers, only what can be known without an answer is reported.
func DiagnoseProxy(f ProxyFacts) []ProxyFinding {
	var out []ProxyFinding
	add := func(id, level, detail, fix string) {
		out = append(out, ProxyFinding{ID: id, Level: level, Detail: detail, Fix: fix})
	}
	addr := fmt.Sprintf("127.0.0.1:%d", f.Port)
	switch f.Answers {
	case ProxyNoAnswer:
		add("service", FindingFail, "nothing answers on "+addr, "aimux setup --apply (starts it when its part is on), or by hand: aimux model proxy serve")
	case ProxyOtherListener:
		add("service", FindingFail, "something answers on "+addr+" and it is not the model proxy", "stop what listens there, or move the proxy: set port under parts.inference-proxy, then aimux setup --apply")
	default:
		switch {
		case f.Status != "ok" && f.Reason != "":
			add("service", FindingWarn, "answers on "+addr+", "+f.Status+": "+f.Reason, "")
		case f.Status != "ok" && f.KeyProven != nil && *f.KeyProven:
			add("service", FindingWarn, "answers on "+addr+", "+f.Status, "aimux model proxy status")
		case f.Status != "ok":
			// That command sends the key, so it is named only for a listener that proved it holds it.
			add("service", FindingWarn, "answers on "+addr+", "+f.Status, "")
		default:
			add("service", FindingOK, "answers on "+addr, "")
		}
	}
	answers := f.Answers == ProxyAnswers
	if answers {
		row := ServiceStatus{Build: f.Build}
		if problem, older := olderBuild(&row, f.CLIBuild); older {
			add("build", FindingWarn, "its "+problem+", so it lacks what was fixed since", "aimux upgrade model-proxy")
		} else {
			add("build", FindingOK, "build "+f.Build, "")
		}
	}
	switch {
	case !f.KeyStored:
		add("key", FindingFail, "no proxy key is stored yet", "start the proxy once, which creates it: aimux model proxy serve")
	case f.ProofUnsupported:
		add("key", FindingWarn, "a proxy key is stored; this proxy is too old a build, so it cannot prove it holds it", "aimux upgrade model-proxy")
	case f.KeyProven != nil && !*f.KeyProven:
		add("key", FindingFail, "what listens on "+addr+" does not hold the stored proxy key, so it is given nothing", "")
	case f.KeyProven != nil:
		add("key", FindingOK, "the proxy holds the stored key", "")
	default:
		add("key", FindingOK, "a proxy key is stored", "")
	}
	if answers {
		// The ledger is in the keyless answer too; Codex is only in the keyed one.
		switch {
		case f.HealthRead:
			out = append(out, codexFinding(f))
		case f.HealthError != "":
			add("codex", FindingWarn, "the proxy's detailed health could not be read ("+f.HealthError+"), so the Codex login is not known", "aimux model proxy status")
		}
		out = append(out, ledgerFinding(f), enginesFinding(f))
	}
	out = append(out, callsFinding(f), shellFinding(f))
	return out
}

func codexFinding(f ProxyFacts) ProxyFinding {
	failing := f.CodexCheckErrorAt != nil && (f.CodexCheckOKAt == nil || f.CodexCheckErrorAt.After(*f.CodexCheckOKAt))
	switch {
	case failing:
		return ProxyFinding{ID: "codex", Level: FindingFail, Detail: "the Codex check fails: " + f.CodexCheckError,
			Fix: "codex login, when it says the login is the trouble"}
	case f.CodexCheckOKAt == nil:
		return ProxyFinding{ID: "codex", Level: FindingOK, Detail: "no Codex check has run on this proxy yet; a turn through it is the proof"}
	case f.Now.Sub(*f.CodexCheckOKAt) > codexCheckStale:
		return ProxyFinding{ID: "codex", Level: FindingWarn, Detail: "the last good Codex check was " + agoText(f.Now.Sub(*f.CodexCheckOKAt)) + " ago, and it runs every two minutes"}
	}
	detail := "the Codex login works, checked " + agoText(f.Now.Sub(*f.CodexCheckOKAt)) + " ago"
	if !f.CodexRunning {
		detail += "; its app-server is not running now and starts with the next turn"
	}
	return ProxyFinding{ID: "codex", Level: FindingOK, Detail: detail}
}

func ledgerFinding(f ProxyFacts) ProxyFinding {
	switch {
	case f.LedgerOff:
		return ProxyFinding{ID: "ledger", Level: FindingFail, Detail: "the ledger is not being written: its file could not be opened when the proxy started, so no call is recorded",
			Fix: "see the proxy's own words in ~/.aimux/logs/model-proxy/stderr.log, then restart it: aimux upgrade model-proxy"}
	case f.LedgerFailed > 0:
		return ProxyFinding{ID: "ledger", Level: FindingWarn, Detail: fmt.Sprintf("%d ledger %s could not be written; aimux cost reports the gap", f.LedgerFailed, pluralWord(int(f.LedgerFailed), "line", "lines"))} // #nosec G115 -- a small count
	}
	return ProxyFinding{ID: "ledger", Level: FindingOK, Detail: "every call is recorded"}
}

func enginesFinding(f ProxyFacts) ProxyFinding {
	if f.ConfigProblem != "" {
		return ProxyFinding{ID: "engines", Level: FindingWarn, Detail: "the engines in the config file could not be read: " + f.ConfigProblem, Fix: "aimux setup --check"}
	}
	var differ []string
	for _, name := range slices.Sorted(maps.Keys(f.Stated)) {
		running := slices.Contains(f.Engines, name)
		switch {
		case f.Stated[name] && !running:
			differ = append(differ, name+" is on in the config file and not running")
		case !f.Stated[name] && running:
			differ = append(differ, name+" is off in the config file and still running")
		}
	}
	if len(differ) > 0 {
		return ProxyFinding{ID: "engines", Level: FindingWarn, Detail: strings.Join(differ, "; "), Fix: "aimux setup --apply"}
	}
	if len(f.Engines) == 0 {
		return ProxyFinding{ID: "engines", Level: FindingOK, Detail: "answers with Codex (a build from before it listed its engines)"}
	}
	return ProxyFinding{ID: "engines", Level: FindingOK, Detail: "answers with " + strings.Join(f.Engines, ", ")}
}

func callsFinding(f ProxyFacts) ProxyFinding {
	if f.HistoryError != "" {
		return ProxyFinding{ID: "calls", Level: FindingWarn, Detail: "the request history could not be read, so how the calls went is not known: " + f.HistoryError}
	}
	// A turn the owner stopped is counted apart: it is no failure of the proxy.
	stopped := 0
	var failed []CallFailure
	for _, x := range f.Failures {
		if x.Error == "client_gone" {
			stopped += x.Count
			continue
		}
		failed = append(failed, x)
	}
	tail := ""
	if stopped > 0 {
		tail = fmt.Sprintf("; %d stopped by the session", stopped)
	}
	if f.HistoryIncomplete {
		tail += "; not all of the history could be read"
	}
	if len(failed) == 0 {
		level := FindingOK
		if f.HistoryIncomplete {
			level = FindingWarn
		}
		if f.Calls == 0 {
			return ProxyFinding{ID: "calls", Level: level, Detail: "no calls in the last hour" + tail}
		}
		return ProxyFinding{ID: "calls", Level: level, Detail: fmt.Sprintf("%d %s in the last hour, none failed%s", f.Calls, pluralWord(f.Calls, "call", "calls"), tail)}
	}
	level, fix := FindingWarn, ""
	var parts []string
	for _, x := range failed {
		who := x.Engine
		if who == "" {
			who = "the engine"
		}
		n := fmt.Sprintf("%d %s", x.Count, pluralWord(x.Count, "call", "calls"))
		switch {
		case x.Status == 429:
			parts = append(parts, fmt.Sprintf("%s hit %s's rate limit (the plan's limit, not the proxy; the session retries)", n, who))
		case x.Error == "context_too_long":
			parts = append(parts, fmt.Sprintf("%s to %s did not fit the model's context window; the session was told so, and compacts", n, who))
			if fix == "" {
				fix = "in a session that stays stuck: /compact, or /model to a model with a larger window and then /compact"
			}
		case x.Status == 401 || x.Status == 403:
			level = FindingFail
			parts = append(parts, fmt.Sprintf("%s: %s refused the login (%d)", n, who, x.Status))
			fix = "in a session: /login; for Codex: codex login"
		case x.Error != "" && x.Error != "upstream_error":
			// The proxy's own word for a call that got no answer from the engine; the 502 the session saw is the
			// proxy's, not the engine's.
			level = FindingFail
			parts = append(parts, fmt.Sprintf("%s to %s got no answer: %s", n, who, x.Error))
		case x.Status >= 500:
			parts = append(parts, fmt.Sprintf("%s: %s answered %d, its own failure", n, who, x.Status))
		default:
			parts = append(parts, fmt.Sprintf("%s: %s refused the request (%d)", n, who, x.Status))
		}
	}
	return ProxyFinding{ID: "calls", Level: level, Fix: fix,
		Detail: fmt.Sprintf("of %d in the last hour: %s%s", f.Calls, strings.Join(parts, "; "), tail)}
}

func shellFinding(f ProxyFacts) ProxyFinding {
	switch {
	case !f.ShellThrough:
		return ProxyFinding{ID: "shell", Level: FindingOK, Detail: "sessions started from this shell do not go through the proxy, so it does not see them"}
	case f.ShellEngineOn != nil && !*f.ShellEngineOn:
		return ProxyFinding{ID: "shell", Level: FindingFail, Detail: "sessions started from this shell need the " + f.ShellEngine + " engine, which this proxy does not answer with",
			Fix: "aimux engine enable " + f.ShellEngine}
	}
	return ProxyFinding{ID: "shell", Level: FindingOK, Detail: "sessions started from this shell go through this proxy, on " + f.ShellEngine}
}

// ProxyVerdict is the worst level among the findings.
func ProxyVerdict(findings []ProxyFinding) string {
	verdict := FindingOK
	for _, f := range findings {
		switch f.Level {
		case FindingFail:
			return FindingFail
		case FindingWarn:
			verdict = FindingWarn
		}
	}
	return verdict
}

func agoText(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
