# Annotation Parity: Web Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the web dashboard to full parity with the TUI for turn-level annotations, session-level annotations, tags, and notes.

**Architecture:** The backend (`evaluation.Store` for turn-level, `history.SaveMeta/LoadMeta` for session-level) already exists. We need: (1) new API endpoints that call these directly (no callback functions needed), (2) frontend components to display and edit annotations. All business logic stays in Go; the frontend is a thin rendering layer.

**Tech Stack:** Go (net/http handlers), React/TypeScript (frontend components), existing `evaluation` and `history` packages.

---

### Task 1: Backend - Turn-level annotation endpoints

The existing `handleAnnotate` uses a callback (`annotateFn`) that is never wired in `cmd/aimux/main.go`. Replace it with direct `evaluation.Store` calls, and add GET + DELETE endpoints.

**Files:**
- Modify: `internal/frontend/web/handlers.go` - rewrite `handleAnnotate`, add `handleGetAnnotations`
- Modify: `internal/frontend/web/server.go` - add route, remove `annotateFn` field
- Modify: `internal/frontend/web/handlers_test.go` - update test
- Modify: `cmd/aimux/main.go` - remove `SetAnnotateFunc` call if it exists

- [ ] **Step 1: Write failing test for GET annotations**

```go
// In handlers_test.go
func TestGetAnnotationsHandler(t *testing.T) {
	s := NewServer(0)

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	// POST an annotation first
	body, _ := json.Marshal(map[string]any{
		"turn": 1, "label": "good", "note": "clean code",
	})
	resp, err := http.Post(s.URL()+"/api/sessions/test-session-123/annotate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST expected 200, got %d", resp.StatusCode)
	}

	// GET annotations
	resp, err = http.Get(s.URL() + "/api/sessions/test-session-123/annotations")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Annotations []map[string]any `json:"annotations"`
	}
	json.NewDecoder(resp.Body).Decode(&payload)
	if len(payload.Annotations) == 0 {
		t.Fatal("expected at least one annotation")
	}
	if payload.Annotations[0]["label"] != "good" {
		t.Errorf("expected label good, got %v", payload.Annotations[0]["label"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/frontend/web/... -run TestGetAnnotationsHandler -v -timeout 30s`
