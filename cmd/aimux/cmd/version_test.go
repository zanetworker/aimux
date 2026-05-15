package cmd

import (
	"bytes"
	"encoding/json"
	"runtime"
	"testing"
)

func TestVersionCmd_Text(t *testing.T) {
	var stdout bytes.Buffer
	c := newVersionCmd()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"version"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("aimux")) {
		t.Errorf("output missing 'aimux', got %q", stdout.String())
	}
}

func TestVersionCmd_JSON(t *testing.T) {
	var stdout bytes.Buffer
	c := newVersionCmd()
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"version"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if got["os"] != runtime.GOOS {
		t.Errorf("os=%q, want %q", got["os"], runtime.GOOS)
	}
	if got["arch"] != runtime.GOARCH {
		t.Errorf("arch=%q, want %q", got["arch"], runtime.GOARCH)
	}
}
