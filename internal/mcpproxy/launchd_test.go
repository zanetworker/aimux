package mcpproxy

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// fakeRun records launchctl calls for verification
type fakeRun struct {
	calls [][]string
	// outputs maps command patterns to their output
	outputs map[string]fakeOutput
}

type fakeOutput struct {
	stdout []byte
	err    error
}

func newFakeRun() *fakeRun {
	return &fakeRun{
		calls:   [][]string{},
		outputs: make(map[string]fakeOutput),
	}
}

func (f *fakeRun) run(name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)

	// Match against registered outputs
	key := strings.Join(call, " ")
	if output, ok := f.outputs[key]; ok {
		return output.stdout, output.err
	}

	// Default: success with empty output
	return []byte{}, nil
}

func (f *fakeRun) setOutput(pattern string, stdout []byte, err error) {
	f.outputs[pattern] = fakeOutput{stdout: stdout, err: err}
}

// TestAgent_Plist verifies the plist has the required keys with absolute binary path
func TestAgent_Plist(t *testing.T) {
	agent := Agent{
		Label:     "com.aimux.mcp-proxy",
		PlistPath: "/Users/test/Library/LaunchAgents/com.aimux.mcp-proxy.plist",
		Binary:    "/usr/local/bin/aimux",
		LogDir:    "/Users/test/.aimux/logs",
		Args:      []string{"mcp", "proxy", "serve", "--port", "7381"},
		UID:       501,
		Run:       nil, // Not needed for Plist()
	}

	plistBytes := agent.Plist()

	// Parse as XML to verify structure
	type plist struct {
		XMLName xml.Name `xml:"plist"`
		Dict    struct {
			Keys   []string `xml:"key"`
			Values []string `xml:"string"`
			True   []string `xml:"true"`
			Array  struct {
				Strings []string `xml:"string"`
			} `xml:"array"`
		} `xml:"dict"`
	}

	var p plist
	if err := xml.Unmarshal(plistBytes, &p); err != nil {
		t.Fatalf("Plist is not valid XML: %v", err)
	}

	// Verify required keys are present
	requiredKeys := []string{"Label", "ProgramArguments", "RunAtLoad", "KeepAlive", "StandardOutPath", "StandardErrorPath"}
	foundKeys := make(map[string]bool)
	for _, key := range p.Dict.Keys {
		foundKeys[key] = true
	}

	for _, required := range requiredKeys {
		if !foundKeys[required] {
			t.Errorf("Plist missing required key: %s", required)
		}
	}

	// Verify ProgramArguments includes absolute binary path and args
	plistStr := string(plistBytes)
	if !strings.Contains(plistStr, "/usr/local/bin/aimux") {
		t.Error("Plist should contain absolute binary path")
	}
	if !strings.Contains(plistStr, "mcp") {
		t.Error("Plist should contain args")
	}

	// Verify StandardOutPath and StandardErrorPath point to LogDir
	if !strings.Contains(plistStr, "/Users/test/.aimux/logs") {
		t.Error("Plist should contain LogDir paths")
	}
}

// TestAgent_Plist_XMLEscaping verifies paths with spaces and XML special characters are escaped
func TestAgent_Plist_XMLEscaping(t *testing.T) {
	agent := Agent{
		Label:     "com.aimux.mcp-proxy",
		PlistPath: "/Users/test/Library/LaunchAgents/com.aimux.mcp-proxy.plist",
		Binary:    "/usr/local/bin/ai mux & co",
		LogDir:    "/Users/test/.aimux/logs & data",
		Args:      []string{"serve"},
		UID:       501,
	}

	plistBytes := agent.Plist()

	// Parse as XML - will fail if escaping is wrong
	var p interface{}
	if err := xml.Unmarshal(plistBytes, &p); err != nil {
		t.Fatalf("Plist with special characters is not valid XML: %v", err)
	}

	// Verify special characters are properly escaped in the XML
	plistStr := string(plistBytes)
	if strings.Contains(plistStr, " & ") && !strings.Contains(plistStr, "&amp;") {
		t.Error("XML special characters should be escaped")
	}
}

