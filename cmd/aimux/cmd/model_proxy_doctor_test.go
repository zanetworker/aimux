package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/modelproxy"
)

func doctorRun(t *testing.T, port int, d doctorDeps, asJSON bool) (string, error) {
	t.Helper()
	if d.home == "" {
		d.home = t.TempDir()
	}
	if d.key == nil {
		d.key = testKey
	}
	if d.getenv == nil {
		d.getenv = func(string) string { return "" }
	}
	d.now, d.cliBuild = time.Now, "v0.14.0-289-gabc"
	var out bytes.Buffer
	err := runProxyDoctor(context.Background(), port, d, asJSON, &out)
	return out.String(), err
}

func TestProxyDoctor_AProxyThatAnswersAndHoldsTheKey(t *testing.T) {
	port := proxyAnswering(t, "codex")
	out, err := doctorRun(t, port, doctorDeps{}, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"service", fmt.Sprintf("answers on 127.0.0.1:%d", port), "the proxy holds the stored key", "answers with codex",
		// The stand-in answers only the keyless health, so the keyed one is said to be unread, not guessed at.
		"detailed health could not be read", "nothing is failing"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, envTestKey) {
		t.Fatal("the key was printed")
	}
}

func TestProxyDoctor_NothingAnswers(t *testing.T) {
	out, err := doctorRun(t, 1, doctorDeps{}, false)
	if err == nil || !strings.Contains(out, "nothing answers on 127.0.0.1:1") || !strings.Contains(out, "aimux model proxy serve") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// A listener that calls itself the proxy and cannot prove it holds the key is never sent the key: one that gives
// a wrong proof is said not to hold it, and one that gives none is taken for a build too old to prove anything.
func TestProxyDoctor_AListenerThatCannotProveItselfGetsNoKey(t *testing.T) {
	for proof, want := range map[string]string{"wrong": "does not hold the stored proxy key", "": "cannot prove it holds it"} {
		sawKey := false
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-api-key") != "" || strings.Contains(r.URL.String(), envTestKey) {
				sawKey = true
			}
			_ = json.NewEncoder(w).Encode(modelproxy.Liveness{Service: modelproxy.LivenessService, Status: "ok", StartedAt: time.Now(), Engines: []string{"codex"}, Proof: proof})
		}))
		var port int
		_, _ = fmt.Sscanf(other.URL, "http://127.0.0.1:%d", &port)
		out, err := doctorRun(t, port, doctorDeps{}, false)
		other.Close()
		if !strings.Contains(out, want) || (proof == "wrong") != (err != nil) {
			t.Fatalf("proof %q: %v\n%s", proof, err, out)
		}
		if sawKey {
			t.Fatalf("proof %q: a listener that did not prove itself was sent the key", proof)
		}
	}
}

func TestProxyDoctor_NoKeyStoredAndTheConfigFile(t *testing.T) {
	port := proxyAnswering(t, "codex")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte("parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	noKey := func(context.Context) (string, error) { return "", keyring.ErrNotFound }
	out, err := doctorRun(t, port, doctorDeps{home: home, key: noKey}, true)
	if err == nil {
		t.Fatalf("no key stored and it succeeded:\n%s", out)
	}
	var doc struct {
		SchemaVersion int    `json:"schema_version"`
		Port          int    `json:"port"`
		Verdict       string `json:"verdict"`
		Findings      []struct{ ID, Level, Detail, Fix string }
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	levels := map[string]string{}
	fixes := map[string]string{}
	for _, f := range doc.Findings {
		levels[f.ID], fixes[f.ID] = f.Level, f.Fix
	}
	if doc.SchemaVersion != 1 || doc.Port != port || doc.Verdict != "fail" || levels["key"] != "fail" || levels["engines"] != "warn" || fixes["engines"] != "aimux setup --apply" {
		t.Fatalf("doc = %+v", doc)
	}
}

// A key that cannot be read is an error of its own, not "no key".
func TestProxyDoctor_AKeyThatCannotBeRead(t *testing.T) {
	broken := func(context.Context) (string, error) { return "", errors.New("the keychain is locked") }
	if _, err := doctorRun(t, 1, doctorDeps{key: broken}, false); err == nil || !strings.Contains(err.Error(), "keychain is locked") {
		t.Fatalf("err = %v", err)
	}
}

// The key goes only down the connection the proof came back on. A listener that proves itself and then makes the
// doctor connect again (here: it closes every connection) is not sent the key on the new one, since by then
// something else could hold the port.
func TestProxyDoctor_TheKeyNeverGoesDownASecondConnection(t *testing.T) {
	sawKey := false
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" {
			sawKey = true
		}
		_ = json.NewEncoder(w).Encode(modelproxy.Liveness{
			Service: modelproxy.LivenessService, Status: "ok", StartedAt: time.Now(), Engines: []string{"codex"},
			Proof: modelproxy.LivenessProof(envTestKey, r.URL.Query().Get("challenge"), listenerPort(r)),
		})
	}))
	server.Config.SetKeepAlivesEnabled(false)
	server.Start()
	t.Cleanup(server.Close)
	var port int
	_, _ = fmt.Sscanf(server.URL, "http://127.0.0.1:%d", &port)
	out, err := doctorRun(t, port, doctorDeps{}, false)
	if err != nil || !strings.Contains(out, "the proxy holds the stored key") || !strings.Contains(out, "detailed health could not be read") {
		t.Fatalf("%v\n%s", err, out)
	}
	if sawKey {
		t.Fatal("the key was sent on a connection that had proven nothing")
	}
}

// A listener that has not proven itself is never the subject of advice that would send it the key.
func TestProxyDoctor_NoKeyedAdviceForAnUnprovenListener(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(modelproxy.Liveness{Service: modelproxy.LivenessService, Status: "degraded", StartedAt: time.Now(), Proof: "wrong"})
	}))
	t.Cleanup(other.Close)
	var port int
	_, _ = fmt.Sscanf(other.URL, "http://127.0.0.1:%d", &port)
	out, _ := doctorRun(t, port, doctorDeps{}, false)
	if strings.Contains(out, "aimux model proxy status") {
		t.Fatalf("it names a command that sends the key:\n%s", out)
	}
}