Expected: FAIL (routes don't exist yet)

- [ ] **Step 3: Implement handlers**

In `handlers.go`, rewrite `handleAnnotate` to use `evaluation.Store` directly (remove dependency on `annotateFn`), and add `handleGetAnnotations`:

```go
func (s *Server) handleAnnotateTurn(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var req struct {
		Turn  int    `json:"turn"`
		Label string `json:"label"`
		Note  string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	store := evaluation.NewStore(sessionID)
	if req.Label == "" {
		if err := store.Remove(req.Turn); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := store.Save(evaluation.Annotation{
			Turn:      req.Turn,
			Label:     req.Label,
			Note:      req.Note,
			Timestamp: time.Now(),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleGetAnnotations(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	store := evaluation.NewStore(sessionID)
	annotations, err := store.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if annotations == nil {
		annotations = []evaluation.Annotation{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"annotations": annotations})
}
```

In `server.go`, add routes:
```go
mux.HandleFunc("POST /api/sessions/{id}/annotate", s.handleAnnotateTurn)
mux.HandleFunc("GET /api/sessions/{id}/annotations", s.handleGetAnnotations)
```

Keep the old `/api/agents/{id}/annotate` route pointing to `handleAnnotateTurn` for backward compat.

Remove `annotateFn` field and `SetAnnotateFunc` from `Server` struct.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/frontend/web/... -run TestGetAnnotationsHandler -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Clean up test evaluation files**

Add `t.Cleanup` to remove test evaluation files created by the test:
```go
t.Cleanup(func() {
	home, _ := os.UserHomeDir()
	os.Remove(filepath.Join(home, ".aimux", "evaluations", "test-session-123.jsonl"))
})
```

- [ ] **Step 6: Commit**

```bash
git add internal/frontend/web/handlers.go internal/frontend/web/server.go internal/frontend/web/handlers_test.go
git commit -m "feat: turn-level annotation endpoints using evaluation.Store directly"
```

---

### Task 2: Backend - Session-level metadata endpoints

Add endpoints for session-level annotation (achieved/partial/failed/abandoned), tags, and notes using `history.SaveMeta/LoadMeta`.

**Files:**
- Modify: `internal/frontend/web/handlers.go` - add `handleSessionMeta`, `handleUpdateSessionMeta`
- Modify: `internal/frontend/web/server.go` - add routes
- Modify: `internal/frontend/web/handlers_test.go` - add tests

- [ ] **Step 1: Write failing test**

```go
func TestSessionMetaHandler(t *testing.T) {
	// Create a temp session file to use as the target
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "test-meta-session.jsonl")
	os.WriteFile(sessionFile, []byte(`{"type":"user"}`+"\n"), 0o644)

	s := NewServer(0)
	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	// Update meta
	body, _ := json.Marshal(map[string]any{
		"filePath":   sessionFile,
		"annotation": "achieved",
		"tags":       []string{"clean-code"},
		"note":       "Great session",
	})
	resp, err := http.Post(s.URL()+"/api/sessions/meta", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST expected 200, got %d", resp.StatusCode)
	}

	// Verify the meta file was written
	meta := history.LoadMeta(sessionFile)
	if meta.Annotation != "achieved" {
		t.Errorf("expected annotation achieved, got %s", meta.Annotation)
	}
	if len(meta.Tags) != 1 || meta.Tags[0] != "clean-code" {
		t.Errorf("expected tags [clean-code], got %v", meta.Tags)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/frontend/web/... -run TestSessionMetaHandler -v -timeout 30s`
Expected: FAIL

- [ ] **Step 3: Implement handler**

```go
func (s *Server) handleUpdateSessionMeta(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FilePath   string   `json:"filePath"`
		Annotation string   `json:"annotation"`
		Tags       []string `json:"tags"`
		Note       string   `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.FilePath == "" {
		http.Error(w, "filePath required", http.StatusBadRequest)
		return
	}

	meta := history.LoadMeta(req.FilePath)
	if req.Annotation != "" || meta.Annotation != "" {
		meta.Annotation = req.Annotation
	}
	if req.Tags != nil {
		meta.Tags = req.Tags
	}
	if req.Note != "" || meta.Note != "" {
		meta.Note = req.Note
	}

	if err := history.SaveMeta(req.FilePath, meta); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
```

Route: `mux.HandleFunc("POST /api/sessions/meta", s.handleUpdateSessionMeta)`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/frontend/web/... -run TestSessionMetaHandler -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/frontend/web/handlers.go internal/frontend/web/server.go internal/frontend/web/handlers_test.go
git commit -m "feat: session-level metadata endpoint for annotations, tags, notes"
```

---

### Task 3: Frontend - Turn-level annotation display and interaction

Update `TraceView` to load existing annotations, show active labels on each turn, and support notes.

**Files:**
- Modify: `web/src/components/TraceView.tsx` - load annotations, show labels, add note input
- Modify: `web/src/types.ts` - add `Annotation` type

- [ ] **Step 1: Add Annotation type to types.ts**

```typescript
export interface Annotation {
  turn: number;
  label: string;
  note: string;
  timestamp: string;
}
```

- [ ] **Step 2: Update TraceView to fetch and display annotations**

In `TraceView.tsx`:

1. Add state: `const [annotations, setAnnotations] = useState<Map<number, Annotation>>(new Map());`
2. Add state: `const [noteInput, setNoteInput] = useState<{ turn: number; text: string } | null>(null);`
3. Fetch annotations on mount:
```typescript
useEffect(() => {
  if (!sessionId) return;
  fetch(`/api/sessions/${sessionId}/annotations`)
    .then(r => r.ok ? r.json() : null)
    .then(d => {
      if (!d?.annotations) return;
      const m = new Map<number, Annotation>();
      for (const a of d.annotations) {
        m.set(a.turn, a); // last one wins (latest annotation per turn)
      }
      setAnnotations(m);
    })
    .catch(() => {});
}, [sessionId, turns.length]);
```

4. Update `handleAnnotate` to also update local state and support toggle-off:
```typescript
const handleAnnotate = async (turnNumber: number, label: string) => {
  const current = annotations.get(turnNumber);
  const newLabel = current?.label === label ? '' : label;
  await fetch(`/api/sessions/${sessionId}/annotate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ turn: turnNumber, label: newLabel, note: noteInput?.turn === turnNumber ? noteInput.text : '' }),
  });
  setAnnotations(prev => {
    const next = new Map(prev);
    if (newLabel) {
      next.set(turnNumber, { turn: turnNumber, label: newLabel, note: '', timestamp: new Date().toISOString() });
    } else {
      next.delete(turnNumber);
    }
    return next;
  });
};
```

5. Show active label on collapsed row (colored dot/badge next to turn number):
```typescript
{annotations.has(turn.number) && (
  <span style={{
    fontSize: 8, fontWeight: 700, padding: '1px 4px', borderRadius: 2,
    color: labelColor(annotations.get(turn.number)!.label),
    border: `1px solid ${labelColor(annotations.get(turn.number)!.label)}`,
  }}>
    {annotations.get(turn.number)!.label.toUpperCase()}
  </span>
)}
```

6. Replace the fixed G/B/W buttons with the full label set matching the TUI:

```typescript
const labels = [
  { key: 'good', short: 'G', color: 'var(--green)' },
  { key: 'bad', short: 'B', color: 'var(--accent)' },
  { key: 'waste', short: 'W', color: 'var(--orange)' },
  { key: 'error', short: 'E', color: 'var(--purple)' },
];
```

7. Add a note input that appears when clicking a "Note" button in the footer:
```typescript
<button onClick={() => setNoteInput(noteInput?.turn === turn.number ? null : { turn: turn.number, text: annotations.get(turn.number)?.note || '' })}>
  Note
