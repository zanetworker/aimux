# aimux Web Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a browser-based Kanban dashboard to aimux for multi-repo, multi-session AI agent tracking with trace viewing and interactive terminal access.

**Architecture:** Go HTTP server in `internal/frontend/web/` serves a React SPA (embedded via `go:embed`) alongside the existing Bubble Tea TUI in `internal/frontend/tui/`. Both frontends consume shared packages (`controller`, `discovery`, `trace`, etc.) independently. Real-time updates via SSE, terminal attachment via WebSocket.

**Tech Stack:** Go stdlib `net/http` + `gorilla/websocket`, React 19 + Vite + TypeScript + dnd-kit + xterm.js + shadcn/ui

**Spec:** `docs/superpowers/specs/2026-05-03-web-dashboard-design.md`

---

## File Structure

### Go backend (new/modified)

| File | Responsibility |
|------|---------------|
| `internal/frontend/tui/` | Existing TUI, moved from `internal/tui/` |
| `internal/frontend/web/server.go` | HTTP router, static file serving, lifecycle |
| `internal/frontend/web/server_test.go` | Server startup, routing, embed tests |
| `internal/frontend/web/sse.go` | SSE endpoint: agent state + trace streaming |
| `internal/frontend/web/sse_test.go` | SSE event format, client tracking, subscriptions |
| `internal/frontend/web/handlers.go` | REST endpoints: launch, annotate, archive, diff, history |
| `internal/frontend/web/handlers_test.go` | REST handler tests |
| `internal/frontend/web/terminal.go` | WebSocket proxy: xterm.js to tmux PTY |
| `internal/frontend/web/terminal_test.go` | WebSocket handshake, tmux session validation |
| `internal/frontend/web/embed.go` | `//go:embed` directive for `web/dist` |
| `cmd/aimux/main.go` | Add `--web` flag and `web` subcommand |

### React frontend (all new)

| File | Responsibility |
|------|---------------|
| `web/package.json` | Dependencies: react, vite, dnd-kit, xterm, shadcn |
| `web/vite.config.ts` | Vite config with API proxy to Go backend |
| `web/tsconfig.json` | TypeScript config |
| `web/index.html` | SPA entry point |
| `web/src/main.tsx` | React mount |
| `web/src/App.tsx` | Layout: StatsBar + Board + RightPanel |
| `web/src/types.ts` | Agent, Turn, ToolSpan TypeScript types |
| `web/src/hooks/useAgentStream.ts` | SSE hook for agent state |
| `web/src/hooks/useTraceStream.ts` | SSE hook for trace updates |
| `web/src/components/StatsBar.tsx` | Top stats bar |
| `web/src/components/KanbanBoard.tsx` | Columns + drag-and-drop |
| `web/src/components/AgentCard.tsx` | Session card |
| `web/src/components/RightPanel.tsx` | Tabbed panel: Trace + Session |
| `web/src/components/TraceView.tsx` | Compact trace with tool pills, diffs, annotations |
| `web/src/components/SessionView.tsx` | xterm.js terminal |
| `web/src/components/LaunchDialog.tsx` | Agent launch modal |
| `web/src/styles/theme.css` | DevHub dark-black CSS variables |

---

## Task 1: Move TUI to `internal/frontend/tui/`

**Files:**
- Move: `internal/tui/` → `internal/frontend/tui/`
- Modify: `cmd/aimux/main.go` (import path)
- Modify: `internal/tui/app.go` → `internal/frontend/tui/app.go` (internal import)
- Modify: `internal/tui/app_test.go` → `internal/frontend/tui/app_test.go` (internal import)

- [ ] **Step 1: Create the frontend directory and move files**

```bash
mkdir -p internal/frontend
git mv internal/tui internal/frontend/tui
```

- [ ] **Step 2: Update import in `cmd/aimux/main.go`**

Change line 15 from:
```go
"github.com/zanetworker/aimux/internal/tui"
```
to:
```go
"github.com/zanetworker/aimux/internal/frontend/tui"
```

- [ ] **Step 3: Update internal imports in `internal/frontend/tui/app.go`**

Change line 35 from:
```go
"github.com/zanetworker/aimux/internal/tui/views"
```
to:
```go
"github.com/zanetworker/aimux/internal/frontend/tui/views"
```

