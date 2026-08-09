package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/insight"
	"github.com/zanetworker/aimux/internal/plugin"
	"github.com/zanetworker/aimux/internal/sessiondiff"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/tasks"
	"github.com/zanetworker/aimux/internal/team"
	"github.com/zanetworker/aimux/internal/trace"
)

type launchRequest struct {
	Provider       string `json:"provider"`
	Dir            string `json:"dir"`
	Model          string `json:"model"`
	Mode           string `json:"mode"`
	Runtime        string `json:"runtime"`
	Execution      string `json:"execution"`
	Shell          string `json:"shell"`
	SessionManager string `json:"session_manager"`
	OTELEnabled    bool   `json:"otel_enabled"`
	TaskID         string `json:"task_id,omitempty"`
	TaskListID     string `json:"task_list_id,omitempty"`
	UserPrompt     string `json:"user_prompt,omitempty"`
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
	if req.Dir == "" {
		http.Error(w, "directory is required", http.StatusBadRequest)
		return
	}
	if info, err := os.Stat(req.Dir); err != nil || !info.IsDir() {
		http.Error(w, fmt.Sprintf("directory does not exist: %s", req.Dir), http.StatusBadRequest)
		return
	}

	// Assemble prompt: direct user prompt, or from task context
	prompt := req.UserPrompt
	if req.TaskID != "" && s.taskProvider != nil {
		taskList, err := s.taskProvider.ListTasks(req.TaskListID)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to fetch task: %v", err), http.StatusInternalServerError)
			return
		}

		// Find the task by ID
		var task *tasks.Task
		for i := range taskList {
			if taskList[i].ID == req.TaskID {
				task = &taskList[i]
				break
			}
		}
		if task == nil {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}

		// Render the prompt using the task template
		template := s.cfg.Tasks.PromptTemplate
		if template == "" {
			template = "Task: {title}\n\nNotes:\n{notes}\n\n{user_prompt}"
		}
		prompt = tasks.RenderPrompt(template, task.Title, task.Notes, req.UserPrompt)

		// Add a started note to the task
		note := "Session started by aimux at " + time.Now().Format(time.RFC3339)
		if err := s.taskProvider.AddNote(req.TaskListID, req.TaskID, note); err != nil {
			// Log but don't fail — note is nice-to-have
			fmt.Fprintf(os.Stderr, "Warning: failed to add note to task %s: %v\n", req.TaskID, err)
		}
	}

	opts := spawn.LaunchOpts{
		Provider:       req.Provider,
		Dir:            req.Dir,
		Model:          req.Model,
		Mode:           req.Mode,
		Prompt:         prompt,
		Runtime:        req.Runtime,
		Execution:      req.Execution,
		Shell:          req.Shell,
		SessionManager: req.SessionManager,
		OTELEnabled:    req.OTELEnabled,
	}
	result, err := s.launchFn(opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if result.SandboxName != "" && result.OTELSessionID != "" && s.sessionStore != nil {
		s.sessionStore.Put(result.SandboxName, result.OTELSessionID)
	}
	w.WriteHeader(http.StatusOK)
	resp := map[string]interface{}{
		"status":       "launched",
		"tmux_session": result.TmuxSession,
	}
	if result.SandboxName != "" {
		resp["sandbox_name"] = result.SandboxName
	}
	if result.OTELSessionID != "" {
		resp["otel_session_id"] = result.OTELSessionID
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		debuglog.Log("encode launch response: %v", err)
	}
}

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
			Turn: req.Turn, Label: req.Label, Note: req.Note, Timestamp: time.Now(),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		debuglog.Log("encode annotate response: %v", err)
	}
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
	if err := json.NewEncoder(w).Encode(map[string]any{"annotations": annotations}); err != nil {
		debuglog.Log("encode annotations response: %v", err)
	}
}

func (s *Server) handleUpdateSessionMeta(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FilePath      string   `json:"filePath"`
		Annotation    string   `json:"annotation"`
		Tags          []string `json:"tags"`
		Note          string   `json:"note"`
		Starred       *bool    `json:"starred"`
		ROIMultiplier *float64 `json:"roiMultiplier"`
		TaskType      *string  `json:"taskType"`
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
	meta.Annotation = req.Annotation
	if req.Tags != nil {
		meta.Tags = req.Tags
	}
	meta.Note = req.Note
	if req.Starred != nil {
		meta.Starred = *req.Starred
	}
	if req.ROIMultiplier != nil {
		meta.ROIMultiplier = *req.ROIMultiplier
	}
	if req.TaskType != nil {
		meta.TaskType = *req.TaskType
	}
	if err := history.SaveMeta(req.FilePath, meta); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		debuglog.Log("encode update meta response: %v", err)
	}
}