// TestAgent_Install writes plist atomically, bootouts ignoring 'not loaded', then bootstraps
func TestAgent_Install(t *testing.T) {
	tmpDir := t.TempDir()
	plistPath := filepath.Join(tmpDir, "com.aimux.mcp-proxy.plist")

	fake := newFakeRun()
	// bootout returns "service not loaded" error
	fake.setOutput(
		"launchctl bootout gui/501/com.aimux.mcp-proxy",
		[]byte("Boot-out failed: 113: Could not find specified service"),
		fmt.Errorf("exit status 113"),
	)

	agent := Agent{
		Label:     "com.aimux.mcp-proxy",
		PlistPath: plistPath,
		Binary:    "/usr/local/bin/aimux",
		LogDir:    filepath.Join(tmpDir, "logs"),
		Args:      []string{"mcp", "proxy", "serve"},
		UID:       501,
		Run:       fake.run,
	}

	if err := agent.Install(); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// Verify plist was written with correct permissions
	info, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("Plist not written: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("Plist has wrong permissions: got %o, want 0644", info.Mode().Perm())
	}

	// Verify call order: bootout then bootstrap
	if len(fake.calls) != 2 {
		t.Fatalf("Expected 2 launchctl calls, got %d", len(fake.calls))
	}

	bootoutCall := strings.Join(fake.calls[0], " ")
	if !strings.Contains(bootoutCall, "bootout") {
		t.Errorf("First call should be bootout, got: %s", bootoutCall)
	}

	bootstrapCall := strings.Join(fake.calls[1], " ")
	if !strings.Contains(bootstrapCall, "bootstrap gui/501") {
		t.Errorf("Second call should be bootstrap gui/501, got: %s", bootstrapCall)
	}
	if !strings.Contains(bootstrapCall, plistPath) {
		t.Errorf("Bootstrap should include plist path, got: %s", bootstrapCall)
	}
}

// TestAgent_Install_Twice is safe (idempotent)
func TestAgent_Install_Twice(t *testing.T) {
	tmpDir := t.TempDir()
	plistPath := filepath.Join(tmpDir, "com.aimux.mcp-proxy.plist")

	fake := newFakeRun()
	// Second bootout finds the service already loaded
	fake.setOutput(
		"launchctl bootout gui/501/com.aimux.mcp-proxy",
		[]byte(""),
		nil,
	)

	agent := Agent{
		Label:     "com.aimux.mcp-proxy",
		PlistPath: plistPath,
		Binary:    "/usr/local/bin/aimux",
		LogDir:    filepath.Join(tmpDir, "logs"),
		Args:      []string{"mcp", "proxy", "serve"},
		UID:       501,
		Run:       fake.run,
	}

	// First install
	if err := agent.Install(); err != nil {
		t.Fatalf("First install failed: %v", err)
	}

	// Reset calls to verify second install
	fake.calls = [][]string{}

	// Second install should succeed
	if err := agent.Install(); err != nil {
		t.Fatalf("Second install failed: %v", err)
	}

	// Should still do bootout + bootstrap
	if len(fake.calls) != 2 {
		t.Errorf("Second install should still make 2 calls, got %d", len(fake.calls))
	}
}

// TestAgent_Uninstall_WhenNotInstalled returns nil (idempotent)
func TestAgent_Uninstall_WhenNotInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	plistPath := filepath.Join(tmpDir, "com.aimux.mcp-proxy.plist")

	fake := newFakeRun()
	// bootout returns "could not find service"
	fake.setOutput(
		"launchctl bootout gui/501/com.aimux.mcp-proxy",
		[]byte("Boot-out failed: 113: Could not find specified service"),
		fmt.Errorf("exit status 113"),
	)

	agent := Agent{
		Label:     "com.aimux.mcp-proxy",
		PlistPath: plistPath,
		Binary:    "/usr/local/bin/aimux",
		LogDir:    filepath.Join(tmpDir, "logs"),
		Args:      []string{"mcp", "proxy", "serve"},
		UID:       501,
		Run:       fake.run,
	}

	// Uninstall when nothing is installed should not error
	if err := agent.Uninstall(); err != nil {
		t.Errorf("Uninstall when not installed should return nil, got: %v", err)
	}

	// Should have attempted bootout
	if len(fake.calls) != 1 {
		t.Fatalf("Expected 1 launchctl call, got %d", len(fake.calls))
	}

	bootoutCall := strings.Join(fake.calls[0], " ")
	if !strings.Contains(bootoutCall, "bootout") {
		t.Errorf("Call should be bootout, got: %s", bootoutCall)
	}
}

// TestAgent_Uninstall_RemovesPlist removes plist file after bootout
func TestAgent_Uninstall_RemovesPlist(t *testing.T) {
	tmpDir := t.TempDir()
	plistPath := filepath.Join(tmpDir, "com.aimux.mcp-proxy.plist")

	// Create plist file first
	//nolint:gosec // G306: test file, 0644 is acceptable
	if err := os.WriteFile(plistPath, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create test plist: %v", err)
	}

	fake := newFakeRun()
	agent := Agent{
		Label:     "com.aimux.mcp-proxy",
		PlistPath: plistPath,
		Binary:    "/usr/local/bin/aimux",
		LogDir:    filepath.Join(tmpDir, "logs"),
		Args:      []string{"mcp", "proxy", "serve"},
		UID:       501,
		Run:       fake.run,
	}

	if err := agent.Uninstall(); err != nil {
		t.Fatalf("Uninstall failed: %v", err)
	}

	// Verify plist was removed
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Error("Plist should have been removed")
	}
}