- [ ] **Step 4: Update internal imports in `internal/frontend/tui/app_test.go`**

Change line 18 from:
```go
"github.com/zanetworker/aimux/internal/tui/views"
```
to:
```go
"github.com/zanetworker/aimux/internal/frontend/tui/views"
```

- [ ] **Step 5: Verify build and tests pass**

```bash
go build ./cmd/aimux
go test ./internal/frontend/tui/... -count=1
```

Expected: BUILD OK, all existing TUI tests pass.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor: move internal/tui to internal/frontend/tui"
```

---

## Task 2: Go Web Server Skeleton

**Files:**
- Create: `internal/frontend/web/server.go`
- Create: `internal/frontend/web/server_test.go`
- Create: `internal/frontend/web/embed.go`
- Create: `web/dist/index.html` (placeholder for embed)
- Modify: `cmd/aimux/main.go`

- [ ] **Step 1: Create placeholder `web/dist/index.html` for embed**

```bash
mkdir -p web/dist
```

```html
<!-- web/dist/index.html -->
<!DOCTYPE html>
<html><body><h1>aimux dashboard</h1><p>Build the React app to replace this.</p></body></html>
```

- [ ] **Step 2: Write the failing test for server startup**

Create `internal/frontend/web/server_test.go`:

```go
package web

import (
	"net/http"
	"testing"
	"time"
)

func TestServerStartsAndServesRoot(t *testing.T) {
	s := NewServer(0) // port 0 = auto-assign
	go s.Start()
	defer s.Stop()

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestServerHealthEndpoint(t *testing.T) {
	s := NewServer(0)
	go s.Start()
	defer s.Stop()

	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./internal/frontend/web/... -v -count=1
```

Expected: FAIL — `NewServer` not defined.

- [ ] **Step 4: Create `internal/frontend/web/embed.go`**

```go
package web

import "embed"

//go:embed all:../../../web/dist
var staticFiles embed.FS
```

- [ ] **Step 5: Create `internal/frontend/web/server.go`**

```go
package web

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"
)

type Server struct {
	port     int
	listener net.Listener
	srv      *http.Server
}

func NewServer(port int) *Server {
	return &Server{port: port}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	sub, err := fs.Sub(staticFiles, "web/dist")
	if err != nil {
		return fmt.Errorf("embed sub: %w", err)
	}
	mux.Handle("/", http.FileServerFS(sub))

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = ln
	s.srv = &http.Server{Handler: mux}

	return s.srv.Serve(ln)
}

func (s *Server) Stop() {
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.srv.Shutdown(ctx)
	}
}

func (s *Server) URL() string {
	if s.listener == nil {
		return ""
	}
	return fmt.Sprintf("http://%s", s.listener.Addr().String())
}
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
go test ./internal/frontend/web/... -v -count=1
```

Expected: PASS — both tests green.

- [ ] **Step 7: Add `--web` flag and `web` subcommand to `cmd/aimux/main.go`**

Add import:
```go
"github.com/zanetworker/aimux/internal/frontend/web"
```

Replace the `main()` function's switch block to add `web` case and `--web` flag:

```go
func main() {
	if len(os.Args) < 2 {
		runTUI()
		return
	}

	switch os.Args[1] {
	case "--version", "-v":
		fmt.Printf("aimux %s\n", version)
	case "--web":
		runBoth()
	case "web":
		runWeb()
	case "sessions":
		runSessions(os.Args[2:])
	case "resume":
		runResume(os.Args[2:])
	case "--help", "-h", "help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}
```

Add `runWeb` and `runBoth`:

```go
func runWeb() {
	port := 3000
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &port)
		}
	}
	s := web.NewServer(port)
	fmt.Printf("aimux web dashboard: http://127.0.0.1:%d\n", port)
	if err := s.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Web server error: %v\n", err)
		os.Exit(1)
	}
}

func runBoth() {
	port := 3000
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &port)
		}
	}
	s := web.NewServer(port)
	go func() {
		fmt.Printf("aimux web dashboard: http://127.0.0.1:%d\n", port)
		if err := s.Start(); err != nil {
			debuglog.Log("web server error: %v", err)
		}
	}()
	runTUI()
}
```

- [ ] **Step 8: Update help text**

Add to `printHelp()`:
```go
fmt.Println(`  aimux --web              Launch TUI + web dashboard
  aimux web                Launch web dashboard only (headless)
  aimux web --port 8080    Custom port (default: 3000)`)
