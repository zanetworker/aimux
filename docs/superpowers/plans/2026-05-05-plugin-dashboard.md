# Plugin Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a plugin extension model to aimux so external tools can register dashboard panels. Ship a built-in skill-dashboard plugin that auto-detects skill tracking files and renders six panels (metrics, health table, bar charts, lists).

**Architecture:** Core `internal/plugin/` package handles loading manifests, executing commands, and caching output. Web frontend adds a generic `PluginView` component with four panel renderers (MetricRow, DataTable, BarChart, ExpandableList). Built-in plugins are registered in Go code with auto-detect file paths. The existing `skill-dashboard.py` gets a `--format json` flag.

**Tech Stack:** Go (plugin core, HTTP handlers), React/TypeScript (panel renderers), Python (skill-dashboard.py JSON output mode), YAML (plugin manifests).

---

### Task 1: Core plugin types and built-in registry

**Files:**
- Create: `internal/plugin/types.go`
- Create: `internal/plugin/builtin.go`

- [ ] **Step 1: Create types.go with all data structures**

```go
package plugin

import "encoding/json"

type PanelType string

const (
	PanelMetricRow PanelType = "metric-row"
	PanelTable     PanelType = "table"
	PanelBarChart  PanelType = "bar-chart"
	PanelList      PanelType = "list"
)

type Panel struct {
	ID         string    `json:"id" yaml:"id"`
	Type       PanelType `json:"type" yaml:"type"`
	Title      string    `json:"title" yaml:"title"`
	Sortable   bool      `json:"sortable,omitempty" yaml:"sortable,omitempty"`
	Expandable bool      `json:"expandable,omitempty" yaml:"expandable,omitempty"`
	Width      string    `json:"width,omitempty" yaml:"width,omitempty"` // "half" or "" (full)
}

type Plugin struct {
	Name         string   `json:"name" yaml:"name"`
	Tab          string   `json:"tab" yaml:"tab"`
	Command      string   `json:"command" yaml:"command"`
	CacheSecs    int      `json:"cache_seconds" yaml:"cache_seconds"`
	Panels       []Panel  `json:"panels" yaml:"panels"`
	AutoDetect   []string `json:"-" yaml:"-"` // built-in only: files to check
	BuiltIn      bool     `json:"-" yaml:"-"`
}

// MetricItem is one chip in a metric-row panel.
type MetricItem struct {
	Label string      `json:"label"`
	Value interface{} `json:"value"`
	Color string      `json:"color"`
}

// TableRow is one row in a table panel.
type TableRow struct {
	Cells []interface{} `json:"cells"`
	Color string        `json:"color,omitempty"`
}

// TableData is the data shape for a table panel.
type TableData struct {
	Columns []string   `json:"columns"`
	Rows    []TableRow `json:"rows"`
}

// BarChartItem is one bar in a bar-chart panel.
type BarChartItem struct {
	Label     string   `json:"label"`
	Value     float64  `json:"value"`
	Secondary float64  `json:"secondary,omitempty"`
	Legend    []string `json:"legend,omitempty"`
}

// ListItem is one entry in a list panel.
type ListItem struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Body     string   `json:"body,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// PanelData wraps the JSON output for a single panel.
// Only one of the typed fields is populated based on PanelType.
type PanelData struct {
	Items json.RawMessage `json:"items,omitempty"`
	// For table type:
	Columns []string        `json:"columns,omitempty"`
	Rows    json.RawMessage `json:"rows,omitempty"`
}
```

- [ ] **Step 2: Create builtin.go with the skill-dashboard definition**

```go
package plugin

