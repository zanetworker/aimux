package deliver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliver_Stdout(t *testing.T) {
	err := Deliver([]byte(`{"test":true}`), "stdout")
	if err != nil {
		t.Errorf("stdout delivery failed: %v", err)
	}
}

func TestDeliver_Empty(t *testing.T) {
	err := Deliver([]byte(`{"test":true}`), "")
	if err != nil {
		t.Errorf("empty target should default to stdout: %v", err)
	}
}

func TestDeliver_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	err := Deliver([]byte(`{"delivered":true}`), "file:"+path)
	if err != nil {
		t.Fatalf("file delivery failed: %v", err)
	}

	data, err := os.ReadFile(path) // #nosec G304 -- test-only, path from t.TempDir()
	if err != nil {
		t.Fatalf("read delivered file: %v", err)
	}
	if string(data) != `{"delivered":true}` {
		t.Errorf("file content=%q, want %q", data, `{"delivered":true}`)
	}
}

func TestDeliver_FileCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "out.json")

	err := Deliver([]byte(`{}`), "file:"+path)
	if err != nil {
		t.Fatalf("file delivery with nested dir failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should exist: %v", err)
	}
}

func TestDeliver_Webhook(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		received = buf[:n]
		w.WriteHeader(200)
	}))
	defer srv.Close()

	err := Deliver([]byte(`{"hook":true}`), "webhook:"+srv.URL)
	if err != nil {
		t.Fatalf("webhook delivery failed: %v", err)
	}
	if string(received) != `{"hook":true}` {
		t.Errorf("webhook received=%q, want %q", received, `{"hook":true}`)
	}
}

func TestDeliver_WebhookError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	err := Deliver([]byte(`{}`), "webhook:"+srv.URL)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got: %s", err.Error())
	}
}

func TestDeliver_InvalidScheme(t *testing.T) {
	err := Deliver([]byte(`{}`), "s3:bucket/key")
	if err == nil {
		t.Fatal("expected error for invalid scheme")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "stdout") || !strings.Contains(errStr, "file:") || !strings.Contains(errStr, "webhook:") {
		t.Errorf("error should list valid schemes, got: %s", errStr)
	}
}