```

- [ ] **Step 9: Verify build**

```bash
go build ./cmd/aimux
```

Expected: BUILD OK.

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "feat: add web server skeleton with embed and CLI flags"
```

---

## Task 3: SSE Endpoint for Agent State

**Files:**
- Create: `internal/frontend/web/sse.go`
- Create: `internal/frontend/web/sse_test.go`
- Modify: `internal/frontend/web/server.go` (add discovery orchestrator, wire SSE route)

- [ ] **Step 1: Write the failing test for SSE agent streaming**

Create `internal/frontend/web/sse_test.go`:

```go
package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestSSEStreamsAgentState(t *testing.T) {
	// Create a server with a mock discoverer
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: 123, Name: "test-repo", ProviderName: "claude", Status: agent.StatusActive},
		}, nil
	})

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/events")
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	var gotEvent bool
	deadline := time.After(5 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for SSE event")
		default:
		}
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var payload struct {
				Agents []agent.Agent `json:"agents"`
			}
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				t.Fatalf("unmarshal SSE data: %v", err)
			}
			if len(payload.Agents) != 1 || payload.Agents[0].PID != 123 {
				t.Fatalf("unexpected agents: %+v", payload.Agents)
			}
			gotEvent = true
			break
		}
	}
	if !gotEvent {
		t.Fatal("never received agents event")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/frontend/web/... -v -count=1 -run TestSSEStreamsAgentState
```

Expected: FAIL — `SetDiscoverFunc` not defined.

- [ ] **Step 3: Add discover function to server and create `sse.go`**

Update `internal/frontend/web/server.go` — add field and setter:

```go
type Server struct {
	port        int
	listener    net.Listener
	srv         *http.Server
	discoverFn  func() ([]agent.Agent, error)
}

func (s *Server) SetDiscoverFunc(fn func() ([]agent.Agent, error)) {
	s.discoverFn = fn
}
```

Add import for `agent` package and wire SSE route in `Start()`:

```go
mux.HandleFunc("GET /api/events", s.handleSSE)
```

Create `internal/frontend/web/sse.go`:

```go
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Send initial state immediately
	s.sendAgentEvent(w, flusher)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			s.sendAgentEvent(w, flusher)
		}
	}
}

func (s *Server) sendAgentEvent(w http.ResponseWriter, flusher http.Flusher) {
	if s.discoverFn == nil {
		return
	}
	agents, err := s.discoverFn()
	if err != nil {
		return
	}
	data, err := json.Marshal(map[string]any{"agents": agents})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: agents\ndata: %s\n\n", data)
	flusher.Flush()
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/frontend/web/... -v -count=1
```

Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: add SSE endpoint for streaming agent state"
```

---

## Task 4: REST Handlers (Launch, Annotate, Archive, Diff, History)

**Files:**
- Create: `internal/frontend/web/handlers.go`
- Create: `internal/frontend/web/handlers_test.go`
- Modify: `internal/frontend/web/server.go` (wire routes, add spawn/eval deps)

- [ ] **Step 1: Write the failing test for the launch handler**

Create `internal/frontend/web/handlers_test.go`:

```go
package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestLaunchHandler(t *testing.T) {
	s := NewServer(0)
	var launched bool
	s.SetLaunchFunc(func(provider, dir, model, mode string) error {
		launched = true
		if provider != "claude" {
			t.Errorf("expected provider claude, got %s", provider)
		}
		return nil
	})

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	body, _ := json.Marshal(map[string]string{
		"provider": "claude",
		"dir":      "/tmp/test",
		"model":    "opus",
		"mode":     "auto",
	})
	resp, err := http.Post(s.URL()+"/api/agents/launch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/agents/launch failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !launched {
		t.Fatal("launch function was not called")
	}
}

