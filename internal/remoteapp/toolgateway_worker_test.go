package remoteapp

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

type observedWorker struct{}

func (observedWorker) PutLease(context.Context, toolgateway.Lease) error { return nil }
func (observedWorker) Renew(string, time.Time) error                     { return nil }
func (observedWorker) DeleteLease(string)                                {}
func (observedWorker) Status() toolgateway.ControlStatus                 { return toolgateway.ControlStatus{} }
func (observedWorker) Observed() map[string]toolgateway.Observed {
	return map[string]toolgateway.Observed{"fake": {LiveSource: toolgateway.LiveSource{Tools: map[string]json.RawMessage{
		"search": json.RawMessage(`{"name":"search","inputSchema":{"type":"object"}}`)}}, Endpoint: "http://fake/"}}
}

// shortHome is a home under /tmp: the control socket path must stay under
// macOS's 104-byte limit.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

// GET /v1/tools reads the gateway's listings from the worker over its
// control socket; without a worker every source is unknown.
func TestToolsView_ObservedFromWorker(t *testing.T) {
	home := shortHome(t)
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{"fake": {Type: toolreg.TypeMCPHTTP, URL: "http://fake/"}},
		Sets:    map[string]toolreg.Set{"fake:readonly": {Source: "fake", Tools: []string{"search"}}}}
	if err := toolreg.WriteNew(toolreg.DefaultPath(home), reg); err != nil {
		t.Fatal(err)
	}
	tg := newToolGateway(home, log.New(io.Discard, "", 0))
	if v := tg.tools(); len(v.Live) != 0 {
		t.Fatalf("no worker: live %v, want none (unknown)", v.Live)
	}
	ln, err := toolgateway.ListenControl(toolgateway.ControlSocketPath(home))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = toolgateway.ServeControl(ctx, ln, observedWorker{}) }()
	v := tg.tools()
	if _, ok := v.Live["fake"].Tools["search"]; !ok {
		t.Fatalf("live %v, want fake/search from the worker", v.Live)
	}
}

// A turn's leases go to the worker's control socket under home, and the
// sandbox reaches the gateway at its fixed URL; remote serve opens no port.
func TestToolGateway_LeasesGoToTheWorker(t *testing.T) {
	home := shortHome(t)
	tg := newToolGateway(home, log.New(io.Discard, "", 0))
	base, leases, err := tg.gateway()
	if err != nil || base != toolgateway.ReadonlyURL {
		t.Fatalf("gateway = %q %v", base, err)
	}
	c, ok := leases.(toolgateway.ControlClient)
	if !ok || c.Path != toolgateway.ControlSocketPath(home) {
		t.Fatalf("leases %#v, want the worker's control client", leases)
	}
}