</button>
{noteInput?.turn === turn.number && (
  <div style={{ display: 'flex', gap: 4, marginTop: 4 }}>
    <input
      type="text"
      value={noteInput.text}
      onChange={e => setNoteInput({ ...noteInput, text: e.target.value })}
      onKeyDown={e => { if (e.key === 'Enter') { handleAnnotate(turn.number, annotations.get(turn.number)?.label || 'good'); setNoteInput(null); } }}
      placeholder="Add note..."
      autoFocus
      style={{ flex: 1, padding: '3px 6px', fontSize: 10, background: 'var(--bg-0)', border: '1px solid var(--border)', borderRadius: 3, color: 'var(--fg)', outline: 'none' }}
    />
    <button onClick={() => setNoteInput(null)} style={{ background: 'transparent', border: 'none', color: 'var(--fg-3)', cursor: 'pointer', fontSize: 10 }}>Esc</button>
  </div>
)}
```

8. Show existing note below annotation buttons if one exists:
```typescript
{annotations.get(turn.number)?.note && (
  <span style={{ fontSize: 9, fontStyle: 'italic', color: 'var(--fg-3)' }}>
    "{annotations.get(turn.number)!.note}"
  </span>
)}
```

- [ ] **Step 3: Build and verify**

Run: `cd web && npx tsc --noEmit && npm run build`
Expected: Build succeeds with zero errors

- [ ] **Step 4: Commit**

```bash
git add web/src/components/TraceView.tsx web/src/types.ts
git commit -m "feat: turn-level annotation display, toggle, notes in trace view"
```

---

### Task 4: Frontend - Session-level annotations in SessionsTable

Add annotation cycling, tag editing, and notes to the sessions table. Matches TUI keybindings: annotation cycle (click), tags (inline edit), notes (inline edit).

**Files:**
- Modify: `web/src/components/SessionsTable.tsx` - add annotation/tag/note editing UI

- [ ] **Step 1: Add session annotation cycling**

Add a click handler on the annotation badge that cycles through: achieved -> partial -> failed -> abandoned -> (clear). POST to `/api/sessions/meta` on each click:

```typescript
const annotationCycle = ['achieved', 'partial', 'failed', 'abandoned', ''];

const handleCycleAnnotation = async (session: HistorySession) => {
  const current = session.annotation || '';
  const idx = annotationCycle.indexOf(current);
  const next = annotationCycle[(idx + 1) % annotationCycle.length];
  await fetch('/api/sessions/meta', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ filePath: session.filePath, annotation: next }),
  });
  setSessions(prev => prev.map(s => s.id === session.id ? { ...s, annotation: next } : s));
};
```

Add a clickable annotation cell in each row. If no annotation, show a subtle "+" button. If annotated, show the colored badge (clickable to cycle):

```typescript
<td style={{ padding: '4px 8px', width: 80 }}>
  <button
    onClick={(e) => { e.stopPropagation(); handleCycleAnnotation(s); }}
    title="Cycle: achieved → partial → failed → abandoned → clear"
    style={{
      background: 'transparent',
      border: s.annotation ? `1px solid ${annotationColor(s.annotation)}` : '1px solid var(--border)',
      color: s.annotation ? annotationColor(s.annotation) : 'var(--fg-4)',
      fontSize: 8, fontWeight: 700, textTransform: 'uppercase',
      padding: '2px 6px', borderRadius: 3, cursor: 'pointer',
    }}
  >
    {s.annotation || '+'}
  </button>
