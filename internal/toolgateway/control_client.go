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

// PutLease puts l on the worker.
func (c ControlClient) PutLease(ctx context.Context, l Lease) error {
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
	out := make(map[string]Observed, len(wire))
	for name, o := range wire {
		ob := Observed{LiveSource: LiveSource{Tools: o.Tools}, Endpoint: o.Endpoint, RelistFailed: o.RelistFailed}
		if o.Err != "" {
			ob.Err = errors.New(o.Err)
		}
		out[name] = ob
	}
	return out, nil
}

// Status is the worker at a glance.
func (c ControlClient) Status(ctx context.Context) (ControlStatus, error) {
	var st ControlStatus
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &st)
	return st, err
}