func (s *Server) handleGetSessionMeta(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("file")
	if filePath == "" {
		http.Error(w, "missing file param", http.StatusBadRequest)
		return
	}
	meta := history.LoadMeta(filePath)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(meta); err != nil {
		debuglog.Log("encode meta response: %v", err)
	}
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	if s.discoverFn == nil || s.killFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}

	agents, err := s.cachedDiscover()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, a := range agents {
		if a.SessionID == sessionID || fmt.Sprintf("%d", a.PID) == sessionID {
			if err := s.killFn(a.PID, a.TMuxSession); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(map[string]string{"status": "killed"}); err != nil {
				debuglog.Log("encode kill response: %v", err)
			}
			return
		}
	}
	http.Error(w, "agent not found", http.StatusNotFound)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "not implemented"}); err != nil {
		debuglog.Log("encode diff response: %v", err)
	}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	opts := history.DiscoverOpts{Dir: dir}
	sessions, err := history.Discover(opts, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []history.Session{}
	}

	result := make([]map[string]any, len(sessions))
	for i, s := range sessions {
		result[i] = map[string]any{
			"id":          s.ID,
			"provider":    s.Provider,
			"project":     s.Project,
			"filePath":    s.FilePath,
			"startTime":   s.StartTime.Format(time.RFC3339),
			"lastActive":  s.LastActive.Format(time.RFC3339),
			"turnCount":   s.TurnCount,
			"tokensIn":    s.TokensIn,
			"tokensOut":   s.TokensOut,
			"costUSD":     s.CostUSD,
			"firstPrompt": s.FirstPrompt,
			"title":       s.Title,
			"resumable":   s.Resumable,
			"annotation":  s.Annotation,
			"tags":        s.Tags,
			"note":        s.Note,
			"isSubagent":     s.IsSubagent,
			"permissionMode": s.PermissionMode,
			"starred":        s.Starred,
			"gitBranch":      s.GitBranch,
			"lastPrompt":     s.LastPrompt,
			"lastAction":     s.LastAction,
			"model":          s.Model,
			"roiMultiplier":  s.ROIMultiplier,
			"taskType":       s.TaskType,
			"durationMin":    s.DurationMin,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"sessions": result}); err != nil {
		debuglog.Log("encode history response: %v", err)
	}
}

func (s *Server) handleTraceSubscribe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "subscribed"}); err != nil {
		debuglog.Log("encode subscribe response: %v", err)
	}
}

func (s *Server) handleTraceUnsubscribe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "unsubscribed"}); err != nil {
		debuglog.Log("encode unsubscribe response: %v", err)
	}
}

func (s *Server) handleFastTrace(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	if file == "" {
		http.Error(w, "missing file param", http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(file, ".jsonl") && !strings.HasSuffix(file, ".json") {
		http.Error(w, "invalid file", http.StatusBadRequest)
		return
	}
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		providerName = "claude"
	}
	if s.providerLookupFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	p := s.providerLookupFn(providerName)
	if p == nil {
		http.Error(w, "unknown provider", http.StatusInternalServerError)
		return
	}
	turns, err := p.ParseTrace(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"turns": turnsToJSON(turns)}); err != nil {
		debuglog.Log("encode fast trace response: %v", err)
	}
}

