# Web Backend Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate duplicated logic in the web frontend by making the Go backend the single source of truth for trace parsing, cost calculation, and tool detail extraction, using the same provider-based parsers the TUI uses.

**Architecture:** The web server already has `SetTraceParseFn` wired to `provider.Claude.ParseTrace` but the trace handlers ignore it and use a duplicated `parseTailTurns`. We delete `parseTailTurns`, make both trace endpoints use the provider-based parser via a new `SetProviderLookup` function (so the correct provider is used per agent, not just Claude), and convert `trace.Turn` to a rich JSON response server-side. The frontend becomes a thin rendering layer.

**Tech Stack:** Go (backend), React/TypeScript (frontend, rendering only)

---

### Task 1: Replace trace handlers with provider-based parsing

**Files:**
- Modify: `internal/frontend/web/server.go` -- add `SetProviderLookup`, remove `SetTraceParseFn`
- Modify: `internal/frontend/web/handlers.go` -- delete `parseTailTurns`, `parseTailTurnsRetry`, `toolSnippet`, `fillToolDetail`, `truncate`, `extractText`; rewrite `handleGetTrace` and `handleFastTrace` to use provider parser
- Modify: `cmd/aimux/main.go` -- wire `SetProviderLookup` instead of `SetTraceParseFn`

- [ ] **Step 1: Write the failing test**

Create a test that verifies the trace endpoint returns rich turn data using the provider parser. Add to `internal/frontend/web/handlers_test.go`:

```go
func TestHandleGetTrace_UsesProviderParser(t *testing.T) {
    // Create a server with a mock provider lookup that returns a Claude parser
    s := NewServer(0)
    // ... verify that GET /api/agents/{id}/trace returns turns with
    // outputText, model, tokens, cost, and rich tool actions
}
```

- [ ] **Step 2: Add `SetProviderLookup` to server.go**

Replace `traceParseFn` with a provider lookup function:

```go
type Server struct {
    // ... existing fields
    providerLookupFn func(providerName string) interface{ ParseTrace(string) ([]trace.Turn, error) }
}

func (s *Server) SetProviderLookup(fn func(string) interface{ ParseTrace(string) ([]trace.Turn, error) }) {
    s.providerLookupFn = fn
}
```

- [ ] **Step 3: Create `turnsToJSON` conversion function**

Single function that converts `[]trace.Turn` to `[]map[string]any` with all rich data. This replaces both the `parseTailTurns` output format AND the `main.go` conversion. Place in `handlers.go`:

```go
func turnsToJSON(turns []trace.Turn) []map[string]any {
    result := make([]map[string]any, len(turns))
    for i, t := range turns {
        actions := make([]map[string]any, len(t.Actions))
        for j, a := range t.Actions {
            action := map[string]any{
                "name":     a.Name,
                "snippet":  a.Snippet,
                "success":  a.Success,
                "errorMsg": a.ErrorMsg,
            }
            if a.OldString != "" {
                action["oldString"] = a.OldString
            }
            if a.NewString != "" {
                action["newString"] = a.NewString
            }
            actions[j] = action
        }
        result[i] = map[string]any{
            "number":     t.Number,
            "timestamp":  t.Timestamp.Format(time.RFC3339),
            "userText":   strings.Join(t.UserLines, "\n"),
            "outputText": strings.Join(t.OutputLines, "\n"),
            "actions":    actions,
            "tokensIn":   t.TokensIn,
            "tokensOut":  t.TokensOut,
            "costUSD":    t.CostUSD,
            "model":      t.Model,
        }
    }
    return result
}
```

- [ ] **Step 4: Rewrite `handleGetTrace`**

Use provider lookup to find the right parser for the agent:

```go
func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
    sessionID := r.PathValue("id")
    agents, err := s.discoverFn()
    // ... find agent by sessionID
    // Use providerLookupFn to get the right parser
    p := s.providerLookupFn(agent.ProviderName)
    turns, err := p.ParseTrace(agent.SessionFile)
    // ... turnsToJSON(turns) and return
}
```

- [ ] **Step 5: Rewrite `handleFastTrace`**