var builtins = []Plugin{
	{
		Name:    "skill-dashboard",
		Tab:     "Skills",
		Command: "python3 ~/.claude/scripts/skill-dashboard.py --format json",
		CacheSecs: 30,
		AutoDetect: []string{
			"~/.claude/skill-usage.jsonl",
			"~/.claude/skill-effectiveness.jsonl",
		},
		BuiltIn: true,
		Panels: []Panel{
			{ID: "metrics", Type: PanelMetricRow, Title: "Overview"},
			{ID: "health", Type: PanelTable, Title: "Skill Health", Sortable: true},
			{ID: "top-skills", Type: PanelBarChart, Title: "Top Skills", Width: "half"},
			{ID: "triggers", Type: PanelBarChart, Title: "Trigger Breakdown", Width: "half"},
			{ID: "pending", Type: PanelList, Title: "Pending Learnings", Expandable: true},
			{ID: "never-triggered", Type: PanelList, Title: "Never Triggered"},
		},
	},
}

// Builtins returns built-in plugin definitions that pass auto-detection.
func Builtins() []Plugin {
	var result []Plugin
	for _, p := range builtins {
		if autoDetect(p.AutoDetect) {
			result = append(result, p)
		}
	}
	return result
}

func autoDetect(paths []string) bool {
	for _, p := range paths {
		expanded := expandHome(p)
		if fileExists(expanded) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./internal/plugin/...`
Expected: passes (needs `expandHome` and `fileExists` helpers, add them in types.go)

Add to types.go:
```go
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
```

- [ ] **Step 4: Commit**

```bash
git add internal/plugin/types.go internal/plugin/builtin.go
git commit -m "feat: plugin types and built-in skill-dashboard definition"
```

---

### Task 2: Plugin loader (custom plugins from ~/.aimux/plugins/)

**Files:**
- Create: `internal/plugin/loader.go`
- Create: `internal/plugin/loader_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanPlugins_ValidManifest(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "my-plugin")
	os.MkdirAll(pluginDir, 0o755)

	manifest := `
name: my-plugin
tab: MyTab
command: echo '{"test": {"items": []}}'
cache_seconds: 10
panels:
  - id: test
    type: metric-row
    title: Test Panel
`
	os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte(manifest), 0o644)

	plugins, err := ScanPlugins(dir)
	if err != nil {
		t.Fatalf("ScanPlugins error: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].Name != "my-plugin" {
		t.Errorf("expected name my-plugin, got %s", plugins[0].Name)
	}
	if plugins[0].Tab != "MyTab" {
		t.Errorf("expected tab MyTab, got %s", plugins[0].Tab)
	}
	if len(plugins[0].Panels) != 1 {
		t.Fatalf("expected 1 panel, got %d", len(plugins[0].Panels))
	}
	if plugins[0].Panels[0].Type != PanelMetricRow {
		t.Errorf("expected metric-row, got %s", plugins[0].Panels[0].Type)
	}
}

func TestScanPlugins_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	plugins, err := ScanPlugins(dir)
	if err != nil {
		t.Fatalf("ScanPlugins error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(plugins))
	}
}

func TestScanPlugins_MissingDir(t *testing.T) {
	plugins, err := ScanPlugins("/nonexistent/path")
	if err != nil {
		t.Fatalf("ScanPlugins should not error on missing dir: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(plugins))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugin/... -run TestScanPlugins -v -timeout 30s`
Expected: FAIL (ScanPlugins not defined)

- [ ] **Step 3: Implement loader.go**

```go
package plugin

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ScanPlugins reads plugin.yaml manifests from subdirectories of dir.
// Returns nil (not error) if dir does not exist.
func ScanPlugins(dir string) ([]Plugin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plugins dir %s: %w", dir, err)
	}

	var plugins []Plugin
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(dir, e.Name(), "plugin.yaml")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var p Plugin
		if err := yaml.Unmarshal(data, &p); err != nil {
			continue
		}
		if p.Name == "" || p.Command == "" || len(p.Panels) == 0 {
			continue
		}
		if p.CacheSecs == 0 {
			p.CacheSecs = 30
		}
		plugins = append(plugins, p)
	}
	return plugins, nil
}

// DefaultPluginsDir returns ~/.aimux/plugins/.
func DefaultPluginsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aimux", "plugins")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/plugin/... -run TestScanPlugins -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/loader.go internal/plugin/loader_test.go
git commit -m "feat: plugin loader scans ~/.aimux/plugins/ for manifests"
```

---

### Task 3: Plugin executor with caching

**Files:**
- Create: `internal/plugin/executor.go`
- Create: `internal/plugin/executor_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugin

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExecutor_RunsCommand(t *testing.T) {
	p := Plugin{
		Name:      "test",
		Command:   `echo '{"metrics":{"items":[{"label":"count","value":42}]}}'`,
		CacheSecs: 1,
	}
	exec := NewExecutor([]Plugin{p})

	data, err := exec.Execute("test")
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if data == nil {
		t.Fatal("expected non-nil data")
	}

	var metrics struct {
		Items []MetricItem `json:"items"`
	}
	raw, ok := data["metrics"]
	if !ok {
		t.Fatal("expected metrics key in output")
	}
	if err := json.Unmarshal(raw, &metrics); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if len(metrics.Items) != 1 || metrics.Items[0].Label != "count" {
		t.Errorf("unexpected metrics: %+v", metrics)
	}
}

func TestExecutor_CachesResult(t *testing.T) {
	p := Plugin{
		Name:      "counter",
		Command:   "date +%s%N",
		CacheSecs: 5,
	}
	exec := NewExecutor([]Plugin{p})

	data1, _ := exec.Execute("counter")
	data2, _ := exec.Execute("counter")

	// Should be identical (cached)
	r1, _ := json.Marshal(data1)
	r2, _ := json.Marshal(data2)
	if string(r1) != string(r2) {
		t.Errorf("expected cached result, got different outputs")
	}
}

func TestExecutor_CacheExpires(t *testing.T) {
	p := Plugin{
		Name:      "counter",
		Command:   "date +%s%N",
		CacheSecs: 1,
	}
	exec := NewExecutor([]Plugin{p})

	data1, _ := exec.Execute("counter")
	time.Sleep(1100 * time.Millisecond)
	data2, _ := exec.Execute("counter")

	r1, _ := json.Marshal(data1)
	r2, _ := json.Marshal(data2)
	if string(r1) == string(r2) {
		t.Errorf("expected cache to expire, got same result")
	}
}

func TestExecutor_UnknownPlugin(t *testing.T) {
	exec := NewExecutor(nil)
	_, err := exec.Execute("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown plugin")
	}
}

func TestExecutor_CommandFailure(t *testing.T) {
	p := Plugin{
		Name:    "bad",
		Command: "false",
	}
	exec := NewExecutor([]Plugin{p})
	_, err := exec.Execute("bad")
	if err == nil {
		t.Fatal("expected error for failing command")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugin/... -run TestExecutor -v -timeout 30s`
Expected: FAIL

- [ ] **Step 3: Implement executor.go**

```go
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	data   map[string]json.RawMessage
	expiry time.Time
}

// Executor runs plugin commands and caches their output.
type Executor struct {
	plugins map[string]Plugin
	mu      sync.Mutex
	cache   map[string]cacheEntry
}

// NewExecutor creates an executor for the given plugins.
func NewExecutor(plugins []Plugin) *Executor {
	m := make(map[string]Plugin, len(plugins))
	for _, p := range plugins {
		m[p.Name] = p
	}
	return &Executor{
		plugins: m,
		cache:   make(map[string]cacheEntry),
	}
}

// Plugins returns the list of registered plugins.
func (e *Executor) Plugins() []Plugin {
	result := make([]Plugin, 0, len(e.plugins))
	for _, p := range e.plugins {
		result = append(result, p)
	}
	return result
}

// Execute runs the plugin command and returns parsed JSON output.
// Results are cached for the plugin's cache_seconds duration.
func (e *Executor) Execute(name string) (map[string]json.RawMessage, error) {
	p, ok := e.plugins[name]
	if !ok {
		return nil, fmt.Errorf("unknown plugin: %s", name)
	}

	e.mu.Lock()
	if cached, ok := e.cache[name]; ok && time.Now().Before(cached.expiry) {
		e.mu.Unlock()
		return cached.data, nil
	}
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmdStr := expandHome(p.Command)
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("plugin %s command failed: %w", name, err)
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("plugin %s invalid JSON: %w (output: %s)", name, err, truncateStr(string(out), 200))
	}

	ttl := p.CacheSecs
	if ttl <= 0 {
		ttl = 30
	}

	e.mu.Lock()
	e.cache[name] = cacheEntry{data: data, expiry: time.Now().Add(time.Duration(ttl) * time.Second)}
	e.mu.Unlock()

	return data, nil
}

func truncateStr(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/plugin/... -run TestExecutor -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/plugin/executor.go internal/plugin/executor_test.go
git commit -m "feat: plugin executor with caching and timeout"
```

---

### Task 4: Web backend - plugin API endpoints

**Files:**
- Modify: `internal/frontend/web/server.go` - add `*plugin.Executor` field, routes
- Modify: `internal/frontend/web/handlers.go` - add `handlePlugins`, `handlePluginData`
- Modify: `cmd/aimux/main.go` - wire executor

- [ ] **Step 1: Add executor field and setter to server.go**

Add to Server struct:
```go
pluginExec *plugin.Executor
```

Add setter:
```go
func (s *Server) SetPluginExecutor(exec *plugin.Executor) {
	s.pluginExec = exec
}
```

Add routes in `Start()`:
```go
mux.HandleFunc("GET /api/plugins", s.handlePlugins)
mux.HandleFunc("GET /api/plugins/{name}/data", s.handlePluginData)
```

Add import `"github.com/zanetworker/aimux/internal/plugin"`.

- [ ] **Step 2: Add handlers to handlers.go**

```go
func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	if s.pluginExec == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"plugins": []any{}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"plugins": s.pluginExec.Plugins()})
}

func (s *Server) handlePluginData(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.pluginExec == nil {
		http.Error(w, "plugins not configured", http.StatusServiceUnavailable)
		return
	}
	data, err := s.pluginExec.Execute(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
```

Add import `"github.com/zanetworker/aimux/internal/plugin"` (may already be covered by the `plugin` used in server.go).

- [ ] **Step 3: Wire executor in cmd/aimux/main.go**

After `s.SetController(controller.New(cfg))`, add:

```go
// Discover plugins: built-in auto-detect + custom from ~/.aimux/plugins/
allPlugins := plugin.Builtins()
if custom, err := plugin.ScanPlugins(plugin.DefaultPluginsDir()); err == nil {
	allPlugins = append(allPlugins, custom...)
}
if len(allPlugins) > 0 {
	s.SetPluginExecutor(plugin.NewExecutor(allPlugins))
}
```

Add import `"github.com/zanetworker/aimux/internal/plugin"`.

- [ ] **Step 4: Build and verify**

Run: `go build ./... && go vet ./...`
Expected: passes

- [ ] **Step 5: Commit**

```bash
git add internal/frontend/web/server.go internal/frontend/web/handlers.go cmd/aimux/main.go
git commit -m "feat: web API endpoints for plugin list and data"
```

---

### Task 5: Frontend - PluginView with four panel renderers

**Files:**
- Create: `web/src/components/PluginView.tsx`
- Modify: `web/src/App.tsx` - fetch plugins, add dynamic tabs

- [ ] **Step 1: Create PluginView.tsx**

This component fetches plugin data and renders panels in order. It contains all four inline panel renderers to keep things simple (no separate files for CSS-only components).

```typescript
import { useState, useEffect } from 'react';

interface PluginManifest {
  name: string;
  tab: string;
  panels: {
    id: string;
    type: 'metric-row' | 'table' | 'bar-chart' | 'list';
    title: string;
    sortable?: boolean;
    expandable?: boolean;
    width?: string;
  }[];
}

interface MetricItem { label: string; value: any; color: string; }
interface TableRow { cells: any[]; color?: string; }
interface BarItem { label: string; value: number; secondary?: number; legend?: string[]; }
interface ListItem { title: string; subtitle?: string; body?: string; tags?: string[]; }

const colorMap: Record<string, string> = {
  green: 'var(--green)', accent: 'var(--accent)', orange: 'var(--orange)',
  purple: 'var(--purple)', teal: 'var(--teal)', 'fg-3': 'var(--fg-3)',
};
const toColor = (c: string) => colorMap[c] || `var(--${c})`;

interface Props {
  plugin: PluginManifest;
}

export function PluginView({ plugin }: Props) {
  const [data, setData] = useState<Record<string, any> | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    fetch(`/api/plugins/${plugin.name}/data`)
      .then(r => {
        if (!r.ok) return r.text().then(t => { throw new Error(t); });
        return r.json();
      })
      .then(d => { setData(d); setError(null); })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false));
  }, [plugin.name]);

  if (loading) {
    return <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--fg-3)', fontSize: 13 }}>Loading {plugin.tab}...</div>;
  }
  if (error) {
    return <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--accent)', fontSize: 12, fontFamily: 'var(--mono)', padding: 20, textAlign: 'center' }}>{error}</div>;
  }
  if (!data) return null;

  // Group panels: consecutive half-width panels go side-by-side
  const rows: { panels: typeof plugin.panels }[] = [];
  let i = 0;
  while (i < plugin.panels.length) {
    const p = plugin.panels[i];
    if (p.width === 'half' && i + 1 < plugin.panels.length && plugin.panels[i + 1].width === 'half') {
      rows.push({ panels: [p, plugin.panels[i + 1]] });
      i += 2;
    } else {
      rows.push({ panels: [p] });
      i++;
    }
  }

  return (
    <div style={{ flex: 1, overflowY: 'auto', padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 16 }}>
      {rows.map((row, ri) => (
        <div key={ri} style={{ display: 'flex', gap: 16 }}>
          {row.panels.map(panel => {
            const panelData = data[panel.id];
            return (
              <div key={panel.id} style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 10, fontWeight: 600, color: 'var(--fg-3)', textTransform: 'uppercase', letterSpacing: '0.06em', marginBottom: 8 }}>
                  {panel.title}
                </div>
                {!panelData ? (
                  <div style={{ color: 'var(--fg-4)', fontSize: 11, fontStyle: 'italic' }}>No data</div>
                ) : panel.type === 'metric-row' ? (
                  <MetricRow items={panelData.items || []} />
                ) : panel.type === 'table' ? (
                  <DataTable columns={panelData.columns || []} rows={panelData.rows || []} sortable={panel.sortable} />
                ) : panel.type === 'bar-chart' ? (
                  <BarChart items={panelData.items || []} />
                ) : panel.type === 'list' ? (
                  <ExpandableList items={panelData.items || []} expandable={panel.expandable} />
                ) : null}
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
}

function MetricRow({ items }: { items: MetricItem[] }) {
  return (
    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
      {items.map((item, i) => (
        <div key={i} style={{ display: 'flex', alignItems: 'baseline', gap: 4, padding: '6px 12px', borderRadius: 4, background: 'var(--bg-1)', border: '1px solid var(--border)' }}>
          <span style={{ fontSize: 18, fontWeight: 700, fontFamily: 'var(--mono)', color: toColor(item.color) }}>{item.value}</span>
          <span style={{ fontSize: 9, color: 'var(--fg-4)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>{item.label}</span>
        </div>
      ))}
    </div>
  );
}

function DataTable({ columns, rows, sortable }: { columns: string[]; rows: TableRow[]; sortable?: boolean }) {
  const [sortCol, setSortCol] = useState<number | null>(null);
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('asc');

  const handleSort = (col: number) => {
    if (!sortable) return;
    if (sortCol === col) { setSortDir(d => d === 'asc' ? 'desc' : 'asc'); }
    else { setSortCol(col); setSortDir('asc'); }
  };

  let sorted = rows;
  if (sortable && sortCol !== null) {
    sorted = [...rows].sort((a, b) => {
      const av = a.cells[sortCol], bv = b.cells[sortCol];
      const cmp = typeof av === 'number' && typeof bv === 'number' ? av - bv : String(av).localeCompare(String(bv));
      return sortDir === 'asc' ? cmp : -cmp;
    });
  }

  const rowColor = (c?: string) => c ? toColor(c) : 'var(--fg-2)';

  return (
    <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 11 }}>
      <thead>
        <tr>
          {columns.map((col, ci) => (
            <th key={ci} onClick={() => handleSort(ci)} style={{
              padding: '6px 10px', textAlign: 'left', fontSize: 9, fontWeight: 700,
              textTransform: 'uppercase', letterSpacing: '0.06em', borderBottom: '1px solid var(--border)',
              color: sortCol === ci ? 'var(--fg)' : 'var(--fg-3)', cursor: sortable ? 'pointer' : 'default',
              userSelect: 'none',
            }}>
              {col} {sortCol === ci ? (sortDir === 'asc' ? '\u25b2' : '\u25bc') : ''}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {sorted.map((row, ri) => (
          <tr key={ri} onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg-1)')} onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
            {row.cells.map((cell, ci) => (
              <td key={ci} style={{ padding: '6px 10px', color: ci === 0 ? 'var(--fg)' : rowColor(row.color), fontFamily: ci > 0 ? 'var(--mono)' : 'inherit', borderBottom: '1px solid var(--bg-2)' }}>
                {cell}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function BarChart({ items }: { items: BarItem[] }) {
  const max = Math.max(...items.map(i => i.value + (i.secondary || 0)), 1);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      {items.map((item, i) => (
        <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 10 }}>
          <span style={{ width: 120, textAlign: 'right', color: 'var(--fg-2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontFamily: 'var(--mono)', fontSize: 9 }}>
            {item.label}
          </span>
          <div style={{ flex: 1, display: 'flex', height: 14, borderRadius: 2, overflow: 'hidden', background: 'var(--bg-2)' }}>
            <div style={{ width: `${(item.value / max) * 100}%`, background: 'var(--teal)', borderRadius: '2px 0 0 2px' }} />
            {item.secondary !== undefined && item.secondary > 0 && (
              <div style={{ width: `${(item.secondary / max) * 100}%`, background: 'var(--purple)' }} />
            )}
          </div>
          <span style={{ fontFamily: 'var(--mono)', fontSize: 9, color: 'var(--fg-3)', minWidth: 30 }}>
            {item.value}{item.secondary ? `+${item.secondary}` : ''}
          </span>
        </div>
      ))}
      {items[0]?.legend && (
        <div style={{ display: 'flex', gap: 12, marginTop: 4, paddingLeft: 128 }}>
          {items[0].legend.map((l, i) => (
            <div key={l} style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 9, color: 'var(--fg-4)' }}>
              <div style={{ width: 8, height: 8, borderRadius: 2, background: i === 0 ? 'var(--teal)' : 'var(--purple)' }} />
              {l}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function ExpandableList({ items, expandable }: { items: ListItem[]; expandable?: boolean }) {
  const [expanded, setExpanded] = useState<Set<number>>(new Set());

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      {items.map((item, i) => {
        const isExpanded = expanded.has(i);
        return (
          <div key={i} style={{ background: 'var(--bg-1)', borderRadius: 4, border: '1px solid var(--border)' }}>
            <div
              onClick={() => expandable && item.body ? setExpanded(prev => { const n = new Set(prev); n.has(i) ? n.delete(i) : n.add(i); return n; }) : undefined}
              style={{ padding: '8px 10px', display: 'flex', alignItems: 'center', gap: 8, cursor: expandable && item.body ? 'pointer' : 'default' }}
            >
              {expandable && item.body && (
                <span style={{ fontSize: 8, color: 'var(--fg-4)', transform: isExpanded ? 'rotate(90deg)' : 'none', transition: 'transform 0.15s' }}>
                  ▶
                </span>
              )}
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 11, color: 'var(--fg)', fontWeight: 500 }}>{item.title}</div>
                {item.subtitle && <div style={{ fontSize: 9, color: 'var(--fg-3)' }}>{item.subtitle}</div>}
              </div>
              {item.tags?.map(t => (
                <span key={t} style={{ fontSize: 8, padding: '1px 4px', borderRadius: 2, background: 'var(--accent-dim)', color: 'var(--accent)' }}>{t}</span>
              ))}
            </div>
            {isExpanded && item.body && (
              <div style={{ padding: '0 10px 10px 28px', fontSize: 10, color: 'var(--fg-2)', lineHeight: '1.5' }}>
                {item.body}
              </div>
            )}
          </div>
        );
      })}
      {items.length === 0 && (
        <div style={{ color: 'var(--fg-4)', fontSize: 11, fontStyle: 'italic', padding: '8px 10px' }}>None</div>
      )}
    </div>
  );
}
```

- [ ] **Step 2: Update App.tsx to discover and render plugin tabs**

Add state:
```typescript
const [pluginTabs, setPluginTabs] = useState<{ name: string; tab: string; panels: any[] }[]>([]);
```

Fetch on mount:
```typescript
useEffect(() => {
  fetch('/api/plugins')
    .then(r => r.ok ? r.json() : null)
    .then(d => { if (d?.plugins) setPluginTabs(d.plugins); })
    .catch(() => {});
}, []);
```

Update the `ViewTab` type:
```typescript
type ViewTab = 'agents' | 'sessions' | string; // string for plugin tabs
```

Update the tab switcher to include plugin tabs:
```typescript
{(['agents', 'sessions'] as string[]).concat(pluginTabs.map(p => `plugin:${p.name}`)).map(tab => {
  const label = tab === 'agents' ? `Agents (${agents.length})`
    : tab === 'sessions' ? `Sessions${sessionCount !== null ? ` (${sessionCount})` : ''}`
    : pluginTabs.find(p => `plugin:${p.name}` === tab)?.tab || tab;
  return (
    <button key={tab} onClick={() => { setActiveTab(tab); ... }} ...>
      {label}
    </button>
  );
})}
```

Add rendering in the main content area:
```typescript
{!panelFullscreen && activeTab.startsWith('plugin:') && (
  <PluginView plugin={pluginTabs.find(p => `plugin:${p.name}` === activeTab)!} />
)}
```

Add import: `import { PluginView } from './components/PluginView';`

- [ ] **Step 3: Build and verify**

Run: `cd web && npx tsc --noEmit && npm run build`
Expected: passes

- [ ] **Step 4: Commit**

```bash
git add web/src/components/PluginView.tsx web/src/App.tsx
git commit -m "feat: PluginView with MetricRow, DataTable, BarChart, ExpandableList renderers"
```

---

### Task 6: Add --format json to skill-dashboard.py

**Files:**
- Modify: `~/.claude/scripts/skill-dashboard.py`

- [ ] **Step 1: Add --format argument parsing**

Near the existing arg parsing (around line 61-66), add:
```python
output_format = "terminal"  # default
for i, arg in enumerate(sys.argv[1:], 1):
    if arg == "--format" and i < len(sys.argv) - 1:
        output_format = sys.argv[i + 1]
```

- [ ] **Step 2: Add json output function**

Add a new function `render_json()` that collects all the data and outputs a single JSON object with keys matching the panel IDs:

```python
def render_json():
    """Output structured JSON for aimux plugin consumption."""
    invocations = load_invocations()
    effectiveness = load_effectiveness_archive()
    pending = load_pending()

    # Metrics
    total_invocations = sum(v for v in invocations.values())
    total_corrections = sum(e.get("corrections_after", 0) for entries in effectiveness.values() for e in entries)
    total_skills = len(set(invocations.keys()) | set(effectiveness.keys()))
    correction_rate = (total_corrections / max(total_invocations, 1)) * 100
    pending_count = sum(len(p.get("proposals", [])) for p in pending)

    # Health table
    all_skills = sorted(set(invocations.keys()) | set(effectiveness.keys()))
    health_rows = []
    never_triggered = []
    for skill in all_skills:
        inv = invocations.get(skill, 0)
        corr = sum(e.get("corrections_after", 0) for e in effectiveness.get(skill, []))
        rate = (corr / max(inv, 1)) * 100
        color = "green" if rate < 15 else "orange" if rate < 30 else "accent"
        if inv == 0:
            never_triggered.append({"title": skill, "subtitle": "Registered but never invoked"})
        else:
            health_rows.append({"cells": [skill, inv, corr, f"{rate:.0f}%"], "color": color})
    health_rows.sort(key=lambda r: r["cells"][1], reverse=True)

    # Top skills bar chart
    top_skills = sorted(invocations.items(), key=lambda x: x[1], reverse=True)[:15]

    # Trigger breakdown
    cl_data = load_cl_debug()
    trigger_items = []
    for skill, count in sorted(invocations.items(), key=lambda x: x[1], reverse=True)[:15]:
        auto = cl_data.get("auto_triggered", {}).get(skill, 0)
        user = cl_data.get("user_triggered", {}).get(skill, 0)
        if auto + user > 0:
            trigger_items.append({"label": skill, "value": auto, "secondary": user, "legend": ["auto", "user"]})

    # Pending learnings
    pending_items = []
    for pfile in pending:
        for proposal in pfile.get("proposals", []):
            pending_items.append({
                "title": proposal.get("title", "(untitled)"),
                "subtitle": f"{proposal.get('category', '')} | {proposal.get('confidence', '')}",
                "body": proposal.get("summary", ""),
                "tags": [proposal["attributed_skill"]] if proposal.get("attributed_skill") and proposal["attributed_skill"] != "none" else [],
            })

    result = {
        "metrics": {
            "items": [
                {"label": "Invocations", "value": total_invocations, "color": "teal"},
                {"label": "Correction Rate", "value": f"{correction_rate:.0f}%", "color": "green" if correction_rate < 15 else "orange" if correction_rate < 30 else "accent"},
                {"label": "Pending", "value": pending_count, "color": "orange" if pending_count > 0 else "fg-3"},
                {"label": "Never Triggered", "value": len(never_triggered), "color": "accent" if len(never_triggered) > 0 else "fg-3"},
            ]
        },
        "health": {
            "columns": ["Skill", "Invocations", "Corrections", "Rate"],
            "rows": health_rows,
        },
        "top-skills": {
            "items": [{"label": s, "value": v} for s, v in top_skills],
        },
        "triggers": {
            "items": trigger_items if trigger_items else [{"label": s, "value": v} for s, v in top_skills[:10]],
        },
        "pending": {
            "items": pending_items,
        },
        "never-triggered": {
            "items": never_triggered,
        },
    }
    print(json.dumps(result))
```

- [ ] **Step 3: Wire the format flag in main()**

At the top of `main()`:
```python
if output_format == "json":
    render_json()
    return
```

- [ ] **Step 4: Test the JSON output**

Run: `python3 ~/.claude/scripts/skill-dashboard.py --format json | python3 -m json.tool | head -30`
Expected: valid JSON with metrics, health, top-skills, triggers, pending, never-triggered keys

- [ ] **Step 5: Commit**

```bash
git add ~/.claude/scripts/skill-dashboard.py  # only if in repo, otherwise note as external
git commit -m "feat: --format json flag for skill-dashboard.py"
```

Note: `skill-dashboard.py` is outside the aimux repo (in `~/.claude/scripts/`). This step modifies it in place but does not commit it to the aimux repo.

---

### Task 7: Integration test and final build

- [ ] **Step 1: Run full Go test suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: all pass (except pre-existing TestSSEStreamsAgentState flake)

- [ ] **Step 2: Run frontend build**

Run: `cd web && npx tsc --noEmit && npm run build`
Expected: zero errors

- [ ] **Step 3: Build binary and run**

Run: `go build -o aimux ./cmd/aimux`

- [ ] **Step 4: Manual verification**

Start: `./aimux web --port 3001`
Verify:
1. Skills tab appears (auto-detected from skill-usage.jsonl)
2. Overview metrics show invocation count, correction rate, pending, never-triggered
3. Health table is sortable, rows colored by correction rate
4. Top Skills and Trigger Breakdown bar charts render side-by-side
5. Pending Learnings items expand to show body text
6. Never Triggered list shows skills with zero observations

- [ ] **Step 5: Final commit**

```bash
go build -o aimux ./cmd/aimux
git add -A
git commit -m "feat: plugin dashboard with built-in skill effectiveness view"
```