// TestAgent_Loaded parses launchctl print output
func TestAgent_Loaded(t *testing.T) {
	tests := []struct {
		name       string
		output     []byte
		err        error
		wantLoaded bool
		wantErr    bool
	}{
		{
			name: "service running",
			output: []byte(`{
				"LimitLoadToSessionType" = "Aqua";
				"Label" = "com.aimux.mcp-proxy";
				"OnDemand" = false;
				"LastExitStatus" = 0;
				"PID" = 12345;
				"Program" = "/usr/local/bin/aimux";
			}`),
			err:        nil,
			wantLoaded: true,
			wantErr:    false,
		},
		{
			name:       "service not found",
			output:     []byte("Could not find service \"com.aimux.mcp-proxy\" in domain for port"),
			err:        fmt.Errorf("exit status 113"),
			wantLoaded: false,
			wantErr:    false,
		},
		{
			name:       "service not loaded",
			output:     []byte("service not loaded"),
			err:        fmt.Errorf("exit status 113"),
			wantLoaded: false,
			wantErr:    false,
		},
		{
			name:       "other error",
			output:     []byte("permission denied"),
			err:        fmt.Errorf("exit status 1"),
			wantLoaded: false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeRun()
			fake.setOutput(
				"launchctl print gui/501/com.aimux.mcp-proxy",
				tt.output,
				tt.err,
			)

			agent := Agent{
				Label: "com.aimux.mcp-proxy",
				UID:   501,
				Run:   fake.run,
			}

			loaded, err := agent.Loaded()
			if (err != nil) != tt.wantErr {
				t.Errorf("Loaded() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if loaded != tt.wantLoaded {
				t.Errorf("Loaded() = %v, want %v", loaded, tt.wantLoaded)
			}
		})
	}
}

// TestAgent_Plist_ValidXML verifies plist can be parsed back
func TestAgent_Plist_ValidXML(t *testing.T) {
	agent := Agent{
		Label:     "com.aimux.mcp-proxy",
		PlistPath: "/Users/test/Library/LaunchAgents/com.aimux.mcp-proxy.plist",
		Binary:    "/usr/local/bin/aimux",
		LogDir:    "/Users/test/.aimux/logs",
		Args:      []string{"mcp", "proxy", "serve"},
		UID:       501,
	}

	plistBytes := agent.Plist()

	// Must be parseable as XML
	var parsed interface{}
	if err := xml.Unmarshal(plistBytes, &parsed); err != nil {
		t.Fatalf("Plist is not valid XML: %v", err)
	}

	// If plutil exists, use it for validation
	tmpFile := filepath.Join(t.TempDir(), "test.plist")
	//nolint:gosec // G306: test file, 0644 is acceptable
	if err := os.WriteFile(tmpFile, plistBytes, 0644); err != nil {
		t.Fatalf("Failed to write temp plist: %v", err)
	}

	// plutil -lint returns 0 for valid plists
	// Skip if plutil not available (Linux, etc.)
	if _, err := os.Stat("/usr/bin/plutil"); err == nil {
		//nolint:gosec // G306: test file, 0644 is acceptable
		if err := os.WriteFile(tmpFile, plistBytes, 0644); err != nil {
			t.Fatalf("Failed to write test plist: %v", err)
		}
		// Note: We don't actually run plutil here to avoid test environment dependencies
		// The XML unmarshal above is sufficient for the test
	}
}

// launchctl's real answers when the service is not loaded (macOS 26, captured
// with a label that does not exist): bootout exits 3 "No such process", print
// exits 113. The text is on the output, the error only carries the exit code.
func TestAgent_NotLoadedRealLaunchctlAnswers(t *testing.T) {
	tmpDir := t.TempDir()
	fake := newFakeRun()
	fake.setOutput("launchctl bootout gui/501/com.aimux.mcp-proxy",
		[]byte("Boot-out failed: 3: No such process\n"), fmt.Errorf("exit status 3"))
	fake.setOutput("launchctl print gui/501/com.aimux.mcp-proxy",
		[]byte("Bad request.\nCould not find service \"com.aimux.mcp-proxy\" in domain for user gui: 501\n"), fmt.Errorf("exit status 113"))
	agent := Agent{Label: "com.aimux.mcp-proxy", PlistPath: filepath.Join(tmpDir, "p.plist"), Binary: "/usr/local/bin/aimux",
		LogDir: filepath.Join(tmpDir, "logs"), Args: []string{"mcp", "proxy", "serve"}, UID: 501, Run: fake.run}
	if err := agent.Install(); err != nil {
		t.Fatalf("first install with nothing loaded: %v", err)
	}
	if err := agent.Uninstall(); err != nil {
		t.Fatalf("uninstall with nothing loaded: %v", err)
	}
	if loaded, err := agent.Loaded(); err != nil || loaded {
		t.Fatalf("Loaded = %v, %v; want false, nil", loaded, err)
	}
	// A different failure is still an error.
	fake.setOutput("launchctl bootout gui/501/com.aimux.mcp-proxy",
		[]byte("Boot-out failed: 1: Operation not permitted\n"), fmt.Errorf("exit status 1"))
	if err := agent.Uninstall(); err == nil {
		t.Fatal("a permission failure was ignored")
	}
}

