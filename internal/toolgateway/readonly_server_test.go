package toolgateway_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// readonlyRig is a ReadonlyServer over fakes: an ephemeral listener (never
// :7390), a Build that serves nothing, and a Serve that records its options.
func readonlyRig(t *testing.T, logw *bytes.Buffer) (*toolgateway.ReadonlyServer, chan mcpproxy.DaemonOptions, *string) {
	t.Helper()
	served := make(chan mcpproxy.DaemonOptions, 1)
	asked := new(string)
	return &toolgateway.ReadonlyServer{
		Home: t.TempDir(),
		Log:  log.New(logw, "", 0),
		Listen: func(network, addr string) (net.Listener, error) {
			*asked = addr
			return net.Listen("tcp", "127.0.0.1:0")
		},
		Build: func(context.Context, mcpproxy.ReadonlyConfig, mcpproxy.ToolPins, func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
			return map[string]*server.MCPServer{}, nil, map[string]error{"jira": errors.New("down")}, nil
		},
		Serve: func(ctx context.Context, ln net.Listener, _ map[string]*server.MCPServer, _ *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error {
			served <- opt
			<-ctx.Done()
			return ln.Close()
		},
	}, served, asked
}

// The server listens on the read-only address and checks both the admin
// token (aimux browser verify) and the issued session tokens.
func TestReadonlyServer_ServesAdminTokenAndSessionTokens(t *testing.T) {
	var logw bytes.Buffer
	r, served, asked := readonlyRig(t, &logw)
	r.Token = "admin-token"
	r.Tokens = &toolgateway.Tokens{}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opt := <-served
	stop()
	if *asked != toolgateway.ReadonlyAddr || toolgateway.ReadonlyAddr != "127.0.0.1:7390" {
		t.Fatalf("listened on %q, want %s", *asked, toolgateway.ReadonlyAddr)
	}
	if opt.Token != "admin-token" || opt.Tokens != r.Tokens || opt.AddServers == nil {
		t.Fatalf("daemon options: token %q tokens %v addServers %v", opt.Token, opt.Tokens, opt.AddServers != nil)
	}
	if !strings.Contains(logw.String(), "backend jira unavailable") || strings.Contains(logw.String(), "admin-token") {
		t.Fatalf("log %q: want the down backend, never the token", logw.String())
	}
}

// A listen failure is an error and leaves nothing running.
func TestReadonlyServer_ListenFailure(t *testing.T) {
	var logw bytes.Buffer
	r, _, _ := readonlyRig(t, &logw)
	r.Listen = func(string, string) (net.Listener, error) { return nil, errors.New("address in use") }
	if _, err := r.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "127.0.0.1:7390") {
		t.Fatalf("Start = %v, want a listen error naming the address", err)
	}
}

// Refresh reloads the pins: a changed pin set rebuilds every backend with
// the new pins (replacing the running servers); unchanged pins with no
// backend down rebuild nothing.
func TestReadonlyServer_RefreshReloadsChangedPins(t *testing.T) {
	var logw bytes.Buffer
	r, served, _ := readonlyRig(t, &logw)
	store := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(r.Home)}
	if err := store.Save(toolgateway.Pins{"wiki": {"search": "p1"}}); err != nil {
		t.Fatal(err)
	}
	r.Build = func(context.Context, mcpproxy.ReadonlyConfig, mcpproxy.ToolPins, func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		return map[string]*server.MCPServer{"wiki": server.NewMCPServer("wiki", "1")}, nil, nil, nil
	}
	var rebuilt [][]string
	var rebuiltPins []mcpproxy.ToolPins
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		rebuilt = append(rebuilt, names)
		rebuiltPins = append(rebuiltPins, pins)
		out := map[string]*server.MCPServer{}
		for _, n := range names {
			out[n] = server.NewMCPServer(n, "2")
		}
		return out, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	opt := <-served
	var added []string
	opt.AddServers(func(name string, _ *server.MCPServer) { added = append(added, name) }, func(string) bool { return true })

	r.Refresh(context.Background())
	if len(rebuilt) != 0 {
		t.Fatalf("unchanged pins, nothing down: rebuilt %v", rebuilt)
	}
	if err := store.Save(toolgateway.Pins{"wiki": {"search": "p2"}}); err != nil {
		t.Fatal(err)
	}
	r.Refresh(context.Background())
	if len(rebuilt) != 1 || strings.Join(rebuilt[0], ",") != "wiki" || strings.Join(added, ",") != "wiki" {
		t.Fatalf("re-pin: rebuilt %v added %v, want wiki rebuilt and replaced", rebuilt, added)
	}
	if p, _ := rebuiltPins[0].(toolgateway.Pins); p["wiki"]["search"] != "p2" {
		t.Fatalf("rebuilt with pins %v, want the new ones", rebuiltPins[0])
	}
}

