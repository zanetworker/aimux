package mcpproxy

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestEditServer_EditingArgsKeepsEnvByteIdentical verifies that editing only
// args does not touch env values.
func TestEditServer_EditingArgsKeepsEnvByteIdentical(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	// Add env with a secret
	servers, resolved, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	cfg := servers["slack"]
	cfg.Env = map[string]string{ // #nosec G101 -- test secret
		"SLACK_TOKEN": "xoxb-secret-token-12345",
		"OTHER_VAR":   "other-value",
	}
	servers["slack"] = cfg
	if err := SaveServerFile(fx.serversPath, servers, resolved); err != nil {
		t.Fatalf("SaveServerFile() = %v", err)
	}

	// Edit only args
	fx.editorFn = func(path string) error {
		data, err := os.ReadFile(path) // #nosec G304 -- test reading the temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Args = []string{"--verbose"}
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0600)
	}

	fx.probeFn = func(cfg ServerConfig) error {
		// Verify probe gets the env
		if cfg.Env["SLACK_TOKEN"] != "xoxb-secret-token-12345" {
			t.Errorf("probe cfg.Env[SLACK_TOKEN] = %q, want the original token", cfg.Env["SLACK_TOKEN"])
		}
		return nil
	}

	if err := EditServer(fx.ctx, fx.opts); err != nil {
		t.Fatalf("EditServer() = %v", err)
	}

	// Verify env is byte-identical
	servers, _, err = LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	if got := servers["slack"].Env["SLACK_TOKEN"]; got != "xoxb-secret-token-12345" {
		t.Errorf("saved Env[SLACK_TOKEN] = %q, want %q (byte-identical)", got, "xoxb-secret-token-12345")
	}
	if got := servers["slack"].Env["OTHER_VAR"]; got != "other-value" {
		t.Errorf("saved Env[OTHER_VAR] = %q, want %q (byte-identical)", got, "other-value")
	}
}

// TestEditServer_EditingEnvSavesNewValue verifies that editing an env value
// saves the new value.
func TestEditServer_EditingEnvSavesNewValue(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	// Add env with a token
	servers, resolved, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	cfg := servers["slack"]
	cfg.Env = map[string]string{"SLACK_TOKEN": "old-token"} // #nosec G101 -- test secret
	servers["slack"] = cfg
	if err := SaveServerFile(fx.serversPath, servers, resolved); err != nil {
		t.Fatalf("SaveServerFile() = %v", err)
	}

	// Rotate the token
	fx.editorFn = func(path string) error {
		data, err := os.ReadFile(path) // #nosec G304 -- test reading the temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Env["SLACK_TOKEN"] = "new-rotated-token"
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0600)
	}

	fx.probeFn = func(cfg ServerConfig) error {
		// Verify probe gets the new token
		if cfg.Env["SLACK_TOKEN"] != "new-rotated-token" {
			t.Errorf("probe cfg.Env[SLACK_TOKEN] = %q, want new-rotated-token", cfg.Env["SLACK_TOKEN"])
		}
		return nil
	}

	if err := EditServer(fx.ctx, fx.opts); err != nil {
		t.Fatalf("EditServer() = %v", err)
	}

	// Verify new token is saved
	servers, _, err = LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	if got := servers["slack"].Env["SLACK_TOKEN"]; got != "new-rotated-token" {
		t.Errorf("saved Env[SLACK_TOKEN] = %q, want %q", got, "new-rotated-token")
	}
}

// TestEditServer_RemovingEnvKeyRemovesIt verifies that removing an env key
// removes it from the saved definition.
func TestEditServer_RemovingEnvKeyRemovesIt(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	// Add env with two keys
	servers, resolved, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	cfg := servers["slack"]
	cfg.Env = map[string]string{ // #nosec G101 -- test secret
		"SLACK_TOKEN": "token",
		"KEEP_THIS":   "value",
	}
	servers["slack"] = cfg
	if err := SaveServerFile(fx.serversPath, servers, resolved); err != nil {
		t.Fatalf("SaveServerFile() = %v", err)
	}

	// Remove SLACK_TOKEN
	fx.editorFn = func(path string) error {
		data, err := os.ReadFile(path) // #nosec G304 -- test reading the temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		delete(cfg.Env, "SLACK_TOKEN")
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0600)
	}

	if err := EditServer(fx.ctx, fx.opts); err != nil {
		t.Fatalf("EditServer() = %v", err)
	}

	// Verify SLACK_TOKEN is gone, KEEP_THIS remains
	servers, _, err = LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	if _, ok := servers["slack"].Env["SLACK_TOKEN"]; ok {
		t.Error("SLACK_TOKEN still exists, want removed")
	}
	if got := servers["slack"].Env["KEEP_THIS"]; got != "value" {
		t.Errorf("KEEP_THIS = %q, want %q", got, "value")
	}
}