func TestAnnotateHandler(t *testing.T) {
	s := NewServer(0)
	var annotated bool
	s.SetAnnotateFunc(func(sessionID string, turn int, label, note string) error {
		annotated = true
		if label != "good" {
			t.Errorf("expected label good, got %s", label)
		}
		return nil
	})

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	body, _ := json.Marshal(map[string]any{
		"turn":  1,
		"label": "good",
		"note":  "clean implementation",
	})
	resp, err := http.Post(s.URL()+"/api/agents/abc-123/annotate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !annotated {
		t.Fatal("annotate function was not called")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/frontend/web/... -v -count=1 -run TestLaunchHandler
```

Expected: FAIL — `SetLaunchFunc` not defined.

- [ ] **Step 3: Add function setters to server.go**

Add to `Server` struct:
```go
launchFn   func(provider, dir, model, mode string) error
annotateFn func(sessionID string, turn int, label, note string) error
```

Add setters:
```go
func (s *Server) SetLaunchFunc(fn func(provider, dir, model, mode string) error) {
	s.launchFn = fn
}

func (s *Server) SetAnnotateFunc(fn func(sessionID string, turn int, label, note string) error) {
	s.annotateFn = fn
}
```

Wire routes in `Start()`:
```go
mux.HandleFunc("POST /api/agents/launch", s.handleLaunch)
mux.HandleFunc("POST /api/agents/{id}/annotate", s.handleAnnotate)
mux.HandleFunc("POST /api/agents/{id}/archive", s.handleArchive)
mux.HandleFunc("GET /api/agents/{id}/diff", s.handleDiff)
mux.HandleFunc("GET /api/history", s.handleHistory)
mux.HandleFunc("POST /api/trace/subscribe/{sessionId}", s.handleTraceSubscribe)
mux.HandleFunc("POST /api/trace/unsubscribe/{sessionId}", s.handleTraceUnsubscribe)
```

- [ ] **Step 4: Create `internal/frontend/web/handlers.go`**

```go
package web

import (
	"encoding/json"
	"net/http"
)

type launchRequest struct {
	Provider string `json:"provider"`
	Dir      string `json:"dir"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
}

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var req launchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if s.launchFn == nil {
		http.Error(w, "launch not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.launchFn(req.Provider, req.Dir, req.Model, req.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "launched"})
}

type annotateRequest struct {
	Turn  int    `json:"turn"`
	Label string `json:"label"`
	Note  string `json:"note"`
}

func (s *Server) handleAnnotate(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req annotateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if s.annotateFn == nil {
		http.Error(w, "annotate not configured", http.StatusServiceUnavailable)
		return
	}
	if err := s.annotateFn(sessionID, req.Turn, req.Label, req.Note); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "annotated"})
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "archived"})
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "not implemented"})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}})
}

func (s *Server) handleTraceSubscribe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "subscribed"})
}

func (s *Server) handleTraceUnsubscribe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "unsubscribed"})
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/frontend/web/... -v -count=1
```

Expected: PASS — all tests green.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: add REST handlers for launch, annotate, archive, diff, history"
```

---

## Task 5: React Frontend Scaffold

**Files:**
- Create: `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`, `web/index.html`
- Create: `web/src/main.tsx`, `web/src/App.tsx`, `web/src/types.ts`, `web/src/styles/theme.css`

- [ ] **Step 1: Initialize React project**

```bash
cd web
pnpm create vite@latest . --template react-ts -- --force
pnpm install
```

- [ ] **Step 2: Install dependencies**

```bash
cd web
pnpm add @dnd-kit/core @dnd-kit/sortable @dnd-kit/utilities
pnpm add @xterm/xterm @xterm/addon-fit @xterm/addon-web-links
pnpm add -D tailwindcss @tailwindcss/vite
```

- [ ] **Step 3: Create `web/src/styles/theme.css`**

```css
:root {
  --bg-0: #000000;
  --bg-1: #0d0d0d;
  --bg-2: #141414;
  --bg-3: #1a1a1a;
  --bg-4: #242424;
  --bg-5: #333333;
  --accent: #FF3131;
  --accent-dim: rgba(255, 49, 49, 0.12);
  --teal: #49D3B4;
  --teal-dim: rgba(73, 211, 180, 0.12);
  --fg: #e6e6e6;
  --fg-2: #b3b3b3;
  --fg-3: #666666;
  --fg-4: #404040;
  --green: #69DF73;
  --green-dim: rgba(105, 223, 115, 0.12);
  --orange: #FFB251;
  --orange-dim: rgba(255, 178, 81, 0.12);
  --purple: #A772EF;
  --purple-dim: rgba(167, 114, 239, 0.12);
  --border: #1f1f1f;
  --border-hover: #2e2e2e;
  --font: 'Inter', system-ui, -apple-system, sans-serif;
  --mono: 'SF Mono', 'Fira Code', monospace;
}

* { margin: 0; padding: 0; box-sizing: border-box; }

body {
  font-family: var(--font);
  background: var(--bg-0);
  color: var(--fg);
  -webkit-font-smoothing: antialiased;
}
```

- [ ] **Step 4: Create `web/src/types.ts`**

```typescript
export interface Agent {
  pid: number;
  sessionId: string;
  name: string;
  providerName: string;
  sessionFile: string;
  model: string;
  workingDir: string;
  status: 'Active' | 'Idle' | 'Waiting' | 'Error' | 'Unknown';
  gitBranch: string;
  tokensIn: number;
  tokensOut: number;
  estCostUSD: number;
  lastActivity: string;
  lastAction: string;
  tmuxSession: string;
  teamName: string;
  taskSubject: string;
}

export interface ToolSpan {
  name: string;
  snippet: string;
  success: boolean;
  errorMsg: string;
}

export interface Turn {
  number: number;
  timestamp: string;
  userText: string;
  outputText: string;
  actions: ToolSpan[];
  tokensIn: number;
  tokensOut: number;
  costUSD: number;
  model: string;
}
```

- [ ] **Step 5: Create `web/src/App.tsx` with placeholder layout**

```tsx
import './styles/theme.css';

export default function App() {
  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <header style={{
        padding: '12px 24px',
        background: 'var(--bg-1)',
        borderBottom: '1px solid var(--border)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
      }}>
        <span style={{ fontSize: 18, fontWeight: 700 }}>
          <span style={{ color: 'var(--accent)' }}>ai</span>
          <span>mux</span>
        </span>
        <span style={{ color: 'var(--fg-3)', fontSize: 12 }}>Dashboard loading...</span>
      </header>
      <main style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <p style={{ color: 'var(--fg-3)' }}>Components will be added in subsequent tasks.</p>
      </main>
    </div>
  );
}
```

- [ ] **Step 6: Configure Vite proxy in `web/vite.config.ts`**

```typescript
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:3000',
        changeOrigin: true,
      },
    },
  },
});
```

- [ ] **Step 7: Build and verify embed works**

```bash
cd web && pnpm build
cd .. && go build ./cmd/aimux
```

Expected: BUILD OK. Running `./aimux web` serves the built React app at `http://127.0.0.1:3000`.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat: scaffold React frontend with Vite, theme, and types"
```

---

## Task 6: SSE Hook and StatsBar Component

**Files:**
- Create: `web/src/hooks/useAgentStream.ts`
- Create: `web/src/components/StatsBar.tsx`
- Modify: `web/src/App.tsx`

- [ ] **Step 1: Create `web/src/hooks/useAgentStream.ts`**

```typescript
import { useEffect, useState, useRef } from 'react';
import type { Agent } from '../types';