func (s *Server) handleGetTrace(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	if s.discoverFn == nil || s.providerLookupFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	agents, err := s.cachedDiscover()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var matched *agent.Agent
	for i := range agents {
		if agents[i].SessionID == sessionID || fmt.Sprintf("%d", agents[i].PID) == sessionID {
			matched = &agents[i]
			break
		}
	}
	if matched == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	var turns []trace.Turn

	if matched.Location == "remote" && matched.SessionFile == "" {
		sandboxName := matched.SandboxName
		if sandboxName == "" {
			sandboxName = matched.Name
		}
		sid := matched.SessionID
		if !controller.UUIDValid(sid) {
			if s.sessionStore != nil {
				if mapped := s.sessionStore.Get(sandboxName); mapped != "" {
					sid = mapped
				}
			}
		}
		turns = controller.RemoteTraceParser(s.otelStore, sid, sandboxName)
	} else {
		sessionFile := matched.SessionFile
		providerName := matched.ProviderName
		p := s.providerLookupFn(providerName)
		if p == nil {
			http.Error(w, "unknown provider", http.StatusInternalServerError)
			return
		}
		var parseErr error
		turns, parseErr = p.ParseTrace(sessionFile)
		if parseErr != nil {
			http.Error(w, parseErr.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"turns": turnsToJSON(turns)}); err != nil {
		debuglog.Log("encode trace response: %v", err)
	}
}

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
			if a.FilePath != "" {
				action["filePath"] = a.FilePath
			}
			if a.OldString != "" {
				action["oldString"] = a.OldString
			}
			if a.NewString != "" {
				action["newString"] = a.NewString
			}
			if a.Content != "" {
				action["content"] = a.Content
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

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"results": []any{}}); err != nil {
			debuglog.Log("encode empty search response: %v", err)
		}
		return
	}
	matches, err := history.SearchContentWithSnippets(q, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	results := make([]map[string]string, len(matches))
	for i, m := range matches {
		results[i] = map[string]string{
			"sessionId": m.SessionID,
			"filePath":  m.FilePath,
			"snippet":   m.Snippet,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"results": results}); err != nil {
		debuglog.Log("encode search results response: %v", err)
	}
}

func (s *Server) handleExportJSONL(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	sessionFile := r.URL.Query().Get("file")
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		providerName = "claude"
	}

	if s.ctrl == nil || s.providerLookupFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}

	p := s.providerLookupFn(providerName)
	if p == nil {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}
	turns, err := p.ParseTrace(sessionFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	ctx := controller.ExportContext{
		SessionID:    sessionID,
		SessionFile:  sessionFile,
		ProviderName: providerName,
		Turns:        controller.TurnsToInputs(turns),
		EvalStore:    evaluation.NewStore(sessionID),
	}

	result, err := s.ctrl.ExportJSONL(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"status": "exported",
		"path":   result.Path,
		"count":  result.Count,
	}); err != nil {
		debuglog.Log("encode export JSONL response: %v", err)
	}
}

func (s *Server) handleExportOTEL(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	sessionFile := r.URL.Query().Get("file")
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		providerName = "claude"
	}

	if s.ctrl == nil || s.providerLookupFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}

	p := s.providerLookupFn(providerName)
	if p == nil {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}
	turns, err := p.ParseTrace(sessionFile)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	ctx := controller.ExportContext{
		SessionID:    sessionID,
		SessionFile:  sessionFile,
		ProviderName: providerName,
		Turns:        controller.TurnsToInputs(turns),
		EvalStore:    evaluation.NewStore(sessionID),
	}

	result, err := s.ctrl.ExportOTEL(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"status":   "exported",
		"endpoint": result.Path,
		"count":    result.Count,
	}); err != nil {
		debuglog.Log("encode export OTEL response: %v", err)
	}
}

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	var plugins []plugin.Plugin
	if s.pluginExec != nil {
		plugins = s.pluginExec.Plugins()
	}
	if plugins == nil {
		plugins = []plugin.Plugin{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"plugins": plugins}); err != nil {
		debuglog.Log("encode plugins response: %v", err)
	}
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
	if err := json.NewEncoder(w).Encode(data); err != nil {
		debuglog.Log("encode plugin data response: %v", err)
	}
}

