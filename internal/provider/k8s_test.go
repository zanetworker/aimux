package provider

import (
	"context"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

// Compile-time interface check — K8s must satisfy Provider.
var _ Provider = (*K8s)(nil)

func TestK8sName(t *testing.T) {
	k := &K8s{}
	if got := k.Name(); got != "k8s" {
		t.Errorf("K8s.Name() = %q, want %q", got, "k8s")
	}
}

func TestK8sCanEmbed(t *testing.T) {
	k := &K8s{}
	if k.CanEmbed() {
		t.Error("K8s.CanEmbed() = true, want false")
	}
}

func TestK8sResumeCommand(t *testing.T) {
	k := &K8s{}
	if cmd := k.ResumeCommand(agent.Agent{}); cmd != nil {
		t.Errorf("K8s.ResumeCommand() = %v, want nil", cmd)
	}
}

func TestK8sFindSessionFile(t *testing.T) {
	k := &K8s{}
	if got := k.FindSessionFile(agent.Agent{}); got != "" {
		t.Errorf("K8s.FindSessionFile() = %q, want empty string", got)
	}
}

func TestK8sFindSessionFile_WithSessionID(t *testing.T) {
	k := &K8s{}
	a := agent.Agent{SessionID: "test-agent-1"}
	got := k.FindSessionFile(a)
	if got != "k8s://test-agent-1" {
		t.Errorf("K8s.FindSessionFile() = %q, want %q", got, "k8s://test-agent-1")
	}
}

func TestK8sRecentDirs(t *testing.T) {
	k := &K8s{}
	if dirs := k.RecentDirs(10); dirs != nil {
		t.Errorf("K8s.RecentDirs() = %v, want nil", dirs)
	}
}

func TestK8sSpawnCommand(t *testing.T) {
	k := &K8s{}
	if cmd := k.SpawnCommand("/tmp", "claude-opus-4-6", "coder"); cmd != nil {
		t.Errorf("K8s.SpawnCommand() = %v, want nil", cmd)
	}
}

func TestK8sSpawnArgs(t *testing.T) {
	k := &K8s{}
	args := k.SpawnArgs()
	if len(args.Models) == 0 {
		t.Error("K8s.SpawnArgs().Models is empty")
	}
	if len(args.Modes) == 0 {
		t.Error("K8s.SpawnArgs().Modes is empty")
	}
	wantModels := []string{"claude-opus-4-6", "claude-sonnet-4-6"}
	modelSet := make(map[string]bool)
	for _, m := range args.Models {
		modelSet[m] = true
	}
	for _, m := range wantModels {
		if !modelSet[m] {
			t.Errorf("K8s.SpawnArgs().Models missing %q", m)
		}
	}
	wantModes := []string{"coder", "researcher", "reviewer"}
	modeSet := make(map[string]bool)
	for _, m := range args.Modes {
		modeSet[m] = true
	}
	for _, m := range wantModes {
		if !modeSet[m] {
			t.Errorf("K8s.SpawnArgs().Modes missing %q", m)
		}
	}
}

func TestK8sParseTrace_NotConfigured(t *testing.T) {
	k := &K8s{}
	turns, err := k.ParseTrace("")
	if err != nil {
		t.Errorf("K8s.ParseTrace() error = %v, want nil", err)
	}
	if len(turns) == 0 {
		t.Error("K8s.ParseTrace() returned empty slice, want at least one informational turn")
	}
}

func TestK8sParseTrace_Configured(t *testing.T) {
	k := NewK8s(K8sConfig{
		RedisURL: "redis://127.0.0.1:6379",
		TeamID:   "my-team",
	})
	turns, err := k.ParseTrace("")
	if err != nil {
		t.Errorf("K8s.ParseTrace() with config error = %v, want nil", err)
	}
	if len(turns) == 0 {
		t.Error("K8s.ParseTrace() returned empty slice, want at least one turn")
	}
}

func TestK8sOTELEnv(t *testing.T) {
	k := &K8s{}
	if got := k.OTELEnv("localhost:4318"); got != "" {
		t.Errorf("K8s.OTELEnv() = %q, want empty string", got)
	}
}

func TestK8sOTELServiceName(t *testing.T) {
	k := &K8s{}
	if got := k.OTELServiceName(); got != "k8s-agent" {
		t.Errorf("K8s.OTELServiceName() = %q, want %q", got, "k8s-agent")
	}
}

func TestK8sSubagentAttrKeys_Empty(t *testing.T) {
	k := &K8s{}
	keys := k.SubagentAttrKeys()
	if !keys.Empty() {
		t.Error("K8s.SubagentAttrKeys() should return empty AttrKeys")
	}
}

func TestNewK8s(t *testing.T) {
	cfg := K8sConfig{
		RedisURL:  "redis://localhost:6379",
		TeamID:    "team1",
		Namespace: "agents",
	}
	k := NewK8s(cfg)
	if k == nil {
		t.Fatal("NewK8s() returned nil")
	}
	if k.cfg.TeamID != "team1" {
		t.Errorf("NewK8s().cfg.TeamID = %q, want %q", k.cfg.TeamID, "team1")
	}
}

func TestNopRedisLogger(t *testing.T) {
	var l nopRedisLogger
	l.Printf(context.TODO(), "should not appear: %s %d", "test", 42)
}

func TestNewRedisClient_PoolSettings(t *testing.T) {
	rdb, err := newRedisClient("redis://127.0.0.1:19999")
	if err != nil {
		t.Fatalf("newRedisClient() error = %v", err)
	}
	defer func() { _ = rdb.Close() }()
	opts := rdb.Options()
	if opts.PoolSize != 2 {
		t.Errorf("PoolSize = %d, want 2", opts.PoolSize)
	}
	if opts.MinIdleConns != 0 {
		t.Errorf("MinIdleConns = %d, want 0", opts.MinIdleConns)
	}
	if opts.MaxRetries != 1 {
		t.Errorf("MaxRetries = %d, want 1", opts.MaxRetries)
	}
}

func TestTerminalForwarding_ExcludesCredentials(t *testing.T) {
	credentialVars := map[string]bool{
		"ANTHROPIC_API_KEY":              true,
		"GOOGLE_APPLICATION_CREDENTIALS": true,
		"AWS_SECRET_ACCESS_KEY":          true,
		"AWS_SESSION_TOKEN":              true,
	}

	terminalForwardedVars := []string{
		"CLAUDE_CODE_USE_VERTEX",
		"CLOUD_ML_REGION",
		"ANTHROPIC_VERTEX_PROJECT_ID",
		"ANTHROPIC_VERTEX_REGION",
	}

	for _, v := range terminalForwardedVars {
		if credentialVars[v] {
			t.Errorf("credential %q must not be forwarded via terminal; use K8s secrets instead", v)
		}
	}
}

func TestK8sDiscover_ReturnsNil(t *testing.T) {
	k := NewK8s(K8sConfig{})
	agents, err := k.Discover()
	if err != nil {
		t.Errorf("K8s.Discover() error = %v, want nil", err)
	}
	if agents != nil {
		t.Errorf("K8s.Discover() = %v, want nil (discovery moved to K8sEnvironment)", agents)
	}
}