For the file-based endpoint, determine provider from file path pattern or default to Claude:

```go
func (s *Server) handleFastTrace(w http.ResponseWriter, r *http.Request) {
    file := r.URL.Query().Get("file")
    providerName := r.URL.Query().Get("provider")
    if providerName == "" { providerName = "claude" }
    p := s.providerLookupFn(providerName)
    turns, err := p.ParseTrace(file)
    // ... turnsToJSON(turns) and return
}
```

- [ ] **Step 6: Delete dead code from handlers.go**

Remove: `parseTailTurns`, `parseTailTurnsRetry`, `toolSnippet`, `fillToolDetail`, `truncate`, `extractText`. These are all replaced by the provider parser.

- [ ] **Step 7: Update `cmd/aimux/main.go`**

Replace `SetTraceParseFn` with `SetProviderLookup`:

```go
s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
    p := disco.ProviderFor(name)
    if p == nil {
        return &provider.Claude{} // fallback
    }
    return p
})
```

Remove the old `SetTraceParseFn` call and the `claudeProvider` variable.

- [ ] **Step 8: Run tests and verify**

```bash
go build ./...
go vet ./...
go test ./... -timeout 30s
```

- [ ] **Step 9: Commit**

```bash
git add internal/frontend/web/ cmd/aimux/main.go
git commit -m "refactor: use provider ParseTrace for web trace endpoints

Delete duplicated parseTailTurns and use the same provider-based trace
parsing the TUI uses. handleGetTrace looks up the correct provider per
agent. Removes ~150 lines of duplicated parsing logic."
```

### Task 2: Simplify frontend trace hook

**Files:**
- Modify: `web/src/hooks/useTraceStream.ts` -- remove field-level defaults and coercion (backend now sends correct types)
- Modify: `web/src/types.ts` -- `success` field becomes boolean (no more string "true")

- [ ] **Step 1: Update `useTraceStream.ts`**

The backend now returns `success` as a boolean and all fields are properly typed. Simplify the mapping:

```typescript
setTurns(data.turns.map((t: any) => ({
  number: t.number,
  timestamp: t.timestamp,
  userText: t.userText || '',
  outputText: t.outputText || '',
  actions: t.actions || [],
  tokensIn: t.tokensIn || 0,
  tokensOut: t.tokensOut || 0,
  costUSD: t.costUSD || 0,
  model: t.model || '',
})));
```

- [ ] **Step 2: Pass `provider` query param in fast trace URL**

```typescript
const url = sessionFile
  ? `/api/trace?file=${encodeURIComponent(sessionFile)}&provider=claude`
  : `/api/agents/${sessionId}/trace`;
```

- [ ] **Step 3: Verify in browser, commit**

```bash
cd web && npx tsc --noEmit
git add web/src/hooks/useTraceStream.ts web/src/types.ts
git commit -m "refactor: simplify trace hook, backend handles all parsing"
```

### Task 3: Add architectural rule to CLAUDE.md

**Files:**
- Modify: `/Users/azaalouk/go/src/github.com/zanetworker/aimux/.claude/CLAUDE.md`

- [ ] **Step 1: Add "Thin Frontend" rule**

Add to the Architecture Rules section:

```markdown
## Thin Frontend Rule

The web frontend is a rendering layer only. All business logic lives in Go core packages.

**Backend owns:**
- Trace parsing (via `provider.ParseTrace`)
- Cost calculation (via `cost.Calculate`)
- Token counting, model identification
- Tool input extraction and snippet generation
- Session discovery and matching
- Search (via `history.SearchContent`)

**Frontend owns:**
- Rendering (React components, styles, layout)
- UI state (expanded/collapsed, selected, fullscreen)
- User interaction (click, keyboard, resize)

**When adding a web feature:**
1. If it needs data transformation, add a Go API endpoint using core packages
2. The frontend fetches and renders -- no parsing, no business logic
3. If the TUI already does it, the web must use the same core function
4. Never reimplement Go logic in TypeScript
```

- [ ] **Step 2: Commit**

```bash
git add .claude/CLAUDE.md
git commit -m "docs: add thin frontend architecture rule"
```
