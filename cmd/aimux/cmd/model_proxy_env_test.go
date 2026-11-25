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
	if err := modelProxyEnv(context.Background(), envOpts{port: port}, testKey, noProviderFlags, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	want := "export ANTHROPIC_BASE_URL='http://127.0.0.1:" + strconv.Itoa(port) + "/k/" + envTestKey + "' &&\nunset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN &&\nexport AIMUX_MODEL_PROXY_ENV='engines'\n"
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
	err = modelProxyEnv(context.Background(), envOpts{port: port}, testKey, noProviderFlags, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "aimux model proxy status --port") || stdout.Len() != 0 {
		t.Fatalf("err = %v, stdout = %q", err, stdout.String())
	}
}

func TestProxyEnv_FailsWhenAnthropicIsOff(t *testing.T) {
	for name, engines := range map[string][]string{"codex only": {"codex"}, "an older build that names none": nil} {
		port := proxyAnswering(t, engines...)
		var stdout, stderr bytes.Buffer
		err := modelProxyEnv(context.Background(), envOpts{port: port}, testKey, noProviderFlags, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "--anthropic") || stdout.Len() != 0 {
			t.Errorf("%s: err = %v, stdout = %q", name, err, stdout.String())
		}
	}
}

func TestProxyEnv_NeverPrintsTheKeyToStderrOrInAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := modelProxyEnv(context.Background(), envOpts{port: proxyAnswering(t, "codex", "anthropic")}, testKey, noProviderFlags, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), envTestKey) {
		t.Errorf("stderr holds the key: %q", stderr.String())
	}
	failing := func(context.Context) (string, error) { return "", errors.New("keychain locked") }
	stdout.Reset()
	err := modelProxyEnv(context.Background(), envOpts{port: proxyAnswering(t, "codex", "anthropic")}, failing, noProviderFlags, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 {
		t.Errorf("a key that cannot be read: err = %v, stdout = %q", err, stdout.String())
	}
}

func TestProxyEnv_OffUnsetsEverythingOnSet(t *testing.T) {
	var on, off, stderr bytes.Buffer
	if err := modelProxyEnv(context.Background(), envOpts{port: proxyAnswering(t, "codex", "anthropic")}, testKey, noProviderFlags, &on, &stderr); err != nil {
		t.Fatal(err)
	}
	// Nothing listens and no key can be read: --off needs neither.
	noKey := func(context.Context) (string, error) { return "", errors.New("must not be asked") }
	if err := modelProxyEnv(context.Background(), envOpts{port: 1, off: true}, noKey, noProviderFlags, &off, &stderr); err != nil {
		t.Fatal(err)
	}
	if off.String() != "unset ANTHROPIC_BASE_URL AIMUX_MODEL_PROXY_ENV\n" {
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
		err := modelProxyEnv(context.Background(), envOpts{port: port}, testKey, noProviderFlags, &stdout, &stderr)
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
	err := modelProxyEnv(context.Background(), envOpts{port: proxyAnswering(t, "codex", "anthropic")}, testKey, vertex, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_USE_VERTEX") || stdout.Len() != 0 {
		t.Fatalf("err = %v, stdout = %q", err, stdout.String())
	}
}

func TestProxyEnv_CodexAlone(t *testing.T) {
	// A proxy with Codex only is enough for this form, and it still has to prove it holds the key.
	port := proxyAnswering(t, "codex")
	var stdout, stderr bytes.Buffer
	if err := modelProxyEnv(context.Background(), envOpts{port: port, codex: true}, testKey, noProviderFlags, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	want := "export ANTHROPIC_BASE_URL='http://127.0.0.1:" + strconv.Itoa(port) + "' &&\nexport ANTHROPIC_API_KEY='" + envTestKey + "' &&\nexport ANTHROPIC_MODEL='codex:gpt-6.2-sol' &&\nunset ANTHROPIC_AUTH_TOKEN &&\nexport AIMUX_MODEL_PROXY_ENV='codex'\n"
	if stdout.String() != want || strings.Contains(stderr.String(), envTestKey) || !strings.Contains(stderr.String(), "gpt-6.2-sol") {
		t.Fatalf("stdout =\n%s\nstderr = %s", stdout.String(), stderr.String())
	}
	stdout.Reset()
	if err := modelProxyEnv(context.Background(), envOpts{port: port, codex: true, model: "gpt-6.2-astra"}, testKey, noProviderFlags, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "ANTHROPIC_MODEL='codex:gpt-6.2-astra'") {
		t.Errorf("another model: %v\n%s", err, stdout.String())
	}
	stdout.Reset()
	if err := modelProxyEnv(context.Background(), envOpts{port: port, codex: true, model: "x'; rm -rf ~; '"}, testKey, noProviderFlags, &stdout, &stderr); err == nil || stdout.Len() != 0 {
		t.Errorf("a model that is not a name: %v, stdout %q", err, stdout.String())
	}
	// Something that is not the proxy gets nothing, in this form as in the other.
	impostor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(modelproxy.Liveness{Service: modelproxy.LivenessService, Status: "ok", StartedAt: time.Now(), Engines: []string{"codex"}})
	}))
	defer impostor.Close()
	_, portText, _ := net.SplitHostPort(impostor.Listener.Addr().String())
	other, _ := strconv.Atoi(portText)
	stdout.Reset()
	if err := modelProxyEnv(context.Background(), envOpts{port: other, codex: true}, testKey, noProviderFlags, &stdout, &stderr); err == nil || stdout.Len() != 0 {
		t.Errorf("an impostor: %v, stdout %q", err, stdout.String())
	}
}