export function useAgentStream(): Agent[] {
  const [agents, setAgents] = useState<Agent[]>([]);
  const esRef = useRef<EventSource | null>(null);

  useEffect(() => {
    const es = new EventSource('/api/events');
    esRef.current = es;

    es.addEventListener('agents', (e) => {
      try {
        const data = JSON.parse(e.data);
        setAgents(data.agents || []);
      } catch {
        // ignore parse errors
      }
    });

    es.onerror = () => {
      es.close();
      // reconnect after 3s
      setTimeout(() => {
        esRef.current = new EventSource('/api/events');
      }, 3000);
    };

    return () => es.close();
  }, []);

  return agents;
}
```

- [ ] **Step 2: Create `web/src/components/StatsBar.tsx`**

```tsx
import type { Agent } from '../types';

interface Props {
  agents: Agent[];
  viewMode: 'status' | 'repo';
  onViewModeChange: (mode: 'status' | 'repo') => void;
  onLaunch: () => void;
}

export function StatsBar({ agents, viewMode, onViewModeChange, onLaunch }: Props) {
  const active = agents.filter(a => a.status === 'Active').length;
  const repos = new Set(agents.map(a => a.name)).size;
  const cost = agents.reduce((sum, a) => sum + a.estCostUSD, 0);
  const attention = agents.filter(a => a.status === 'Waiting').length;

  return (
    <header style={{
      display: 'flex', alignItems: 'center', justifyContent: 'space-between',
      padding: '12px 24px', background: 'var(--bg-1)',
      borderBottom: '1px solid var(--border)', flexShrink: 0,
    }}>
      <span style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.02em' }}>
        <span style={{ color: 'var(--accent)' }}>ai</span><span>mux</span>
      </span>

      <div style={{ display: 'flex', gap: 24 }}>
        <Stat value={active} label="Active" />
        <Stat value={repos} label="Repos" />
        <Stat value={`$${cost.toFixed(2)}`} label="Cost Today" />
        <Stat value={attention} label="Need Attention" highlight={attention > 0} />
      </div>

      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <ToggleBtn active={viewMode === 'status'} onClick={() => onViewModeChange('status')}>By Status</ToggleBtn>
        <ToggleBtn active={viewMode === 'repo'} onClick={() => onViewModeChange('repo')}>By Repo</ToggleBtn>
        <button onClick={onLaunch} style={{
          padding: '5px 12px', borderRadius: 4, border: '1px solid var(--accent)',
          background: 'transparent', color: 'var(--accent)', fontSize: 12,
          fontWeight: 600, cursor: 'pointer',
        }}>+ Launch</button>
      </div>
    </header>
  );
}

