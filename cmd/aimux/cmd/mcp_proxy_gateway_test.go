package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/toolgateway"
)

func TestMCPProxyGatewayServe_Help(t *testing.T) {
	root := newMCPProxyCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"gateway", "serve", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sandbox tool gateway worker", "Usage:"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help lacks %q:\n%s", want, out.String())
		}
	}
}

func TestGatewayAgent_Plist(t *testing.T) {
	a := gatewayAgent("/Users/me")
	a.Binary = "/usr/local/bin/aimux"
	p := string(a.Plist())
	for _, want := range []string{"com.aimux.tool-gateway", "<string>gateway</string>", "<string>serve</string>",
		"KeepAlive", "RunAtLoad", "/Users/me/.aimux/logs/tool-gateway"} {
		if !strings.Contains(p, want) {
			t.Fatalf("plist lacks %q:\n%s", want, p)
		}
	}
	if a.PlistPath != "/Users/me/Library/LaunchAgents/com.aimux.tool-gateway.plist" {
		t.Fatalf("plist path %s", a.PlistPath)
	}
	if len(a.Env) != 2 || a.Env["HOME"] != "/Users/me" {
		t.Fatalf("env %v: only PATH and HOME belong in a world-readable plist", a.Env)
	}
}

type fakeGatewayStatus struct {
	st  toolgateway.ControlStatus
	err error
}

func (f fakeGatewayStatus) Status(context.Context) (toolgateway.ControlStatus, error) {
	return f.st, f.err
}

func TestGatewayStatusLine(t *testing.T) {
	running := gatewayStatusLine(context.Background(), fakeGatewayStatus{st: toolgateway.ControlStatus{Leases: 2, Backends: []string{"jira", "wiki"}, Down: []string{"wiki"}}})
	if !strings.Contains(running, "running") || !strings.Contains(running, "2 leases") || !strings.Contains(running, "down: wiki") {
		t.Fatalf("running line %q", running)
	}
	stopped := gatewayStatusLine(context.Background(), fakeGatewayStatus{err: toolgateway.ErrGatewayNotRunning})
	if !strings.Contains(stopped, "not running") || !strings.Contains(stopped, "aimux mcp proxy gateway install") {
		t.Fatalf("stopped line %q", stopped)
	}
}

func TestDoctor_GatewayWorker(t *testing.T) {
	c := gatewayWorkerCheck(context.Background(), fakeGatewayStatus{err: toolgateway.ErrGatewayNotRunning})
	if c.Status != checkFail || !strings.Contains(c.Fix, "aimux mcp proxy gateway install") {
		t.Fatalf("not running: %+v", c)
	}
	c = gatewayWorkerCheck(context.Background(), fakeGatewayStatus{st: toolgateway.ControlStatus{Leases: 1}})
	if c.Status != checkOK {
		t.Fatalf("running: %+v", c)
	}
}