// refreshRig starts r serving wiki and jira pinned, and records what
// Refresh adds and removes on the running daemon.
func refreshRig(t *testing.T, r *toolgateway.ReadonlyServer, served chan mcpproxy.DaemonOptions) (store toolgateway.PinStore, added, removed *[]string) {
	t.Helper()
	store = toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(r.Home)}
	if err := store.Save(toolgateway.Pins{"wiki": {"search": "p1"}, "jira": {"get": "p1"}}); err != nil {
		t.Fatal(err)
	}
	r.Build = func(context.Context, mcpproxy.ReadonlyConfig, mcpproxy.ToolPins, func(string, string, error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error) {
		return map[string]*server.MCPServer{"wiki": server.NewMCPServer("wiki", "1"), "jira": server.NewMCPServer("jira", "1")}, nil, nil, nil
	}
	stop, err := r.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	opt := <-served
	added, removed = new([]string), new([]string)
	opt.AddServers(func(name string, _ *server.MCPServer) { *added = append(*added, name) },
		func(name string) bool { *removed = append(*removed, name); return true })
	return store, added, removed
}

// A re-pin that leaves a backend with no pinned tool removes its endpoint,
// so tokens already issued can no longer reach the revoked tools.
func TestReadonlyServer_RefreshRemovesBackendWithNoPinnedTools(t *testing.T) {
	var logw bytes.Buffer
	r, served, _ := readonlyRig(t, &logw)
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		out := map[string]*server.MCPServer{}
		for _, n := range names {
			if p, _ := pins.(toolgateway.Pins); len(p[n]) > 0 {
				out[n] = server.NewMCPServer(n, "2")
			}
		}
		return out, nil
	}
	store, added, removed := refreshRig(t, r, served)
	if err := store.Save(toolgateway.Pins{"wiki": {"search": "p1"}}); err != nil {
		t.Fatal(err)
	}
	r.Refresh(context.Background())
	if strings.Join(*added, ",") != "wiki" || strings.Join(*removed, ",") != "jira" {
		t.Fatalf("jira un-pinned: added %v removed %v, want wiki replaced and jira removed", *added, *removed)
	}
}

// A re-pin whose rebuild of a backend fails removes that backend's old
// endpoint (fail closed) and keeps it down, so a later Refresh with the
// same pins retries and adds it.
func TestReadonlyServer_RefreshRemovesFailedRebuildAndRetriesIt(t *testing.T) {
	var logw bytes.Buffer
	r, served, _ := readonlyRig(t, &logw)
	jiraUp := false
	var rebuilt [][]string
	r.Rebuild = func(_ context.Context, _ mcpproxy.ReadonlyConfig, names []string, _ mcpproxy.ToolPins, _ *mcpproxy.Pool, _ func(string, string, error)) (map[string]*server.MCPServer, map[string]error) {
		rebuilt = append(rebuilt, names)
		out, errs := map[string]*server.MCPServer{}, map[string]error{}
		for _, n := range names {
			if n == "jira" && !jiraUp {
				errs[n] = errors.New("down")
				continue
			}
			out[n] = server.NewMCPServer(n, "2")
		}
		return out, errs
	}
	store, added, removed := refreshRig(t, r, served)
	if err := store.Save(toolgateway.Pins{"wiki": {"search": "p2"}, "jira": {"get": "p2"}}); err != nil {
		t.Fatal(err)
	}
	r.Refresh(context.Background())
	if strings.Join(*added, ",") != "wiki" || strings.Join(*removed, ",") != "jira" {
		t.Fatalf("jira rebuild failed: added %v removed %v, want wiki replaced and jira removed", *added, *removed)
	}
	jiraUp = true
	r.Refresh(context.Background())
	if got := strings.Join(rebuilt[len(rebuilt)-1], ","); got != "jira" {
		t.Fatalf("retry rebuilt %q, want jira (still down)", got)
	}
	if strings.Join(*added, ",") != "wiki,jira" || strings.Join(*removed, ",") != "jira" {
		t.Fatalf("retry: added %v removed %v, want jira added back", *added, *removed)
	}
}
