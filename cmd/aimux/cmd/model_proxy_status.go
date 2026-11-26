package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/parts"
)

// modelProxyStatus prints what the proxy at addr reports about itself and fails when it is unwell, so a monitor
// can act on the exit code alone. It asks for state the proxy already has: nothing here starts Codex or spends
// tokens.
// history is the proxy's request history file, read for the calls it passed on to other engines; empty reads none.
func modelProxyStatus(ctx context.Context, addr, key, history string, out io.Writer, now func() time.Time) error {
	health, err := readProxyHealth(ctx, addr, key)
	if err != nil {
		var dial *net.OpError
		if errors.As(err, &dial) && dial.Op == "dial" {
			_, _ = fmt.Fprintf(out, "Model proxy at %s is not running. Start it with: aimux model proxy serve\n", addr)
			return fmt.Errorf("the model proxy on %s is not running", addr)
		}
		return err
	}
	var passed []modelproxy.PassedOn
	if history != "" {
		// What it passed on since it started. A history that cannot be read leaves the row out; the health
		// answer above is the proxy's own word and does not depend on it.
		passed, _ = modelproxy.ReadPassedOn(history, health.StartedAt)
	}
	writeHealthTable(out, addr, health, passed, now())
	if health.Status != "ok" {
		return fmt.Errorf("the model proxy on %s is %s", addr, health.Status)
	}
	return nil
}

func readProxyHealth(ctx context.Context, addr, key string) (modelproxy.Health, error) {
	return readProxyHealthOver(ctx, nil, addr, key)
}

// readProxyHealthOver is readProxyHealth over transport (nil: the default one).
func readProxyHealthOver(ctx context.Context, transport http.RoundTripper, addr, key string) (modelproxy.Health, error) {
	var health modelproxy.Health
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
	if err != nil {
		return health, err
	}
	req.Header.Set("x-api-key", key)
	// Never follow a redirect: Go would resend the proxy key, and a redirect from this port means something
	// other than the proxy is listening on it.
	client := &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		return health, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return health, fmt.Errorf("read the model proxy's health: %w", err)
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusServiceUnavailable:
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return health, fmt.Errorf("%s redirected /health elsewhere, so it is not the model proxy", addr)
	case http.StatusUnauthorized:
		return health, fmt.Errorf("the model proxy on %s rejected the key from the %s secret-store entry", addr, modelproxy.KeyService)
	case http.StatusNotFound:
		return health, fmt.Errorf("the model proxy on %s has no /health: it is older than this aimux", addr)
	default:
		return health, fmt.Errorf("the model proxy on %s answered %d: %s", addr, res.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return health, fmt.Errorf("decode the model proxy's health: %w", err)
	}
	return health, nil
}

func writeHealthTable(out io.Writer, addr string, health modelproxy.Health, passed []modelproxy.PassedOn, now time.Time) {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	uptime := (time.Duration(health.UptimeSeconds) * time.Second).String()
	_, _ = fmt.Fprintf(table, "Model proxy\t%s\t%s, up %s\n", health.Status, addr, uptime)
	if health.Reason != "" {
		_, _ = fmt.Fprintf(table, "Problem\t\t%s\n", health.Reason)
	}
	codex := "not started"
	if health.Codex.AppServerRunning {
		codex = "running"
	}
	_, _ = fmt.Fprintf(table, "Codex\t%s\t%s\n", codex, health.Codex.Command)
	// The proxy's own counter knows only Codex's turns; what went to another engine is in the request history.
	_, _ = fmt.Fprintf(table, "Codex turns\t\t%s\n", stageLine(health.Requests, "turn", now))
	if len(passed) > 0 {
		total := 0
		var each []string
		for _, p := range passed {
			total += p.Count
			one := fmt.Sprintf("%s %d", p.Engine, p.Count)
			if p.Failed > 0 {
				one += fmt.Sprintf(" (%d failed)", p.Failed)
			}
			each = append(each, one)
		}
		_, _ = fmt.Fprintf(table, "Passed on\t\t%s since it started: %s\n", plural(total, "call", "calls"), strings.Join(each, ", "))
	}
	_, _ = fmt.Fprintf(table, "Codex check\t\t%s\n", stageLine(health.Probe, "read", now))
	if line := ledgerLine(health.Ledger); line != "" {
		_, _ = fmt.Fprintf(table, "Ledger\t\t%s\n", line)
	}
	for _, a := range health.Allowance {
		name := a.Engine
		if a.Engine == parts.EngineAnthropic {
			name = "Claude"
		}
		_, _ = fmt.Fprintf(table, "%s allowance\t\t%s\n", name, allowanceLine(a, now))
	}
	_ = table.Flush()
}

// ledgerLine says how the proxy's record of its model calls is doing; empty for a proxy that keeps none.
func ledgerLine(l modelproxy.LedgerHealth) string {
	switch {
	case l.Off:
		return "off: the ledger file could not be opened at start (see the proxy's log); model calls are not recorded"
	case l.Seq == 0:
		return ""
	case l.Failed > 0:
		return fmt.Sprintf("latest line %d, %d not written", l.Seq, l.Failed)
	default:
		return fmt.Sprintf("latest line %d", l.Seq)
	}
}

// stageLine reads as a sentence: how much work (unit is its singular), how much of it failed, and how long ago
// each last happened.
func stageLine(stage modelproxy.StageHealth, unit string, now time.Time) string {
	parts := []string{fmt.Sprintf("%s, %d failed", plural(stage.Total, unit, unit+"s"), stage.Failed)}
	if stage.LastOKAt != nil {
		parts = append(parts, "last ok "+since(now, *stage.LastOKAt))
	}
	if stage.LastErrorAt != nil {
		parts = append(parts, fmt.Sprintf("last error %s: %s", since(now, *stage.LastErrorAt), stage.LastError))
	}
	return strings.Join(parts, ", ")
}

func since(now, then time.Time) string {
	elapsed := now.Sub(then).Round(time.Second)
	if elapsed < time.Second {
		return "just now"
	}
	return elapsed.String() + " ago"
}

// allowanceLine says a plan's allowance as an engine's last answer gave it: the share used and when the window
// starts again, in the plan's own word when it refuses, and how old the figure is. A figure the answer did not
// give is left out, never shown as nothing used.
func allowanceLine(a modelproxy.EngineAllowance, now time.Time) string {
	window := map[string]string{"seven_day": "the week", "five_hour": "the five hours"}[a.Window]
	if window == "" {
		window = "the plan's window"
	}
	allowed := a.Status == "allowed" || a.Status == "allowed_warning"
	var said []string
	switch {
	case !allowed:
		said = append(said, "the plan refuses requests ("+printable(a.Status)+")")
	case a.Used >= 0:
		said = append(said, fmt.Sprintf("%.0f%% of %s used", a.Used*100, window))
	default:
		said = append(said, "allowed; the share used was not given")
	}
	if a.Used >= 0 && !allowed {
		said = append(said, fmt.Sprintf("%.0f%% of %s used", a.Used*100, window))
	}
	if left := a.ResetsAt.Sub(now); !a.ResetsAt.IsZero() && left > 0 {
		said = append(said, "starts again in "+spanText(left))
	}
	return strings.Join(said, ", ") + " (seen " + spanText(now.Sub(a.SeenAt)) + " ago)"
}

// spanText is a length of time in its two largest parts: 2d2h, 3h05m, 4m.
func spanText(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