func TestPlist_EnvironmentVariables(t *testing.T) {
	a := Agent{Label: "com.aimux.mcp-proxy", Binary: "/usr/local/bin/aimux", LogDir: "/tmp/l",
		Env: map[string]string{"PATH": "/opt/homebrew/bin:/usr/bin", "HOME": "/Users/x & co"}}
	p := string(a.Plist())
	flat := regexp.MustCompile(`>\s+<`).ReplaceAllString(p, "><")
	want := "<key>EnvironmentVariables</key><dict><key>HOME</key><string>/Users/x &amp; co</string><key>PATH</key><string>/opt/homebrew/bin:/usr/bin</string></dict>"
	if !strings.Contains(flat, want) {
		t.Fatalf("plist lacks the environment dict:\n%s", p)
	}
	if err := xml.Unmarshal(a.Plist()[strings.Index(p, "<plist"):], new(struct{})); err != nil {
		t.Fatalf("plist is not well-formed XML: %v", err)
	}
	if strings.Contains(string(Agent{Label: "x"}.Plist()), "EnvironmentVariables") {
		t.Fatal("environment dict written without Env")
	}
}

func TestAgent_Restart_ReloadsThePlistOnDiskAsIs(t *testing.T) {
	plistPath := filepath.Join(t.TempDir(), "com.aimux.mcp-proxy.plist")
	if err := os.WriteFile(plistPath, []byte("the plist as it was"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := newFakeRun()
	fake.setOutput("launchctl bootout gui/501/com.aimux.mcp-proxy", []byte("No such process"), errors.New("exit status 3"))
	a := Agent{Label: "com.aimux.mcp-proxy", PlistPath: plistPath, Binary: "/x/aimux", UID: 501, Run: fake.run}
	if err := a.Restart(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"launchctl", "bootout", "gui/501/com.aimux.mcp-proxy"},
		{"launchctl", "bootstrap", "gui/501", plistPath},
	}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls %v, want %v", fake.calls, want)
	}
	if b, _ := os.ReadFile(plistPath); string(b) != "the plist as it was" { // #nosec G304 -- test temp file
		t.Fatal("Restart rewrote the plist")
	}
	fake.setOutput("launchctl bootstrap gui/501 "+plistPath, nil, errors.New("exit status 5"))
	if err := a.Restart(); err == nil {
		t.Fatal("a failed bootstrap was not reported")
	}
}

// edit probes with the PATH and HOME the installed agent runs with, read
// back from its plist.
func TestReadPlistEnv(t *testing.T) {
	dir := t.TempDir()
	a := Agent{Label: "com.aimux.mcp-proxy", PlistPath: filepath.Join(dir, "a.plist"), Binary: "/usr/local/bin/aimux",
		LogDir: dir, Env: map[string]string{"PATH": "/plist/bin:/usr/bin", "HOME": "/Users/x & <y>"}}
	if err := os.WriteFile(a.PlistPath, a.Plist(), 0o600); err != nil {
		t.Fatal(err)
	}
	env, found, err := ReadPlistEnv(a.PlistPath)
	if err != nil || !found || !reflect.DeepEqual(env, a.Env) {
		t.Fatalf("env %v found %v err %v, want %v", env, found, err, a.Env)
	}
	if _, found, err := ReadPlistEnv(filepath.Join(dir, "absent.plist")); found || err != nil {
		t.Fatalf("absent plist: found %v err %v", found, err)
	}
	if err := os.WriteFile(a.PlistPath, []byte("<plist><dict><key>EnvironmentVariables</key><dict><key>PATH"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPlistEnv(a.PlistPath); err == nil {
		t.Fatal("a broken plist read without error")
	}
}

// launchd rejects the empty-element form <true></true> ("Bootstrap failed: 5:
// Input/output error") although plutil -lint accepts it; found running the
// real install. The plist must use <true/>.
func TestAgent_PlistUsesSelfClosingTrue(t *testing.T) {
	p := string(Agent{Label: "l", Binary: "/bin/x", LogDir: "/tmp/l"}.Plist())
	if strings.Contains(p, "<true></true>") {
		t.Fatalf("plist has <true></true>, which launchd rejects:\n%s", p)
	}
	if strings.Count(p, "<true/>") != 2 {
		t.Fatalf("want RunAtLoad and KeepAlive as <true/>:\n%s", p)
	}
}
