package modelproxy

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/ledger"
)

// The headers of a real answer on a subscription login, as recorded on 2026-11-26.
func realAllowanceHeaders() http.Header {
	h := http.Header{}
	for name, value := range map[string]string{
		"anthropic-ratelimit-unified-status":                  "allowed",
		"anthropic-ratelimit-unified-representative-claim":    "seven_day",
		"anthropic-ratelimit-unified-7d-utilization":          "0.36",
		"anthropic-ratelimit-unified-7d-status":               "allowed",
		"anthropic-ratelimit-unified-7d-reset":                "1796094000",
		"anthropic-ratelimit-unified-reset":                   "1796094000",
		"anthropic-ratelimit-unified-fallback-percentage":     "0.5",
		"anthropic-ratelimit-unified-overage-status":          "rejected",
		"anthropic-ratelimit-unified-overage-disabled-reason": "org_level_disabled",
	} {
		h.Set(name, value)
	}
	return h
}

func TestAllowanceOf(t *testing.T) {
	got, ok := allowanceOf(realAllowanceHeaders())
	want := Allowance{Status: "allowed", Window: "seven_day", Used: 0.36, ResetsAt: time.Unix(1796094000, 0).UTC()}
	if !ok || got != want {
		t.Fatalf("allowance = %+v, %v", got, ok)
	}
	// The five-hour window, when that is the one that binds.
	five := http.Header{}
	five.Set("anthropic-ratelimit-unified-status", "allowed_warning")
	five.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
	five.Set("anthropic-ratelimit-unified-5h-utilization", "0.91")
	five.Set("anthropic-ratelimit-unified-7d-utilization", "0.40")
	if got, ok := allowanceOf(five); !ok || got.Window != "five_hour" || got.Used != 0.91 || got.Status != "allowed_warning" || !got.ResetsAt.IsZero() {
		t.Errorf("five hour = %+v, %v", got, ok)
	}
	// An answer without the plan's word says nothing, and nothing is made up: not zero used, not allowed.
	if got, ok := allowanceOf(http.Header{"Retry-After": {"3"}}); ok || got != (Allowance{}) {
		t.Errorf("no headers = %+v, %v", got, ok)
	}
	// Words that are not the plan's are not kept: what is kept is shown again in the health answer.
	for _, word := range []string{"sk-ant-secret-key", "allowed; drop table", "ALLOWED", strings.Repeat("a", 64)} {
		h := realAllowanceHeaders()
		h.Set("anthropic-ratelimit-unified-status", word)
		if got, ok := allowanceOf(h); ok || got != (Allowance{}) {
			t.Errorf("status %q kept: %+v", word, got)
		}
	}
	for _, used := range []string{"NaN", "+Inf", "-0.5", "1e9"} {
		h := realAllowanceHeaders()
		h.Set("anthropic-ratelimit-unified-7d-utilization", used)
		if got, _ := allowanceOf(h); got.Used != -1 {
			t.Errorf("a share of %q read as %v", used, got.Used)
		}
	}
	// A figure that is not one is left out, not read as zero.
	odd := realAllowanceHeaders()
	odd.Set("anthropic-ratelimit-unified-7d-utilization", "lots")
	odd.Set("anthropic-ratelimit-unified-reset", "soon")
	if got, _ := allowanceOf(odd); got.Used != -1 || !got.ResetsAt.IsZero() {
		t.Errorf("odd figures = %+v", got)
	}
}

func TestLimitKind(t *testing.T) {
	allowed, _ := allowanceOf(realAllowanceHeaders())
	refused := allowed
	refused.Status = "rejected"
	for name, tc := range map[string]struct {
		status int
		a      Allowance
		known  bool
		want   string
	}{
		"a success":                                  {200, allowed, true, limitNone},
		"a 429 while the plan still allows":          {429, allowed, true, limitShort},
		"a 429 with no word from the plan":           {429, Allowance{}, false, limitShort},
		"a 429 and the plan says no":                 {429, refused, true, limitPlan},
		"a 429 while the plan allows with a warning": {429, Allowance{Status: "allowed_warning"}, true, limitShort},
		"another failure, whatever the plan says":    {500, refused, true, limitNone},
		"a refused login is not a limit":             {401, refused, true, limitNone},
	} {
		if got := limitKind(tc.status, tc.a, tc.known); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// The allowance in an answer reaches the session untouched and is then in the proxy's health answer; a limit is
// recorded on the failed call's ledger line as the kind it was.
func TestHandler_AllowanceAndLimitAreRecorded(t *testing.T) {
	status, refusing := http.StatusOK, false
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		for name, values := range realAllowanceHeaders() {
			w.Header()[name] = values
		}
		if refusing {
			w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
		}
		w.Header().Set("Content-Type", jsonType)
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write(anthropicFixture(t, "message.json"))
		} else {
			_, _ = w.Write(anthropicFixture(t, "error.json"))
		}
	})
	backend, _ := newFakeBackend(t)
	dir := filepath.Join(t.TempDir(), "ledger")
	writer, err := ledger.OpenWriter(dir, LedgerName(StablePort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	engine, err := WithAnthropic(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := twoEngines{ledgerProxy: ledgerProxy{NewHandler(backend, testKey, WithLedger(writer), engine), dir, writer}}
	// Before any answer, nothing is known, and it is not shown as nothing used.
	if h := readHealth(t, p.handler, http.StatusOK); len(h.Allowance) != 0 {
		t.Fatalf("before any call: %+v", h.Allowance)
	}
	res := p.send(t, "/v1/messages", "claude-sonnet-5-5", "0b61f1d5-allowance", "hello", map[string]string{"Authorization": callersLogin})
	if res.Code != http.StatusOK || res.Header().Get("anthropic-ratelimit-unified-7d-utilization") != "0.36" {
		t.Fatalf("HTTP %d, headers %v", res.Code, res.Header())
	}
	h := readHealth(t, p.handler, http.StatusOK)
	if len(h.Allowance) != 1 || h.Allowance[0].Engine != engineAnthropic || h.Allowance[0].Used != 0.36 || h.Allowance[0].Window != "seven_day" || h.Allowance[0].SeenAt.IsZero() {
		t.Fatalf("health allowance = %+v", h.Allowance)
	}
	// A short rate limit, then the plan's own refusal.
	status = http.StatusTooManyRequests
	p.send(t, "/v1/messages", "claude-sonnet-5-5", "0b61f1d5-allowance", "again", map[string]string{"Authorization": callersLogin})
	refusing = true
	p.send(t, "/v1/messages", "claude-sonnet-5-5", "0b61f1d5-allowance", "and again", map[string]string{"Authorization": callersLogin})
	lines := p.lines(t)
	if len(lines) != 3 || lines[0].Limit != "" || lines[1].Limit != limitShort || lines[2].Limit != limitPlan || lines[2].Status != 429 {
		t.Fatalf("limits on the ledger lines: %q %q %q", lines[0].Limit, lines[1].Limit, lines[2].Limit)
	}
	if h := readHealth(t, p.handler, http.StatusOK); h.Allowance[0].Status != "rejected" {
		t.Errorf("after the refusal: %+v", h.Allowance)
	}
}
