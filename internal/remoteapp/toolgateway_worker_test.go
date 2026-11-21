package remoteapp

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

type observedWorker struct{}

func (observedWorker) PutLease(context.Context, toolgateway.Lease) error { return nil }
func (observedWorker) Renew(string, time.Time) error                     { return nil }
func (observedWorker) DeleteLease(string)                                {}
func (observedWorker) Relist(context.Context, string, []string) (toolgateway.RelistResult, error) {
	return toolgateway.RelistResult{}, nil
}
func (observedWorker) Status() toolgateway.ControlStatus { return toolgateway.ControlStatus{} }
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

// A turn's notice names the tools of the profile's sets that the worker
// lists but that need review; with nothing listed it is empty.
func TestToolGateway_NoticeNamesHeldBackTools(t *testing.T) {
	home := shortHome(t)
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{"fake": {Type: toolreg.TypeMCPHTTP, URL: "http://fake/"}},
		Sets:    map[string]toolreg.Set{"jira:readonly": {Source: "fake", Tools: []string{"search"}}}}
	if err := toolreg.WriteNew(toolreg.DefaultPath(home), reg); err != nil {
		t.Fatal(err)
	}
	tg := newToolGateway(home, log.New(io.Discard, "", 0))
	review := tg.review(t.TempDir())
	notice := func(c profile.Compiled) string { return review(c).Notice } // no user profiles: the builtin work-readonly asks for jira:readonly
	c := profile.Compiled{Profile: "work-readonly", Snapshot: tg.snaps.turn()}
	if got := notice(c); got != "" {
		t.Fatalf("no worker: notice %q, want none (nothing listed)", got)
	}
	ln, err := toolgateway.ListenControl(toolgateway.ControlSocketPath(home))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = toolgateway.ServeControl(ctx, ln, observedWorker{}) }()
	// search is listed live but never approved: held for review.
	if got := notice(c); got != "1 tool held for review (fake: search unpinned): run aimux tools status" {
		t.Fatalf("notice %q", got)
	}
	// Granted fake/search, all of it held back: fake is left out of the turn.
	granted := c
	granted.Grant = profile.NewToolGrant(map[string][]string{"fake": {"search"}})
	if r := review(granted); !reflect.DeepEqual(r.LeftOut, []string{"fake"}) ||
		r.Notice != "fake: 1 tool unpinned, held for review, left out of this turn: run aimux tools status" {
		t.Fatalf("review %+v, want fake left out and named", r)
	}
	// The notice names the held tool's set as the profile asks for it.
	if r := review(c); !reflect.DeepEqual(r.Sets, []string{"jira:readonly"}) {
		t.Fatalf("review sets %v, want [jira:readonly]", r.Sets)
	}
	if r := review(profile.Compiled{Profile: "research", Snapshot: c.Snapshot}); r.Sets != nil {
		t.Fatalf("no held tools: sets %v", r.Sets)
	}
	if got := notice(profile.Compiled{Profile: "research", Snapshot: c.Snapshot}); got != "" {
		t.Fatalf("a profile without tool sets got a notice: %q", got)
	}
}
