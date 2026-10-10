package cmd

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/search"
)

func TestBuildAgentsServer_MissingRedisURL(t *testing.T) {
	_, err := buildAgentsServer(agentsFlags{backend: "k8s"}, &config.Config{})
	if err == nil || !strings.Contains(err.Error(), "redis") {
		t.Errorf("err = %v, want one mentioning redis", err)
	}
}

func TestMCPServeCmd_AgentsFlag(t *testing.T) {
	f := newMCPServeCmd().Flags().Lookup("agents")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("--agents flag = %+v, want a bool defaulting to false", f)
	}
}

func testService(t *testing.T) *search.Service {
	t.Helper()
	return &search.Service{DBPath: filepath.Join(t.TempDir(), "search.db"), ProjectsDir: t.TempDir()}
}

func toolNames(srv *mcplib.MCPServer) string {
	var names []string
	for name := range srv.ListTools() {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

const sessionTools = "continue_session,create_virtual_session,get_session,list_sessions,search_sessions"

func TestNewServeMCP_SessionsOnly(t *testing.T) {
	var notes bytes.Buffer
	srv := newServeMCP(false, agentsFlags{}, &config.Config{}, testService(t), &notes)
	if got := toolNames(srv); got != sessionTools {
		t.Errorf("tools = %s, want %s", got, sessionTools)
	}
}

func TestNewServeMCP_AgentsFailureKeepsSessions(t *testing.T) {
	var notes bytes.Buffer
	srv := newServeMCP(true, agentsFlags{backend: "k8s"}, &config.Config{}, testService(t), &notes)
	if got := toolNames(srv); got != sessionTools {
		t.Errorf("tools = %s, want only the session tools", got)
	}
	if !strings.Contains(notes.String(), "agents tools disabled") {
		t.Errorf("notes = %q, want the agents failure explained", notes.String())
	}
}

func TestSessionMCP_NoClusterDeps(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/zanetworker/aimux/internal/sessionmcp").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	bad := regexp.MustCompile(`internal/mcpserver|k8s.io/|redis`)
	for _, line := range strings.Split(string(out), "\n") {
		if bad.MatchString(line) {
			t.Errorf("sessionmcp depends on %s", line)
		}
	}
}

func TestMCPServeCmd_Flags(t *testing.T) {
	cmd := newMCPServeCmd()

	flags := []string{
		"redis-url",
		"kubeconfig",
		"namespace",
		"team-id",
		"max-agents",
		"max-cost",
	}

	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag --%s on serve subcommand", name)
		}
	}
}
