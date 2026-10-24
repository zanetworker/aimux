package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

type stubBackend struct{ tools []json.RawMessage }

func (s stubBackend) ListToolsRaw(context.Context) ([]json.RawMessage, error) { return s.tools, nil }
func (s stubBackend) CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}
func (s stubBackend) Close() error { return nil }

func TestProxyRefresh_WritesCatalogAndReports(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "catalog.json")
	start := func(ctx context.Context, server string) (mcpproxy.Backend, error) {
		if server == "broken" {
			return nil, errors.New("exec: not found")
		}
		return stubBackend{tools: []json.RawMessage{json.RawMessage(`{"name":"a"}`), json.RawMessage(`{"name":"b"}`)}}, nil
	}
	var out bytes.Buffer
	err := proxyRefresh(context.Background(), []string{"slack", "broken"}, start, catalogPath, &out)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := mcpproxy.LoadCatalog(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Servers["slack"].Tools) != 2 {
		t.Fatalf("catalog = %+v, want slack with 2 tools", cat.Servers)
	}
	report := out.String()
	if !strings.Contains(report, "slack") || !strings.Contains(report, "2 tools") ||
		!strings.Contains(report, "broken") || !strings.Contains(report, "not found") {
		t.Fatalf("report missing a server or its outcome:\n%s", report)
	}
}

func TestProxyRefresh_NoServers(t *testing.T) {
	var out bytes.Buffer
	err := proxyRefresh(context.Background(), nil, nil, filepath.Join(t.TempDir(), "c.json"), &out)
	if err == nil || !strings.Contains(err.Error(), "no stdio MCP servers") {
		t.Fatalf("got %v, want an error saying there is nothing to proxy", err)
	}
}

func TestProxyClaudeConfig(t *testing.T) {
	got := proxyClaudeConfig([]string{"slack", "github"}, 7381)
	var parsed struct {
		MCPServers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	start := strings.Index(got, "{")
	if start < 0 || json.Unmarshal([]byte(got[start:strings.LastIndex(got, "}")+1]), &parsed) != nil {
		t.Fatalf("output has no valid JSON entry:\n%s", got)
	}
	mux := parsed.MCPServers["mux"]
	if mux.Type != "http" || mux.URL != "http://127.0.0.1:7381/mcp" {
		t.Fatalf("mux entry = %+v", mux)
	}
	if !strings.Contains(got, "github, slack") {
		t.Fatalf("output should list the entries it replaces, sorted:\n%s", got)
	}
}

func TestProxyRefresh_AllFailedIsAnError(t *testing.T) {
	start := func(ctx context.Context, server string) (mcpproxy.Backend, error) {
		return nil, errors.New("exec: not found")
	}
	var out bytes.Buffer
	err := proxyRefresh(context.Background(), []string{"a", "b"}, start, filepath.Join(t.TempDir(), "c.json"), &out)
	if err == nil || !strings.Contains(err.Error(), "no server") {
		t.Fatalf("got %v, want an error when every server failed", err)
	}
	if !strings.Contains(out.String(), "not found") {
		t.Fatalf("per-server failures not reported:\n%s", out.String())
	}
}

func TestProxyCatalogProblem(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.json")
	if msg := proxyCatalogProblem(missing, 0); !strings.Contains(msg, "no catalog at") || !strings.Contains(msg, "refresh") {
		t.Fatalf("missing catalog message = %q", msg)
	}
	present := filepath.Join(dir, "c.json")
	if err := (&mcpproxy.Catalog{}).Save(present); err != nil {
		t.Fatal(err)
	}
	if msg := proxyCatalogProblem(present, 0); !strings.Contains(msg, "failures") {
		t.Fatalf("empty catalog message = %q, want a pointer to the refresh failures", msg)
	}
	if msg := proxyCatalogProblem(present, 3); msg != "" {
		t.Fatalf("usable catalog reported a problem: %q", msg)
	}
}
