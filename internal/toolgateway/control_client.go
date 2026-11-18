package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// ErrGatewayNotRunning is a control socket that is missing or refuses
// connections.
var ErrGatewayNotRunning = errors.New("the tool gateway is not running: run `aimux mcp proxy gateway install`")

// ControlClient talks to the gateway worker's control socket at Path.
type ControlClient struct {
	Path    string
	Timeout time.Duration // per request; 0: 5 s
}

func (c ControlClient) http() *http.Client {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	// One connection per request: a client lives for one call, so pooled
	// idle connections would only pile up in both processes.
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", c.Path)
		},
	}}
}

// do sends one request; out, when set, receives a 2xx JSON body.
func (c ControlClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://gateway"+path, body)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return ErrGatewayNotRunning
		}
		return fmt.Errorf("tool gateway control: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		var e struct{ Error, Message string }
		_ = json.NewDecoder(io.LimitReader(resp.Body, maxControlBody)).Decode(&e)
		if e.Error == "unknown_lease" {
			return fmt.Errorf("%w: %s", ErrUnknownLease, e.Message)
		}
		if e.Error == "relist_timeout" {
			return ErrRelistTimeout
		}
		if e.Error == "relist_discarded" {
			return ErrRelistDiscarded
		}
		if resp.StatusCode == http.StatusNotFound && e.Error == "" {
			return fmt.Errorf("%w: %s %s", errControlNoRoute, method, path)
		}
		return fmt.Errorf("tool gateway control: %s %s: %d %s", method, path, resp.StatusCode, e.Message)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxControlResponse)).Decode(out)
}

// maxControlResponse bounds a control response: Observed carries every
// backend's full tool schemas, far more than a request body.
const maxControlResponse = 64 << 20

func leasePath(session string) string { return "/v1/leases/" + url.PathEscape(session) }

// putLeaseTimeout covers what a put may wait for on the worker: a refresh to
// the lease's snapshot (leaseRefreshTimeout) and the fresh listing (5 s).
const putLeaseTimeout = leaseRefreshTimeout + 10*time.Second

// PutLease puts l on the worker.
func (c ControlClient) PutLease(ctx context.Context, l Lease) error {
	if c.Timeout < putLeaseTimeout {
		c.Timeout = putLeaseTimeout
	}
	return c.do(ctx, http.MethodPut, leasePath(l.Session), l, nil)
}

// Renew moves the session's lease expiry to until; ErrUnknownLease when the
// worker has no live lease for it.
func (c ControlClient) Renew(ctx context.Context, session string, until time.Time) error {
	return c.do(ctx, http.MethodPost, leasePath(session)+"/renew", map[string]time.Time{"until": until}, nil)
}

// DeleteLease ends the session's lease.
func (c ControlClient) DeleteLease(ctx context.Context, session string) error {
	return c.do(ctx, http.MethodDelete, leasePath(session), nil, nil)
}

// Observed is the worker's listings of its backends.
func (c ControlClient) Observed(ctx context.Context) (map[string]Observed, error) {
	var wire map[string]observedWire
	if err := c.do(ctx, http.MethodGet, "/v1/observed", nil, &wire); err != nil {
		return nil, err
	}
	return fromObservedWire(wire), nil
}

func fromObservedWire(wire map[string]observedWire) map[string]Observed {
	out := make(map[string]Observed, len(wire))
	for name, o := range wire {
		ob := Observed{LiveSource: LiveSource{Tools: o.Tools}, Endpoint: o.Endpoint, RelistFailed: o.RelistFailed}
		if o.Err != "" {
			ob.Err = errors.New(o.Err)
		}
		out[name] = ob
	}
	return out
}

// ErrRelistTimeout is a relist that did not finish before the caller's
// deadline: what the backend lists now is unknown.
var ErrRelistTimeout = errors.New("the tool gateway could not list the source afresh in time")

// ErrRelistUnsupported is a worker from an aimux before /v1/relist: it
// must be reinstalled from this binary.
var ErrRelistUnsupported = errors.New("the tool gateway worker runs an older aimux without a fresh relist")

// errControlNoRoute is a route the worker does not serve (an older aimux).
var errControlNoRoute = errors.New("the tool gateway worker does not serve this route")

// RelistResult is a relist's answer: the tool snapshot the worker listed
// under and what it then holds for the named servers.
type RelistResult struct {
	Version  string
	Observed map[string]Observed
}

// Relist has the worker apply the tool snapshot version (when newer than
// the one it serves) and list servers afresh, waiting until it has, or ctx
// ends (ErrRelistTimeout). What that relist saw comes back in the same
// answer, so a caller checks against it, never a cache.
func (c ControlClient) Relist(ctx context.Context, version string, servers []string) (RelistResult, error) {
	if dl, ok := ctx.Deadline(); ok {
		c.Timeout = time.Until(dl) + time.Second // the context ends it first
	}
	var out relistWire
	err := c.do(ctx, http.MethodPost, "/v1/relist", map[string]any{"servers": servers, "version": version}, &out)
	switch {
	case errors.Is(err, errControlNoRoute):
		return RelistResult{}, ErrRelistUnsupported
	case errors.Is(err, ErrRelistDiscarded):
		return RelistResult{}, err
	case err != nil && ctx.Err() != nil:
		return RelistResult{}, ErrRelistTimeout
	case err != nil:
		return RelistResult{}, err
	}
	return RelistResult{Version: out.Version, Observed: fromObservedWire(out.Observed)}, nil
}

// Status is the worker at a glance.
func (c ControlClient) Status(ctx context.Context) (ControlStatus, error) {
	var st ControlStatus
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &st)
	return st, err
}
