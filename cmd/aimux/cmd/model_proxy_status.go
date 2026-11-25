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
)

// modelProxyStatus prints what the proxy at addr reports about itself and fails when it is unwell, so a monitor
// can act on the exit code alone. It asks for state the proxy already has: nothing here starts Codex or spends
// tokens.
func modelProxyStatus(ctx context.Context, addr, key string, out io.Writer, now func() time.Time) error {
	health, err := readProxyHealth(ctx, addr, key)
	if err != nil {
		var dial *net.OpError
		if errors.As(err, &dial) && dial.Op == "dial" {
			_, _ = fmt.Fprintf(out, "Model proxy at %s is not running. Start it with: aimux model proxy serve\n", addr)
			return fmt.Errorf("the model proxy on %s is not running", addr)
		}
		return err
	}
	writeHealthTable(out, addr, health, now())
	if health.Status != "ok" {
		return fmt.Errorf("the model proxy on %s is %s", addr, health.Status)
	}
	return nil
}

func readProxyHealth(ctx context.Context, addr, key string) (modelproxy.Health, error) {
	var health modelproxy.Health
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
	if err != nil {
		return health, err
	}
	req.Header.Set("x-api-key", key)
	// Never follow a redirect: Go would resend the proxy key, and a redirect from this port means something
	// other than the proxy is listening on it.
	client := &http.Client{
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

func writeHealthTable(out io.Writer, addr string, health modelproxy.Health, now time.Time) {
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
	_, _ = fmt.Fprintf(table, "Requests\t\t%s\n", stageLine(health.Requests, "turn", now))
	_, _ = fmt.Fprintf(table, "Codex check\t\t%s\n", stageLine(health.Probe, "read", now))
	if line := ledgerLine(health.Ledger); line != "" {
		_, _ = fmt.Fprintf(table, "Ledger\t\t%s\n", line)
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