</td>
```

- [ ] **Step 2: Add inline tag editing**

Add state: `const [editingTagsId, setEditingTagsId] = useState<string | null>(null);`
Add state: `const [tagInput, setTagInput] = useState('');`

When user clicks on a tag cell, show an input. On Enter, POST to `/api/sessions/meta`:

```typescript
const handleSaveTags = async (session: HistorySession) => {
  const tags = tagInput.split(',').map(t => t.trim()).filter(Boolean);
  await fetch('/api/sessions/meta', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ filePath: session.filePath, tags }),
  });
  setSessions(prev => prev.map(s => s.id === session.id ? { ...s, tags } : s));
  setEditingTagsId(null);
  setTagInput('');
};
```

- [ ] **Step 3: Add inline note editing**

Same pattern as tags. Add state for `editingNoteId`, show a text input on click, POST on Enter.

- [ ] **Step 4: Add annotation and tags columns to table header**

Add between Cost and Tokens columns:
```typescript
<th>Eval</th>
<th>Tags</th>
```

- [ ] **Step 5: Build and verify**

Run: `cd web && npx tsc --noEmit && npm run build`
Expected: Build succeeds

- [ ] **Step 6: Commit**

```bash
git add web/src/components/SessionsTable.tsx
git commit -m "feat: session-level annotation cycling, tag editing, notes in sessions table"
```

---

### Task 5: Frontend - Session-level annotations in RightPanel header

Show and edit session-level annotation, tags, and notes in the RightPanel header for the selected session.

**Files:**
- Modify: `web/src/components/RightPanel.tsx` - add annotation controls to header

- [ ] **Step 1: Add annotation badge to RightPanel header**

Below the agent name, add a row with:
- Annotation badge (clickable to cycle, same as SessionsTable)
- Tags (shown as pills, click to edit)
- Note (shown as italic text, click to edit)

The RightPanel receives an `agent` prop which has `SessionFile` (the filePath needed for `SaveMeta`). For history sessions, this is already set.

```typescript
const [sessionMeta, setSessionMeta] = useState<{ annotation: string; tags: string[]; note: string }>({ annotation: '', tags: [], note: '' });

// Fetch session meta on mount
useEffect(() => {
  if (!agent.SessionFile) return;
  // We can derive meta from the history endpoint, or add a GET /api/sessions/meta?file=
  // For now, we can parse it from the annotations response
}, [agent.SessionFile]);
```

- [ ] **Step 2: Build and verify**

Run: `cd web && npx tsc --noEmit && npm run build`

- [ ] **Step 3: Commit**

```bash
git add web/src/components/RightPanel.tsx
git commit -m "feat: session-level annotation controls in right panel header"
```

---

### Task 6: Backend - Add GET session meta endpoint

The frontend needs to fetch current meta for a session file.

**Files:**
- Modify: `internal/frontend/web/handlers.go` - add `handleGetSessionMeta`
- Modify: `internal/frontend/web/server.go` - add route

- [ ] **Step 1: Implement handler**

```go
func (s *Server) handleGetSessionMeta(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("file")
	if filePath == "" {
		http.Error(w, "missing file param", http.StatusBadRequest)
		return
	}
	meta := history.LoadMeta(filePath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}
```

Route: `mux.HandleFunc("GET /api/sessions/meta", s.handleGetSessionMeta)`

- [ ] **Step 2: Run full test suite**

Run: `go test ./internal/frontend/web/... -timeout 30s -run 'TestHistoryHandler|TestSessionMetaHandler|TestGetAnnotationsHandler'`
Expected: All PASS

- [ ] **Step 3: Commit**

```bash
git add internal/frontend/web/handlers.go internal/frontend/web/server.go
git commit -m "feat: GET session meta endpoint for loading annotation/tags/notes"
```

---

### Task 7: Full integration test and final build

- [ ] **Step 1: Run full Go test suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: All pass (except pre-existing TestSSEStreamsAgentState flake)

- [ ] **Step 2: Run frontend build**

Run: `cd web && npx tsc --noEmit && npm run build`
Expected: Zero errors

- [ ] **Step 3: Build binary**

Run: `go build -o aimux ./cmd/aimux`
Expected: Success

- [ ] **Step 4: Manual verification**

Start: `./aimux web --port 3001`
Verify:
1. Sessions tab: click annotation badge cycles through labels
2. Sessions tab: click tag cell to edit, Enter saves
3. Trace view: G/B/W/E buttons toggle, active label shown on collapsed row
4. Trace view: Note button opens input, Enter saves
5. RightPanel: session annotation/tags visible
6. Refresh page: all annotations/tags/notes persist

- [ ] **Step 5: Final commit and push**

```bash
git add -A
git commit -m "feat: annotation parity between web dashboard and TUI"
git push
```
