package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// runEdit takes the port from the manifest, probes a real server, saves the
// definition and its tools, and calls that port's /reload.
func TestRunEdit_Wiring(t *testing.T) {
	home := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var reloads atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/reload" && r.Header.Get(mcpproxy.ProbeHeader) != "" {
			reloads.Add(1)
			_, _ = w.Write([]byte(`{"added":[],"removed":[],"changed":["wiki"]}`))
			return
		}
		http.NotFound(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	aimux := filepath.Join(home, ".aimux")
	def := mcpproxy.ServerConfig{Command: exe, Env: map[string]string{fakeMCPEnv: "1"}}
	if err := mcpproxy.SaveServerFile(filepath.Join(aimux, "mcp-servers.json"), map[string]mcpproxy.ServerConfig{"wiki": def}, []string{"wiki"}); err != nil {
		t.Fatal(err)
	}
	written := fmt.Sprintf(`{"type":"http","url":%q}`, mcpproxy.EntryURL(port, "wiki"))
	m := fmt.Sprintf(`{"version": 2, "port": %d, "servers": {"wiki": {"original": "{}", "written": %q}}}`, port, written)
	if err := os.WriteFile(filepath.Join(aimux, "mcp-proxy-install.json"), []byte(m), 0o600); err != nil {
		t.Fatal(err)
	}
	editor := func(path string) error {
		var cfg mcpproxy.ServerConfig
		b, err := os.ReadFile(path) // #nosec G304 -- the edit's temp file
		if err == nil {
			err = json.Unmarshal(b, &cfg)
		}
		if err != nil {
			return err
		}
		cfg.Args = []string{"--edited"}
		b, _ = json.Marshal(cfg)
		return os.WriteFile(path, b, 0o600)
	}
	var out bytes.Buffer
	if err := runEdit(home, "wiki", &out, editor); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if reloads.Load() != 1 {
		t.Fatalf("reloads = %d, want 1 on the manifest's port", reloads.Load())
	}
	saved, _, err := mcpproxy.LoadServerFile(filepath.Join(aimux, "mcp-servers.json"))
	if err != nil || strings.Join(saved["wiki"].Args, ",") != "--edited" {
		t.Fatalf("saved %+v, err %v", saved["wiki"], err)
	}
	cat, err := mcpproxy.LoadCatalog(filepath.Join(aimux, "mcp-catalog.json"))
	if err != nil || len(cat.Servers["wiki"].Tools) != 2 {
		t.Fatalf("catalog %+v, err %v; want the probed tools", cat.Servers, err)
	}
	if !strings.Contains(out.String(), "Saved the definition") {
		t.Fatalf("output: %s", out.String())
	}
}

// edit probes with the installed agent's PATH and HOME (its plist), not
// this shell's; without a plist it uses this shell's and says so.
func TestEditProbeEnv_FromTheInstalledPlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", "/shell/only/bin:/usr/bin")
	var out bytes.Buffer
	env := editProbeEnv(home, 7381, &out)
	if env["PATH"] != "/shell/only/bin:/usr/bin" || env["HOME"] != home || !strings.Contains(out.String(), "no installed launchd plist") {
		t.Fatalf("without a plist: env %v, output %q", env, out.String())
	}

	a := proxyInstaller(home, 7381, &out).Agent
	a.Binary = "/usr/local/bin/aimux"
	a.Env = map[string]string{"PATH": "/plist/bin:/usr/bin", "HOME": home}
	if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.PlistPath, a.Plist(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	env = editProbeEnv(home, 7381, &out)
	if env["PATH"] != "/plist/bin:/usr/bin" || out.Len() != 0 {
		t.Fatalf("with a plist: env %v, output %q; want the plist's PATH", env, out.String())
	}
}
