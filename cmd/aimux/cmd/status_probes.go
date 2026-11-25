package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/parts"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

type statusEnv struct {
	modelAddr, mcpAddr, gatewayAddr, collectorAddr string
	plistDir, cliVersion                           string
	mcpStatus                                      func() (mcpproxy.StatusReport, error)
	// Supply the control reader when gatewayAddr is the public address.
	gatewayStatus   func(context.Context) (toolgateway.ControlStatus, error)
	collectorHealth func(context.Context, string) (otel.ReceiverHealth, error)
}

var (
	errStatusKeyRejected  = errors.New("key rejected")
	errStatusWrongProcess = errors.New("wrong process")
	// errStatusNotCollector is the TUI's own OTLP receiver on the collector's port.
	errStatusNotCollector = errors.New("not the collector")
	// errStatusNoHealth is a 404 (or, for the model proxy, a 401) where a health answer should be: an aimux build
	// from before it had one.
	errStatusNoHealth = errors.New("no health answer")
)

func defaultStatusProbes(env statusEnv) []controller.StatusProbe {
	if env.gatewayStatus == nil {
		env.gatewayStatus = (toolgateway.ControlClient{Path: env.gatewayAddr}).Status
	}
	if env.collectorHealth == nil {
		env.collectorHealth = readCollectorHealth
	}
	// Names, labels and update commands come from the registry (internal/parts), the one list of them. This is
	// the order of the table. Where each is found and how it is asked stays its own: a service's address is not
	// always the registry's default port.
	model, mcp, gateway, collector := parts.MustService("model-proxy"), parts.MustService("mcp-proxy"), parts.MustService("tool-gateway"), parts.MustService("collector")
	return []controller.StatusProbe{
		env.probe(model.Name, env.modelAddr, model.Label, model.Update, env.readModelStatus),
		env.probe(mcp.Name, env.mcpAddr, mcp.Label, mcp.Update, env.readMCPStatus),
		// Its update command rewrites the worker's launchd job and restarts it, waiting until it answers.
		env.probe(gateway.Name, env.gatewayAddr, gateway.Label, gateway.Update, env.readGatewayStatus),
		env.probe(collector.Name, env.collectorAddr, collector.Label, collector.Update, env.readCollectorStatus),
	}
}

func (env statusEnv) probe(name, addr, label, restartFix string, read func(context.Context, *controller.ServiceStatus) error) controller.StatusProbe {
	return controller.StatusProbe{Name: name, Address: addr, RestartFix: restartFix, Run: func(ctx context.Context) controller.ServiceStatus {
		row := controller.ServiceStatus{Name: name, Address: addr, State: controller.ServiceOK, RestartFix: restartFix}
		err := read(ctx, &row)
		if err == nil {
			return row
		}
		row.State = controller.ServiceDown
		switch {
		case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, toolgateway.ErrGatewayNotRunning):
			if !env.installed(label) {
				row.State = controller.ServiceNotInstalled
			} else {
				row.Reason = "not running"
			}
		case errors.Is(err, errStatusKeyRejected):
			row.Reason = "key rejected"
		case errors.Is(err, errStatusNotCollector):
			row.Reason = "not the collector"
		case errors.Is(err, errStatusNoHealth) && env.installed(label):
			// The table turns a degraded row's reason into its attention line, with the restart as the fix.
			row.State, row.Reason = controller.ServiceDegraded, "no health answer"
		default:
			// Client errors may contain an address, response body or credentials.
			row.Reason = "wrong process"
		}
		return row
	}}
}

// installed reports whether the service's launchd job is on disk.
func (env statusEnv) installed(label string) bool {
	_, err := os.Stat(filepath.Join(env.plistDir, label+".plist"))
	return !errors.Is(err, os.ErrNotExist)
}

// readModelStatus reads the keyless /healthz, so the proxy key never goes to whatever listens on the port.
func (env statusEnv) readModelStatus(ctx context.Context, row *controller.ServiceStatus) error {
	live, err := readModelLiveness(ctx, env.modelAddr)
	if err != nil {
		return err
	}
	// A generic health endpoint can also return {"status":"ok"}.
	if live.Service != modelproxy.LivenessService || live.StartedAt.IsZero() {
		return errStatusWrongProcess
	}
	switch live.Status {
	case "ok":
	case "degraded":
		// The keyless answer carries no error text; the keyed command shows it.
		row.State, row.Reason = controller.ServiceDegraded, "degraded"
		row.Attention = []controller.Attention{{Problem: "degraded", Fix: "aimux model proxy status"}}
	default:
		return errStatusWrongProcess
	}
	row.Build, row.StartedAt = live.Version, statusTime(live.StartedAt)
	row.LastActivity, row.ActivityNote = live.LastOKAt, "turn"
	// The proxy answers the same with a ledger that loses lines; only the record is incomplete.
	switch {
	case live.LedgerOff:
		row.Attention = append(row.Attention, controller.Attention{Problem: "the ledger is off", Fix: "aimux model proxy status"})
	case live.LedgerFailed > 0:
		row.Attention = append(row.Attention, controller.Attention{
			Problem: plural(int(live.LedgerFailed), "ledger line was not written", "ledger lines were not written"), // #nosec G115 -- a small count
			Fix:     "aimux model proxy status",
		})
	}
	return nil
}

func readModelLiveness(ctx context.Context, addr string) (modelproxy.Liveness, error) {
	return challengeModelLiveness(ctx, addr, "")
}