// One shell: Codex alone, then both engines, then off. Nothing is left holding the proxy key, and a key or a model
// of the owner's own is not taken from him.
func TestProxyEnv_OneShellThroughEveryForm(t *testing.T) {
	both, codexOnly := proxyAnswering(t, "codex", "anthropic"), proxyAnswering(t, "codex")
	var first, second, stderr bytes.Buffer
	if err := modelProxyEnv(context.Background(), envOpts{port: codexOnly, codex: true}, testKey, noProviderFlags, &first, &stderr); err != nil {
		t.Fatal(err)
	}
	// The second form runs in the shell the first one set, and sees its marker.
	afterCodexForm := func(name string) string {
		return map[string]string{"AIMUX_MODEL_PROXY_ENV": "codex"}[name]
	}
	if err := modelProxyEnv(context.Background(), envOpts{port: both}, testKey, afterCodexForm, &second, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.String(), "unset ANTHROPIC_MODEL") {
		t.Errorf("going from Codex alone to both engines leaves the pinned model:\n%s", strings.ReplaceAll(second.String(), envTestKey, "<key>"))
	}
	off := func(env map[string]string) string {
		var out bytes.Buffer
		if err := modelProxyEnv(context.Background(), envOpts{off: true}, testKey, func(name string) string { return env[name] }, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	// After the Codex form, off takes the key and the model it set, and needs no key to know that.
	locked := func(context.Context) (string, error) { return "", errors.New("the secret store is locked") }
	var printed bytes.Buffer
	codexShell := map[string]string{"ANTHROPIC_API_KEY": envTestKey, "ANTHROPIC_MODEL": "codex:gpt-6.2-sol", "ANTHROPIC_BASE_URL": "http://127.0.0.1:7385", "AIMUX_MODEL_PROXY_ENV": "codex"}
	if err := modelProxyEnv(context.Background(), envOpts{off: true}, locked, func(name string) string { return codexShell[name] }, &printed, &stderr); err != nil || printed.String() != "unset ANTHROPIC_BASE_URL AIMUX_MODEL_PROXY_ENV ANTHROPIC_API_KEY ANTHROPIC_MODEL\n" {
		t.Errorf("off after the Codex form, with the secret store locked: %v, %q", err, printed.String())
	}
	// After the two-engine form, a codex: model the owner had set himself is his, and stays.
	if got := off(map[string]string{"ANTHROPIC_MODEL": "codex:gpt-6.2-astra", "AIMUX_MODEL_PROXY_ENV": "engines"}); got != "unset ANTHROPIC_BASE_URL AIMUX_MODEL_PROXY_ENV\n" {
		t.Errorf("off after the two-engine form: %q", got)
	}
	// The owner's own key and model are left alone.
	if got := off(map[string]string{"ANTHROPIC_API_KEY": "the-owners-own-key", "ANTHROPIC_MODEL": "claude-opus-5-5"}); got != "unset ANTHROPIC_BASE_URL AIMUX_MODEL_PROXY_ENV\n" {
		t.Errorf("off with the owner's own key and model: %q", got)
	}
	// A shell set before there was a marker: the key is looked at. When it cannot be, nothing is printed, so the
	// shell is not left with the key in it and the proxy's address gone.
	old := map[string]string{"ANTHROPIC_API_KEY": envTestKey, "ANTHROPIC_BASE_URL": "http://127.0.0.1:7385"}
	if got := off(old); got != "unset ANTHROPIC_BASE_URL AIMUX_MODEL_PROXY_ENV ANTHROPIC_API_KEY\n" {
		t.Errorf("off in a shell set by hand: %q", got)
	}
	printed.Reset()
	if err := modelProxyEnv(context.Background(), envOpts{off: true}, locked, func(name string) string { return old[name] }, &printed, &stderr); err == nil || printed.Len() != 0 {
		t.Errorf("off in a shell set by hand with the secret store locked: %v, %q", err, printed.String())
	}
	script := first.String() + second.String() + off(map[string]string{"AIMUX_MODEL_PROXY_ENV": "engines"}) + "env\n"
	cmd := exec.Command("sh", "-c", script) // #nosec G204 -- lines this test just produced
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), envTestKey) || strings.Contains(string(out), "ANTHROPIC_BASE_URL") || strings.Contains(string(out), "ANTHROPIC_MODEL") {
		t.Errorf("after every form and off, the shell still holds something of the proxy's:\n%s", out)
	}
}

// If the address cannot be set, the key is not exported to wherever the old address points.
func TestProxyEnv_TheKeyDoesNotFollowAnAddressThatCouldNotBeSet(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	var lines, stderr bytes.Buffer
	if err := modelProxyEnv(context.Background(), envOpts{port: proxyAnswering(t, "codex"), codex: true}, testKey, noProviderFlags, &lines, &stderr); err != nil {
		t.Fatal(err)
	}
	script := "readonly ANTHROPIC_BASE_URL=https://elsewhere.invalid\n" + lines.String() + "\nenv\n"
	cmd := exec.Command("bash", "-c", script) // #nosec G204 -- lines this test just produced
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, _ := cmd.Output()
	if strings.Contains(string(out), envTestKey) {
		t.Error("the proxy key was exported although the address could not be set")
	}
}