function Stat({ value, label, highlight }: { value: string | number; label: string; highlight?: boolean }) {
  return (
    <div style={{ textAlign: 'center' }}>
      <div style={{ fontSize: 20, fontWeight: 700, color: highlight ? 'var(--accent)' : 'var(--fg)' }}>{value}</div>
      <div style={{ fontSize: 10, color: 'var(--fg-3)', textTransform: 'uppercase', letterSpacing: '0.06em' }}>{label}</div>
    </div>
  );
}

function ToggleBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button onClick={onClick} style={{
      padding: '5px 10px', borderRadius: 4,
      border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
      background: active ? 'var(--accent)' : 'var(--bg-3)',
      color: active ? '#fff' : 'var(--fg-3)',
      fontSize: 11, cursor: 'pointer', fontWeight: 500,
    }}>{children}</button>
  );
}
```

- [ ] **Step 3: Wire into App.tsx**

```tsx
import { useState } from 'react';
import { useAgentStream } from './hooks/useAgentStream';
import { StatsBar } from './components/StatsBar';
import './styles/theme.css';

export default function App() {
  const agents = useAgentStream();
  const [viewMode, setViewMode] = useState<'status' | 'repo'>('status');

  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <StatsBar
        agents={agents}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        onLaunch={() => {/* Task 10 */}}
      />
      <main style={{ flex: 1, display: 'flex', padding: 14 }}>
        <p style={{ color: 'var(--fg-3)', margin: 'auto' }}>
          {agents.length} agent(s) discovered. Board coming in Task 7.
        </p>
      </main>
    </div>
  );
}
```

- [ ] **Step 4: Build and verify**

```bash
cd web && pnpm build && cd .. && go build ./cmd/aimux
```

Expected: BUILD OK. Stats bar renders with live agent count.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: add SSE hook and StatsBar component"
```

---

## Task 7: Kanban Board and Agent Cards

**Files:**
- Create: `web/src/components/KanbanBoard.tsx`
- Create: `web/src/components/AgentCard.tsx`
- Modify: `web/src/App.tsx`

This is the core UI component. Due to length, step code is provided as complete file contents rather than inline diffs.

- [ ] **Step 1: Create `web/src/components/AgentCard.tsx`**

The card displays provider badge, repo name, branch, last action, model, cost, and time. Provider color is determined by `providerName` field. Selected state applies red border with glow. Full component code should follow the mockup at `.superpowers/brainstorm/5763-1777821221/content/dashboard-v4.html` for styling reference.

Key props: `agent: Agent`, `selected: boolean`, `onClick: () => void`.

- [ ] **Step 2: Create `web/src/components/KanbanBoard.tsx`**

Two modes controlled by `viewMode` prop:
- `'status'`: columns are `['Active', 'Idle', 'Waiting', 'Error']`, agents grouped by `agent.status`
- `'repo'`: columns are unique `agent.name` values, agents grouped by repo name

Uses `@dnd-kit/core` and `@dnd-kit/sortable` for drag-and-drop. Each column is a droppable container, each card is a draggable item.

Key props: `agents: Agent[]`, `viewMode: 'status' | 'repo'`, `selectedId: string | null`, `onSelect: (id: string) => void`.