func (s *Server) handleInsight(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Data   json.RawMessage `json:"data"`
		Prompt string          `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	model := s.cfg.Sessions.TitleModel
	if model == "" {
		model = "flash"
	}

	if s.cfg.Sessions.APIKey == "" {
		http.Error(w, "insight requires an API key (set sessions.api_key in config)", http.StatusServiceUnavailable)
		return
	}

	cfg := insight.Config{
		Model:  model,
		APIKey: s.cfg.Sessions.APIKey,
	}

	prompt := req.Prompt
	if prompt == "" {
		prompt = "You are analyzing a skill analytics dashboard for an AI coding agent system. " +
			"The data below shows skill invocations, correction rates, learning funnel metrics, and failure patterns. " +
			"For each section with data, provide 1-2 sentences of actionable insight. " +
			"Focus on: which skills need attention, what patterns indicate, and specific next steps. " +
			"Format as a JSON object where keys are section names and values are insight strings.\n\n" +
			"Dashboard data:\n" + string(req.Data)
	}

	result, err := insight.Generate(cfg, prompt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"insight": result}); err != nil {
		debuglog.Log("encode insight response: %v", err)
	}
}

func (s *Server) handleBrowseDir(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			http.Error(w, "unable to determine home directory", http.StatusInternalServerError)
			return
		}
		path = homeDir
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	type dirEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
	}

	var result []dirEntry
	for _, e := range entries {
		// Filter hidden files
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			continue
		}
		result = append(result, dirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
		})
	}

	// Sort: directories first, then alphabetically
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"path":    path,
		"entries": result,
	}); err != nil {
		debuglog.Log("encode browse dir response: %v", err)
	}
}

func (s *Server) handleRecentDirs(w http.ResponseWriter, r *http.Request) {
	if s.recentDirsFn == nil {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"directories": []RecentDirInfo{}}); err != nil {
			debuglog.Log("encode empty recent dirs response: %v", err)
		}
		return
	}

	dirs := s.recentDirsFn(20)
	if dirs == nil {
		dirs = []RecentDirInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"directories": dirs}); err != nil {
		debuglog.Log("encode recent dirs response: %v", err)
	}
}

func (s *Server) handleSessionDiffs(w http.ResponseWriter, r *http.Request) {
	sessionFile := r.URL.Query().Get("file")
	if sessionFile == "" {
		http.Error(w, "file parameter required", http.StatusBadRequest)
		return
	}

	if s.providerLookupFn == nil {
		http.Error(w, "trace parser not configured", http.StatusServiceUnavailable)
		return
	}

	parser := s.providerLookupFn("claude")
	turns, err := parser.ParseTrace(sessionFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("parse trace: %v", err), http.StatusNotFound)
		return
	}

	diffs := sessiondiff.Extract(turns)

	totalAdded, totalRemoved := 0, 0
	for _, d := range diffs {
		totalAdded += d.Added
		totalRemoved += d.Removed
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"files":        diffs,
		"totalFiles":   len(diffs),
		"totalAdded":   totalAdded,
		"totalRemoved": totalRemoved,
	}); err != nil {
		debuglog.Log("encode session diffs: %v", err)
	}
}

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	if s.discoverFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	agents, err := s.cachedDiscover()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type costEntry struct {
		Project    string  `json:"project"`
		Provider   string  `json:"provider"`
		Model      string  `json:"model"`
		TokensIn   int64   `json:"tokens_in"`
		TokensOut  int64   `json:"tokens_out"`
		CostUSD    float64 `json:"cost"`
		AgentCount int     `json:"agent_count"`
	}

	// Group by project
	groups := make(map[string]*costEntry)
	for _, a := range agents {
		proj := a.ShortProject()
		if proj == "" {
			proj = "(unknown)"
		}
		e, ok := groups[proj]
		if !ok {
			e = &costEntry{
				Project:  proj,
				Provider: a.ProviderName,
				Model:    a.ShortModel(),
			}
			groups[proj] = e
		}
		e.TokensIn += a.TokensIn
		e.TokensOut += a.TokensOut
		e.CostUSD += a.EstCostUSD
		e.AgentCount++
	}

	result := make([]costEntry, 0, len(groups))
	for _, e := range groups {
		result = append(result, *e)
	}
	// Sort by cost descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].CostUSD > result[j].CostUSD
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"costs": result}); err != nil {
		debuglog.Log("encode costs response: %v", err)
	}
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.discoverFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	agents, err := s.cachedDiscover()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy to avoid mutating cached slice
	result := make([]agent.Agent, len(agents))
	copy(result, agents)

	if q := r.URL.Query().Get("filter"); q != "" {
		result = controller.FilterAgents(result, q)
	}
	if sortField := r.URL.Query().Get("sort"); sortField != "" {
		controller.SortAgents(result, sortField)
	}

	// Serialize
	items := make([]map[string]any, len(result))
	for i, a := range result {
		items[i] = map[string]any{
			"pid":          a.PID,
			"sessionId":    a.SessionID,
			"name":         a.Name,
			"provider":     a.ProviderName,
			"model":        a.Model,
			"shortModel":   a.ShortModel(),
			"project":      a.ShortProject(),
			"workingDir":   a.WorkingDir,
			"status":       a.Status.String(),
			"source":       a.Source.String(),
			"sessionFile":  a.SessionFile,
			"tmuxSession":  a.TMuxSession,
			"tokensIn":     a.TokensIn,
			"tokensOut":    a.TokensOut,
			"costUSD":      a.EstCostUSD,
			"cpuPercent":   a.CPUPercent,
			"memoryMB":     a.MemoryMB,
			"gitBranch":    a.GitBranch,
			"lastAction":   a.LastAction,
			"startTime":    a.StartTime.Format(time.RFC3339),
			"lastActivity": a.LastActivity.Format(time.RFC3339),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"agents": items}); err != nil {
		debuglog.Log("encode agents response: %v", err)
	}
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "session ID required", http.StatusBadRequest)
		return
	}

	sessions, err := history.Discover(history.DiscoverOpts{}, "")
	if err != nil {
		http.Error(w, fmt.Sprintf("discover sessions: %v", err), http.StatusInternalServerError)
		return
	}

	for _, sess := range sessions {
		if sess.ID == sessionID {
			if err := controller.DeleteSession(sess); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]string{"status": "deleted"}); err != nil {
				debuglog.Log("encode delete session response: %v", err)
			}
			return
		}
	}
	http.Error(w, "session not found", http.StatusNotFound)
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, "agent ID required", http.StatusBadRequest)
		return
	}

	if s.discoverFn == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}
	agents, err := s.cachedDiscover()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Find the agent by PID or session ID
	var target *agent.Agent
	for i := range agents {
		if agents[i].SessionID == agentID || fmt.Sprintf("%d", agents[i].PID) == agentID {
			target = &agents[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	action := controller.DetermineKillAction(*target)

	switch action.Type {
	case controller.KillProcess:
		if s.killFn == nil {
			http.Error(w, "kill not configured", http.StatusServiceUnavailable)
			return
		}
		if err := s.killFn(target.PID, target.TMuxSession); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case controller.KillPod:
		// Use kubectl to delete the pod
		cmd := exec.Command("kubectl", "delete", "pod", action.PodName, "-n", action.Namespace) // #nosec G204
		if out, err := cmd.CombinedOutput(); err != nil {
			http.Error(w, fmt.Sprintf("kubectl delete: %s: %v", string(out), err), http.StatusInternalServerError)
			return
		}
	case controller.KillRemoveOnly:
		// Session-only entry, nothing to kill
	case controller.KillSandbox:
		if s.composeEngine != nil {
			if err := controller.ExecuteKillSandbox(action, s.composeEngine); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"status":   "killed",
		"killType": action.Type,
	}); err != nil {
		debuglog.Log("encode kill response: %v", err)
	}
}

func (s *Server) handleGenerateTitles(w http.ResponseWriter, r *http.Request) {
	sessions, err := history.Discover(history.DiscoverOpts{}, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	cfg := history.TitleConfig{
		Enabled: true,
		Model:   s.cfg.Sessions.TitleModel,
		APIKey:  s.cfg.Sessions.APIKey,
		Output:  io.Discard,
	}

	count, err := history.GenerateTitles(sessions, cfg)

	result := map[string]any{"generated": count}
	if err != nil {
		result["error"] = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	if encErr := json.NewEncoder(w).Encode(result); encErr != nil {
		debuglog.Log("encode generate-titles response: %v", encErr)
	}
}

func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := team.ListTeamsDefault()
	if err != nil {
		// No teams directory or unreadable is not an error for the API;
		// just return an empty list.
		teams = nil
	}

	type teamResp struct {
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Members     []team.Member `json:"members"`
	}

	items := make([]teamResp, len(teams))
	for i, t := range teams {
		members := t.Members
		if members == nil {
			members = []team.Member{}
		}
		items[i] = teamResp{
			Name:        t.Name,
			Description: t.Description,
			Members:     members,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"teams": items}); err != nil {
		debuglog.Log("encode teams response: %v", err)
	}
}

func (s *Server) handleProviderHealth(w http.ResponseWriter, r *http.Request) {
	type providerStatus struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Installed bool   `json:"installed"`
	}

	names := []string{"claude", "codex", "gemini"}
	providers := make([]providerStatus, 0, len(names))
	for _, name := range names {
		_, err := exec.LookPath(name)
		providers = append(providers, providerStatus{
			Name:      name,
			Enabled:   s.cfg.IsProviderEnabled(name),
			Installed: err == nil,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"providers": providers}); err != nil {
		debuglog.Log("encode provider health response: %v", err)
	}
}

func (s *Server) handleGetROIConfig(w http.ResponseWriter, _ *http.Request) {
	rate := s.cfg.ROI.HourlyRate
	if rate <= 0 {
		rate = 150.0
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"hourlyRate":  rate,
		"multipliers": history.TaskMultiplier,
	})
}
