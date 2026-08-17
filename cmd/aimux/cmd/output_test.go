package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestExitCodeConstants(t *testing.T) {
	if ExitSuccess != 0 {
		t.Errorf("ExitSuccess = %d, want 0", ExitSuccess)
	}
	if ExitError != 1 {
		t.Errorf("ExitError = %d, want 1", ExitError)
	}
	if ExitUsage != 2 {
		t.Errorf("ExitUsage = %d, want 2", ExitUsage)
	}
	if ExitNotFound != 3 {
		t.Errorf("ExitNotFound = %d, want 3", ExitNotFound)
	}
	if ExitConfig != 4 {
		t.Errorf("ExitConfig = %d, want 4", ExitConfig)
	}
}

func TestOutputWriter_JSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w := &OutputWriter{JSON: true, Stdout: &stdout, Stderr: &stderr}

	data := map[string]string{"key": "value"}
	code := w.WriteResult(data)
	if code != ExitSuccess {
		t.Errorf("WriteResult returned %d, want %d", code, ExitSuccess)
	}

	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("got key=%q, want %q", got["key"], "value")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", stderr.String())
	}
}

func TestOutputWriter_WriteError_JSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w := &OutputWriter{JSON: true, Stdout: &stdout, Stderr: &stderr}

	code := w.WriteError("invalid provider", ExitUsage, map[string]any{
		"valid_values": []string{"claude", "codex"},
	})
	if code != ExitUsage {
		t.Errorf("WriteError returned %d, want %d", code, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty for errors, got %q", stdout.String())
	}

	var errObj map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &errObj); err != nil {
		t.Fatalf("stderr is not valid JSON: %v", err)
	}
	if errObj["error"] != "invalid provider" {
		t.Errorf("error=%q, want %q", errObj["error"], "invalid provider")
	}
	if errObj["code"].(float64) != float64(ExitUsage) {
		t.Errorf("code=%v, want %d", errObj["code"], ExitUsage)
	}
}

func TestOutputWriter_WriteError_Text(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w := &OutputWriter{JSON: false, Stdout: &stdout, Stderr: &stderr}

	code := w.WriteError("invalid provider \"gpt\"", ExitUsage, map[string]any{
		"valid_values": []string{"claude", "codex"},
	})
	if code != ExitUsage {
		t.Errorf("WriteError returned %d, want %d", code, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty for errors")
	}
	got := stderr.String()
	if !bytes.Contains(stderr.Bytes(), []byte("invalid provider")) {
		t.Errorf("stderr missing error message, got %q", got)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("claude, codex")) {
		t.Errorf("stderr missing valid values, got %q", got)
	}
}