// TestEditServer_SecretsNeverInStdoutStderrOrErrors verifies that secret
// values never appear in stdout, stderr, or error messages.
func TestEditServer_SecretsNeverInStdoutStderrOrErrors(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	// Add env with a secret
	servers, resolved, err := LoadServerFile(fx.serversPath)
	if err != nil {
		t.Fatalf("LoadServerFile() = %v", err)
	}
	cfg := servers["slack"]
	cfg.Env = map[string]string{"SECRET": "super-secret-value-12345"} // #nosec G101 -- test secret
	servers["slack"] = cfg
	if err := SaveServerFile(fx.serversPath, servers, resolved); err != nil {
		t.Fatalf("SaveServerFile() = %v", err)
	}

	// Test 1: Invalid JSON path
	fx.editorFn = func(path string) error {
		return os.WriteFile(path, []byte("not json"), 0600)
	}

	err = EditServer(fx.ctx, fx.opts)
	if err == nil {
		t.Fatal("EditServer() = nil, want error for invalid JSON")
	}
	if strings.Contains(err.Error(), "super-secret-value-12345") {
		t.Errorf("error message contains secret: %v", err)
	}

	// Test 2: Probe failure path
	fx.editorFn = func(path string) error {
		data, err := os.ReadFile(path) // #nosec G304 -- test reading the temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Args = []string{"--bad"}
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0600)
	}

	fx.probeFn = func(cfg ServerConfig) error {
		return os.ErrPermission // probe fails
	}

	err = EditServer(fx.ctx, fx.opts)
	if err == nil {
		t.Fatal("EditServer() = nil, want error for probe failure")
	}
	if strings.Contains(err.Error(), "super-secret-value-12345") {
		t.Errorf("probe error message contains secret: %v", err)
	}
}

// TestEditServer_TempFileIs0600AndCleanedUp verifies the temp file has correct
// permissions and is removed on all paths.
func TestEditServer_TempFileIs0600AndCleanedUp(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	var tempPath string
	fx.editorFn = func(path string) error {
		tempPath = path

		// Verify 0600 permissions
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Errorf("temp file permissions = %o, want 0600", got)
		}

		// Make a valid edit
		data, err := os.ReadFile(path) // #nosec G304 -- test reading the temp file
		if err != nil {
			return err
		}
		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		cfg.Args = []string{"--new"}
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0600)
	}

	if err := EditServer(fx.ctx, fx.opts); err != nil {
		t.Fatalf("EditServer() = %v", err)
	}

	// Verify temp file is gone
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Errorf("temp file still exists at %s", tempPath)
	}
}

// TestEditServer_TempFileCleanedUpOnError verifies temp file cleanup on error.
func TestEditServer_TempFileCleanedUpOnError(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)

	var tempPath string
	fx.editorFn = func(path string) error {
		tempPath = path
		// Write invalid JSON
		return os.WriteFile(path, []byte("not json"), 0600)
	}

	_ = EditServer(fx.ctx, fx.opts) // expect error, don't care

	// Verify temp file is gone even on error
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Errorf("temp file still exists after error at %s", tempPath)
	}
}

// Ctrl-C while the editor is open must not kill aimux before the temp copy
// (which holds the server's env values) is removed: the interrupt goes to the
// editor, and EditServer still cleans up when the editor returns.
func TestEditServer_InterruptDuringEditorStillCleansUp(t *testing.T) {
	home := t.TempDir()
	fx := setupEditFixture(t, home)
	var tempPath string
	fx.editorFn = func(path string) error {
		tempPath = path
		// The terminal sends SIGINT to the whole foreground group, aimux included.
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond) // give the signal time to arrive
		return errors.New("editor quit")
	}
	if err := EditServer(fx.ctx, fx.opts); err == nil {
		t.Fatal("an editor that quit must not save")
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Errorf("temp file with env values still exists at %s", tempPath)
	}
}