- [ ] **Step 3: Wire into App.tsx**

Replace the placeholder `<main>` with `<KanbanBoard>`, pass agents, viewMode, selectedId state, and onSelect handler.

- [ ] **Step 4: Build and verify**

```bash
cd web && pnpm build && cd .. && go build ./cmd/aimux
```

Expected: Kanban board renders with columns and cards. Clicking a card highlights it.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: add KanbanBoard with AgentCards and drag-and-drop"
```

---

## Task 8: Right Panel with Trace Tab

**Files:**
- Create: `web/src/hooks/useTraceStream.ts`
- Create: `web/src/components/TraceView.tsx`
- Create: `web/src/components/RightPanel.tsx`
- Modify: `web/src/App.tsx`
- Modify: `internal/frontend/web/sse.go` (add trace event streaming)

- [ ] **Step 1: Add trace subscription tracking to SSE**

Modify `internal/frontend/web/sse.go` to track per-client trace subscriptions. Add a `sync.Map` of `clientID → set of sessionIDs`. When the trace subscribe endpoint is called, add the sessionID to that client's set. In the SSE loop, for each subscribed session, tail the JSONL file and push `event: trace` events.

- [ ] **Step 2: Write test for trace SSE events**

Add to `sse_test.go`: test that after calling `POST /api/trace/subscribe/{sessionId}`, the SSE stream includes `event: trace` data for that session.

- [ ] **Step 3: Create `web/src/hooks/useTraceStream.ts`**

SSE hook that listens for `trace` events and maintains a `Turn[]` array for the subscribed session. On mount, calls `POST /api/trace/subscribe/{sessionId}`. On unmount, calls unsubscribe.

- [ ] **Step 4: Create `web/src/components/TraceView.tsx`**

Compact trace component matching the mockup. User turns in dark blocks, agent turns with red left border. Tool calls as pills. Collapsed diff previews. G/B/W annotation buttons per agent turn (call `POST /api/agents/{id}/annotate` on click).

- [ ] **Step 5: Create `web/src/components/RightPanel.tsx`**

Tabbed panel with `Trace | Session` tabs. Contains:
- Header: repo name, branch, tabs, fullscreen button, close button
- Stats ribbon: status, turns, tokens, cost, duration
- Tab content: `TraceView` (default) or `SessionView` (Task 9)
- Resizable left edge (CSS `resize` or mouse drag handler, width in localStorage)

Key props: `agent: Agent | null`, `onClose: () => void`.

- [ ] **Step 6: Wire into App.tsx**

When `selectedId` is set, render `<RightPanel>` to the right of the board. Board flexes to fill remaining width.

- [ ] **Step 7: Build, verify, commit**

```bash
cd web && pnpm build && cd .. && go build ./cmd/aimux
go test ./internal/frontend/web/... -v -count=1
git add -A
git commit -m "feat: add RightPanel with trace view and SSE trace streaming"
```

---

## Task 9: Session Tab with xterm.js Terminal

**Files:**
- Create: `internal/frontend/web/terminal.go`
- Create: `internal/frontend/web/terminal_test.go`
- Create: `web/src/components/SessionView.tsx`
- Modify: `internal/frontend/web/server.go` (wire WebSocket route)
- Modify: `web/src/components/RightPanel.tsx` (add Session tab content)

- [ ] **Step 1: Write the failing test for WebSocket terminal proxy**

Create `internal/frontend/web/terminal_test.go`:

```go
package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTerminalWebSocketRejectsMissingSession(t *testing.T) {
	s := NewServer(0)
	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	wsURL := strings.Replace(s.URL(), "http", "ws", 1) + "/api/terminal/nonexistent"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go get github.com/gorilla/websocket
go test ./internal/frontend/web/... -v -count=1 -run TestTerminalWebSocket
```

Expected: FAIL — route not defined.

- [ ] **Step 3: Create `internal/frontend/web/terminal.go`**

WebSocket handler that:
1. Extracts tmux session name from URL path
2. Validates the tmux session exists (`tmux has-session -t {name}`)
3. If valid, creates a PTY attached to `tmux attach-session -t {name}`
4. Proxies between WebSocket and PTY (bidirectional goroutines)
5. On disconnect, detaches cleanly

- [ ] **Step 4: Wire route in server.go**

```go
mux.HandleFunc("/api/terminal/{session}", s.handleTerminal)
```

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./internal/frontend/web/... -v -count=1
```

- [ ] **Step 6: Create `web/src/components/SessionView.tsx`**

xterm.js component that:
1. Creates `Terminal` instance with DevHub theme colors
2. Loads `FitAddon` for responsive resizing
3. Connects to `ws://host/api/terminal/{tmuxSession}`
4. Proxies keyboard input to WebSocket, WebSocket output to terminal
5. Handles resize events

- [ ] **Step 7: Wire into RightPanel Session tab**

When Session tab is active, render `<SessionView tmuxSession={agent.tmuxSession} />`.

- [ ] **Step 8: Build, verify, commit**

```bash
cd web && pnpm build && cd .. && go build ./cmd/aimux
go test ./internal/frontend/web/... -v -count=1
git add -A
git commit -m "feat: add xterm.js terminal with WebSocket proxy to tmux"
```

---

## Task 10: Launch Dialog

**Files:**
- Create: `web/src/components/LaunchDialog.tsx`
- Modify: `web/src/App.tsx` (wire launch dialog)

- [ ] **Step 1: Create `web/src/components/LaunchDialog.tsx`**

Modal dialog with 4 steps:
1. Provider selection (Claude, Codex, Gemini buttons)
2. Directory picker (text input with recent dirs dropdown)
3. Model selector (dropdown based on provider)
4. Mode selector (auto, plan, etc.)

Submit calls `POST /api/agents/launch` with the selections. On success, closes the dialog. The new agent appears on the board via the next SSE tick.

Styled with DevHub dark-black theme. Modal overlay with `var(--bg-1)` background, `var(--border)` borders.

- [ ] **Step 2: Wire into App.tsx**

Add `showLaunch` state. Pass `onLaunch={() => setShowLaunch(true)}` to StatsBar. Render `<LaunchDialog>` when `showLaunch` is true.

- [ ] **Step 3: Build, verify, commit**

```bash
cd web && pnpm build && cd .. && go build ./cmd/aimux
git add -A
git commit -m "feat: add launch dialog for spawning new agents"
```

---

## Task 11: Wire Discovery Orchestrator to Web Server

**Files:**
- Modify: `cmd/aimux/main.go` (create orchestrator and pass to web server)

- [ ] **Step 1: Update `runWeb()` and `runBoth()` to create real orchestrator**

```go
func createWebServer(port int) *web.Server {
	cfg, _ := config.Load(config.DefaultPath())
	disco := discovery.NewOrchestrator(
		provider.NewClaude(),
		provider.NewCodex(),
		provider.NewGemini(),
	)

	s := web.NewServer(port)
	s.SetDiscoverFunc(disco.Discover)
	s.SetLaunchFunc(func(providerName, dir, model, mode string) error {
		cmd := spawn.BuildCommand(providerName, dir, model, mode, cfg)
		shell := config.ResolveShell(cfg)
		return spawn.Launch(cmd, providerName, dir, "tmux", shell, "")
	})
	return s
}
```

Add imports for `config`, `discovery`, `provider`, `spawn`.

- [ ] **Step 2: Verify end-to-end with real agents**

```bash
go build ./cmd/aimux && ./aimux web
```

Open `http://127.0.0.1:3000` in a browser. Running Claude/Codex/Gemini sessions should appear on the Kanban board.

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "feat: wire real discovery orchestrator and spawn to web server"
```

---

## Task 12: Build Pipeline and .gitignore

**Files:**
- Create: `web/.gitignore`
- Modify: `.gitignore` (add web/dist to ignore, but keep placeholder)
- Create: `Makefile` additions (or `scripts/build-web.sh`)

- [ ] **Step 1: Create `web/.gitignore`**

```
node_modules/
dist/
```

- [ ] **Step 2: Add build script**

Add to `Makefile`:
```makefile
.PHONY: web-build web-dev

web-build:
	cd web && pnpm install && pnpm build

web-dev:
	cd web && pnpm dev

build-all: web-build build
```

- [ ] **Step 3: Verify full build from clean state**

```bash
make web-build
go build -o aimux ./cmd/aimux
./aimux --web
```

Expected: Single binary serves both TUI and web dashboard.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "chore: add web build pipeline and gitignore"
```
