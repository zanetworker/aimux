package modelproxy

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Allowance is what an engine's answer says of the plan behind the credential that carried the request: whether
// the plan allows it, which window binds, how much of it is used and when it starts again. It holds the plan's
// own words and numbers, and nothing that names the account.
type Allowance struct {
	// Status is the plan's word: "allowed" while a request is let through.
	Status string `json:"status"`
	// Window is the limit that binds: "seven_day", "five_hour".
	Window string `json:"window,omitempty"`
	// Used is the share of that window used, from 0 to 1; -1 when the answer did not say.
	Used float64 `json:"used"`
	// ResetsAt is when the window starts again; zero when the answer did not say.
	ResetsAt time.Time `json:"resets_at"`
}

// The headers Anthropic sends with every answer on a subscription login.
const (
	unifiedStatus = "anthropic-ratelimit-unified-status"
	unifiedClaim  = "anthropic-ratelimit-unified-representative-claim"
	unifiedReset  = "anthropic-ratelimit-unified-reset"
)

// planWord is a word of the plan's vocabulary: allowed, allowed_warning, rejected, seven_day.
var planWord = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// windowHeader is the short name a window has in its own headers.
var windowHeader = map[string]string{"seven_day": "7d", "five_hour": "5h"}

// allowanceOf reads the plan's allowance from an answer's headers. ok is false when the answer does not carry
// the plan's word at all, which is not the same as allowed, and not the same as nothing used.
func allowanceOf(header http.Header) (Allowance, bool) {
	// The plan's words are kept only when they are plain words: an upstream's header is not trusted to hold
	// nothing else, and what is kept here is shown again in the health answer.
	status := header.Get(unifiedStatus)
	if !planWord.MatchString(status) {
		return Allowance{}, false
	}
	a := Allowance{Status: status, Used: -1}
	if window := header.Get(unifiedClaim); planWord.MatchString(window) {
		a.Window = window
	}
	if short, known := windowHeader[a.Window]; known {
		// A share is a number from nothing used to somewhat over the whole; anything else is not one.
		if used, err := strconv.ParseFloat(header.Get("anthropic-ratelimit-unified-"+short+"-utilization"), 64); err == nil && used >= 0 && used <= 10 {
			a.Used = used
		}
	}
	if seconds, err := strconv.ParseInt(header.Get(unifiedReset), 10, 64); err == nil && seconds > 0 {
		a.ResetsAt = time.Unix(seconds, 0).UTC()
	}
	return a, true
}

// What kind of limit a failed call met.
const (
	limitNone  = ""
	limitShort = "short" // a rate limit of the moment; the harness sends the request again by itself
	limitPlan  = "plan"  // the plan's allowance is used up until its window starts again
)

// limitKind tells a plan limit from a short one, by the plan's own word and not by the status alone: a 429 is
// the plan's only when the plan says the request is not allowed. What that word is when it refuses has not been
// seen, so anything other than "allowed" counts, and the word itself is kept in the allowance.
func limitKind(status int, a Allowance, known bool) string {
	switch {
	case status != http.StatusTooManyRequests:
		return limitNone
	case known && !planAllows(a.Status):
		return limitPlan
	}
	return limitShort
}

// planAllows reports whether the plan's word lets requests through: allowed, and allowed with a warning that the
// window is nearly used.
func planAllows(status string) bool { return status == "allowed" || status == "allowed_warning" }

// EngineAllowance is the allowance last seen in an engine's answers.
type EngineAllowance struct {
	Engine string `json:"engine"`
	Allowance
	// SeenAt is when that answer came. The figure is as old as that, and is the plan of whoever sent that
	// request: on the owner's machine, his.
	SeenAt time.Time `json:"seen_at"`
}

// allowances keeps the latest allowance per engine, in memory only.
type allowances struct {
	mu   sync.Mutex
	seen map[string]EngineAllowance
}

func (b *allowances) note(engine string, a Allowance, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen == nil {
		b.seen = map[string]EngineAllowance{}
	}
	b.seen[engine] = EngineAllowance{Engine: engine, Allowance: a, SeenAt: at}
}

func (b *allowances) list() []EngineAllowance {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]EngineAllowance, 0, len(b.seen))
	for _, a := range b.seen {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Engine < out[j].Engine })
	return out
}

// OnLimitReport is the one thing the proxy can be told to do when a plan limit comes back, in this build: write
// what a fallback would have done, and change nothing.
const OnLimitReport = "report"

// WithOnLimit sets what the proxy does when a plan limit comes back. Only OnLimitReport does anything.
func WithOnLimit(mode string) HandlerOption {
	return func(c *handlerConfig) { c.onLimit = mode }
}

// onLimitReport says what a fallback would have done for a call that met a plan limit: where it would have gone,
// or why it would not have. It decides nothing and sends nothing. The two conditions are the ones a real
// fallback will need first: the call opens a turn, so no tool's result is waiting to be answered, and it
// carried the proxy key, since the other engine spends the owner's allowance.
func onLimitReport(facts CallFacts, withProxyKey bool) string {
	switch {
	case !facts.StartsTurn:
		return "would_not:not_a_fresh_turn"
	case !withProxyKey:
		return "would_not:no_proxy_key"
	}
	return "would:" + engineCodex
}
