package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/modelproxy"
)

const envTestKey = "KEYFORTESTSONLY234567"

// proxyAnswering serves the keyless health answer of a proxy with the given engines, and returns its port.
func proxyAnswering(t *testing.T, engines ...string) int {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(modelproxy.Liveness{
			Service: modelproxy.LivenessService, Status: "ok", StartedAt: time.Now(), Engines: engines,
			Proof: modelproxy.LivenessProof(envTestKey, r.URL.Query().Get("challenge"), listenerPort(r)),
		})
	}))
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	return n
}

func testKey(context.Context) (string, error) { return envTestKey, nil }

func noProviderFlags(string) string { return "" }

// listenerPort is the port a test server accepted r on.
func listenerPort(r *http.Request) int {
	return r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr).Port
}

func TestProxyEnv_PrintsWhatAShellCanEval(t *testing.T) {
	port := proxyAnswering(t, "codex", "anthropic")
	var stdout, stderr bytes.Buffer
	if err := modelProxyEnv(context.Background(), port, false, testKey, noProviderFlags, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	want := "export ANTHROPIC_BASE_URL='http://127.0.0.1:" + strconv.Itoa(port) + "/k/" + envTestKey + "'\nunset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN\n"
	if stdout.String() != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}
	script := filepath.Join(t.TempDir(), "env.sh")
	if err := os.WriteFile(script, stdout.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", script).CombinedOutput(); err != nil { // #nosec G204 -- the test's own file
		t.Fatalf("sh -n: %v: %s", err, out)
	}
	if !strings.Contains(stderr.String(), "aimux model proxy env --off") {
		t.Errorf("stderr does not say how to undo it: %q", stderr.String())
	}
}

func TestProxyEnv_FailsWhenTheProxyDoesNotAnswer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	var stdout, stderr bytes.Buffer
	err = modelProxyEnv(context.Background(), port, false, testKey, noProviderFlags, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "aimux model proxy status --port") || stdout.Len() != 0 {
		t.Fatalf("err = %v, stdout = %q", err, stdout.String())
	}
}

func TestProxyEnv_FailsWhenAnthropicIsOff(t *testing.T) {
	for name, engines := range map[string][]string{"codex only": {"codex"}, "an older build that names none": nil} {
		port := proxyAnswering(t, engines...)
		var stdout, stderr bytes.Buffer
		err := modelProxyEnv(context.Background(), port, false, testKey, noProviderFlags, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "--anthropic") || stdout.Len() != 0 {
			t.Errorf("%s: err = %v, stdout = %q", name, err, stdout.String())
		}
	}
}

func TestProxyEnv_NeverPrintsTheKeyToStderrOrInAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := modelProxyEnv(context.Background(), proxyAnswering(t, "codex", "anthropic"), false, testKey, noProviderFlags, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), envTestKey) {
		t.Errorf("stderr holds the key: %q", stderr.String())
	}
	failing := func(context.Context) (string, error) { return "", errors.New("keychain locked") }
	stdout.Reset()
	err := modelProxyEnv(context.Background(), proxyAnswering(t, "codex", "anthropic"), false, failing, noProviderFlags, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 {
		t.Errorf("a key that cannot be read: err = %v, stdout = %q", err, stdout.String())
	}
}

func TestProxyEnv_OffUnsetsEverythingOnSet(t *testing.T) {
	var on, off, stderr bytes.Buffer
	if err := modelProxyEnv(context.Background(), proxyAnswering(t, "codex", "anthropic"), false, testKey, noProviderFlags, &on, &stderr); err != nil {
		t.Fatal(err)
	}
	// Nothing listens and no key can be read: --off needs neither.
	noKey := func(context.Context) (string, error) { return "", errors.New("must not be asked") }
	if err := modelProxyEnv(context.Background(), 1, true, noKey, noProviderFlags, &off, &stderr); err != nil {
		t.Fatal(err)
	}
	if off.String() != "unset ANTHROPIC_BASE_URL\n" {
		t.Fatalf("--off printed %q", off.String())
	}
	script := on.String() + off.String() + "env\n"
	cmd := exec.Command("sh", "-c", script) // #nosec G204 -- lines this test just produced
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "ANTHROPIC_API_KEY=was-set"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), envTestKey) || strings.Contains(string(out), "ANTHROPIC_BASE_URL") {
		t.Errorf("after on then off, the shell still holds the key or the base URL:\n%s", out)
	}
}

// Something else on the port can say it is the proxy. It cannot show that it holds the key.
func TestProxyEnv_GivesNothingToAListenerThatIsNotTheProxy(t *testing.T) {
	for name, proof := range map[string]func(nonce string) string{
		"no proof":                         func(string) string { return "" },
		"a made-up proof":                  func(string) string { return strings.Repeat("ab", 32) },
		"a proof with its own key":         func(nonce string) string { return modelproxy.LivenessProof("its-own-key", nonce, 0) },
		"the real proxy's answer, relayed": func(nonce string) string { return modelproxy.LivenessProof(envTestKey, nonce, modelproxy.StablePort) },
	} {
		var asked []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, r.URL.String()+" "+r.Header.Get("Authorization")+r.Header.Get("X-Api-Key"))
			_ = json.NewEncoder(w).Encode(modelproxy.Liveness{
				Service: modelproxy.LivenessService, Status: "ok", StartedAt: time.Now(),
				Engines: []string{"codex", "anthropic"}, Proof: proof(r.URL.Query().Get("challenge")),
			})
		}))
		_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
		port, _ := strconv.Atoi(portText)
		var stdout, stderr bytes.Buffer
		err := modelProxyEnv(context.Background(), port, false, testKey, noProviderFlags, &stdout, &stderr)
		server.Close()
		if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "aimux model proxy status --port") {
			t.Errorf("%s: err = %v, stdout = %q", name, err, stdout.String())
		}
		if strings.Contains(strings.Join(asked, " "), envTestKey) {
			t.Errorf("%s: the listener was sent the key", name)
		}
	}
}

func TestProxyEnv_RefusesAShellSetForAnotherProvider(t *testing.T) {
	vertex := func(name string) string {
		if name == "CLAUDE_CODE_USE_VERTEX" {
			return "1"
		}
		return ""
	}
	var stdout, stderr bytes.Buffer
	err := modelProxyEnv(context.Background(), proxyAnswering(t, "codex", "anthropic"), false, testKey, vertex, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_USE_VERTEX") || stdout.Len() != 0 {
		t.Fatalf("err = %v, stdout = %q", err, stdout.String())
	}
}