// challengeModelLiveness reads the keyless health answer, asking the proxy to prove with nonce that it holds the
// key (modelproxy.CheckSessionProxy checks the proof). An empty nonce asks for no proof.
func challengeModelLiveness(ctx context.Context, addr, nonce string) (modelproxy.Liveness, error) {
	var live modelproxy.Liveness
	target := "http://" + addr + "/healthz"
	if nonce != "" {
		target += "?challenge=" + url.QueryEscape(nonce)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return live, err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return live, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&live)
		return live, err
	case http.StatusUnauthorized, http.StatusNotFound:
		// A proxy build from before /healthz checks the key first (401) or has no such route (404).
		return live, errStatusNoHealth
	default:
		return live, errStatusWrongProcess
	}
}

func (env statusEnv) readMCPStatus(ctx context.Context, row *controller.ServiceStatus) error {
	var rep mcpproxy.StatusReport
	var err error
	if env.mcpStatus != nil {
		rep, err = env.mcpStatus()
	} else {
		rep, err = readStatusMCPProxy(ctx, env.mcpAddr)
	}
	if err != nil {
		prefix := "MCP proxy on " + env.mcpAddr + " answered "
		if statusKeyRejected(err, prefix) {
			return errStatusKeyRejected
		}
		return err
	}
	if !rep.Running {
		return syscall.ECONNREFUSED
	}
	row.Build, row.StartedAt = rep.Version, statusTime(rep.StartedAt)
	row.ActivityNote = "call"
	var latest time.Time
	for _, srv := range rep.Servers {
		if srv.LastCall.After(latest) {
			latest = srv.LastCall
		}
	}
	row.LastActivity = statusTime(latest)
	// A server whose last start failed cannot answer a call. Idle and never-started ones are not failures.
	var failed []string
	for _, srv := range rep.Servers {
		if srv.State == mcpproxy.StateFailed {
			failed = append(failed, srv.Server)
		}
	}
	if len(failed) > 0 {
		slices.Sort(failed)
		row.State = controller.ServiceDegraded
		row.Attention = []controller.Attention{{
			Problem: fmt.Sprintf("%d of %d servers failed (%s)", len(failed), len(rep.Servers), strings.Join(failed, ", ")),
			Fix:     "aimux mcp proxy status",
		}}
	}
	// Match Installer's upgrade-pending check, including daemons predating Version.
	if env.cliVersion != "" && rep.Version != env.cliVersion {
		row.BuildNote = "restart pending"
	}
	return nil
}

func readStatusMCPProxy(ctx context.Context, addr string) (mcpproxy.StatusReport, error) {
	// The status command validates PID and start time and emits running:false only on refusal.
	var out bytes.Buffer
	if err := proxyStatus(ctx, proxyStatusOpts{addr: addr, json: true}, &out, time.Now); err != nil {
		return mcpproxy.StatusReport{}, err
	}
	var rep mcpproxy.StatusReport
	err := json.Unmarshal(out.Bytes(), &rep)
	return rep, err
}

func (env statusEnv) readGatewayStatus(ctx context.Context, row *controller.ServiceStatus) error {
	st, err := env.gatewayStatus(ctx)
	if err != nil {
		if statusKeyRejected(err, "tool gateway control: GET /v1/status: ") {
			return errStatusKeyRejected
		}
		return err
	}
	if st.StartedAt.IsZero() {
		return errStatusWrongProcess
	}
	// The gateway reports the snapshot it serves, not the build it runs, so Build stays empty.
	row.StartedAt = statusTime(st.StartedAt)
	row.ActivityNote = fmt.Sprintf("%d leases", st.Leases)
	if len(st.Down) > 0 {
		row.State = controller.ServiceDegraded
		names := mcpproxy.ScrubTokens(strings.Join(st.Down, ", "))
		row.Attention = []controller.Attention{{
			Problem: fmt.Sprintf("%d of %d backends down (%s)", len(st.Down), len(st.Backends), names),
			Fix:     "aimux mcp proxy status",
		}}
	}
	return nil
}

func (env statusEnv) readCollectorStatus(ctx context.Context, row *controller.ServiceStatus) error {
	health, err := env.collectorHealth(ctx, env.collectorAddr)
	if err != nil {
		return err
	}
	switch health.Service {
	case otel.CollectorService:
	case otel.ReceiverService:
		return errStatusNotCollector
	default:
		return errStatusWrongProcess
	}
	row.Build, row.StartedAt = health.Version, statusTime(health.StartedAt)
	row.LastActivity, row.ActivityNote = health.LastSpanAt, "span"
	return nil
}

func readCollectorHealth(ctx context.Context, addr string) (otel.ReceiverHealth, error) {
	var health otel.ReceiverHealth
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
	if err != nil {
		return health, err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return health, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return health, errStatusKeyRejected
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return health, err
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return health, errStatusNoHealth
		}
		err = json.Unmarshal(body, &health)
		return health, err
	case http.StatusNotFound:
		return health, errStatusNoHealth
	default:
		return health, errStatusWrongProcess
	}
}

// The existing clients return HTTP errors as text. Match the client-generated
// prefix and code so a response body mentioning a 401 or 403 cannot spoof one.
func statusKeyRejected(err error, prefix string) bool {
	status, ok := strings.CutPrefix(err.Error(), prefix)
	if !ok {
		return false
	}
	code, _, _ := strings.Cut(status, " ")
	code = strings.TrimSuffix(code, ":")
	return code == "401" || code == "403"
}

func statusTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
