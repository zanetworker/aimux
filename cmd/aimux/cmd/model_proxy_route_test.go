package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func routeRun(t *testing.T, env map[string]string, model string, asJSON bool) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runProxyRoute(context.Background(), func(k string) string { return env[k] }, model, asJSON, &out)
	return out.String(), err
}

func TestProxyRoute_DirectAndElsewhere(t *testing.T) {
	out, err := routeRun(t, nil, "claude-opus-5-5", false)
	if err != nil || out != "direct · anthropic claude-opus-5-5\n" {
		t.Fatalf("%q, %v", out, err)
	}
	out, err = routeRun(t, map[string]string{"ANTHROPIC_BASE_URL": "https://gateway.example.com"}, "", false)
	if err != nil || out != "another gateway\n" {
		t.Fatalf("%q, %v", out, err)
	}
}

func TestProxyRoute_ThroughAProxyThatAnswers(t *testing.T) {
	port := proxyAnswering(t, "codex", "anthropic")
	env := map[string]string{"ANTHROPIC_BASE_URL": fmt.Sprintf("http://127.0.0.1:%d/k/%s", port, envTestKey), "AIMUX_MODEL_PROXY_ENV": "engines"}
	out, err := routeRun(t, env, "codex:gpt-6.2-sol", false)
	if err != nil || out != fmt.Sprintf("proxy :%d ok · codex gpt-6.2-sol\n", port) {
		t.Fatalf("%q, %v", out, err)
	}
	jout, err := routeRun(t, env, "claude-opus-5-5", true)
	if err != nil || strings.Contains(jout, envTestKey) {
		t.Fatalf("%v\n%s", err, jout)
	}
	var doc struct {
		SchemaVersion int  `json:"schema_version"`
		Through       bool `json:"through_proxy"`
		Port          int  `json:"port"`
		Mode, Engine  string
		Proxy         struct {
			State    string   `json:"state"`
			Engines  []string `json:"engines"`
			EngineOn *bool    `json:"engine_on"`
		} `json:"proxy"`
	}
	if err := json.Unmarshal([]byte(jout), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || !doc.Through || doc.Port != port || doc.Mode != "engines" || doc.Engine != "anthropic" ||
		doc.Proxy.State != "ok" || len(doc.Proxy.Engines) != 2 || doc.Proxy.EngineOn == nil || !*doc.Proxy.EngineOn {
		t.Fatalf("doc = %+v\n%s", doc, jout)
	}
}

// The engine a session is on is off on the proxy: said, and the command does not succeed.
func TestProxyRoute_AnEngineThatIsOff(t *testing.T) {
	port := proxyAnswering(t, "codex")
	env := map[string]string{"ANTHROPIC_BASE_URL": fmt.Sprintf("http://127.0.0.1:%d", port), "AIMUX_MODEL_PROXY_ENV": "engines"}
	out, err := routeRun(t, env, "claude-opus-5-5", false)
	if err == nil || !strings.Contains(out, "the anthropic engine is off") {
		t.Fatalf("%q, %v", out, err)
	}
}

// Nothing answers on the port the shell points at: the line says so, the JSON still comes, and the exit is 1.
func TestProxyRoute_AProxyThatDoesNotAnswer(t *testing.T) {
	env := map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:1", "AIMUX_MODEL_PROXY_ENV": "codex", "ANTHROPIC_MODEL": "codex:gpt-6.2-sol"}
	out, err := routeRun(t, env, "", false)
	if err == nil || out != "proxy :1 DOWN · codex gpt-6.2-sol\n" {
		t.Fatalf("%q, %v", out, err)
	}
	jout, err := routeRun(t, env, "", true)
	if err == nil || !strings.Contains(jout, `"state": "down"`) {
		t.Fatalf("%v\n%s", err, jout)
	}
}

// Something answers on the port and it is not the proxy: said as that, not as nothing there.
func TestProxyRoute_AnotherListenerIsNotAimux(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>hello</html>")) }))
	t.Cleanup(other.Close)
	out, err := routeRun(t, map[string]string{"ANTHROPIC_BASE_URL": other.URL, "AIMUX_MODEL_PROXY_ENV": "engines"}, "claude-opus-5-5", false)
	if err == nil || !strings.Contains(out, "NOT AIMUX") {
		t.Fatalf("%q, %v", out, err)
	}
}
