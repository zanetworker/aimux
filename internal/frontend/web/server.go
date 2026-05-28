package web

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/plugin"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/tasks"
	"github.com/zanetworker/aimux/internal/trace"
)

type Server struct {
	port         int
	listener     net.Listener
	srv          *http.Server
	discoverFn   func() ([]agent.Agent, error)
	launchFn     func(opts spawn.LaunchOpts) (spawn.LaunchResult, error)
	providerLookupFn func(providerName string) interface{ ParseTrace(string) ([]trace.Turn, error) }
	killFn           func(pid int, tmuxSession string) error
	ctrl             *controller.Controller
	pluginExec       *plugin.Executor
	cfg              config.Config
	taskProvider     tasks.Provider
	recentDirsFn     func(int) []RecentDirInfo

	// Discovery cache to avoid redundant ps/tmux scans
	cacheMu     sync.Mutex
	cacheAgents []agent.Agent
	cacheTime   time.Time
}

func NewServer(port int) *Server {
	return &Server{port: port}
}

func (s *Server) SetDiscoverFunc(fn func() ([]agent.Agent, error)) {
	s.discoverFn = fn
}

func (s *Server) SetLaunchFunc(fn func(opts spawn.LaunchOpts) (spawn.LaunchResult, error)) {
	s.launchFn = fn
}

func (s *Server) SetProviderLookup(fn func(string) interface{ ParseTrace(string) ([]trace.Turn, error) }) {
	s.providerLookupFn = fn
}

func (s *Server) SetKillFunc(fn func(pid int, tmuxSession string) error) {
	s.killFn = fn
}

func (s *Server) cachedDiscover() ([]agent.Agent, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if time.Since(s.cacheTime) < 2*time.Second && s.cacheAgents != nil {
		return s.cacheAgents, nil
	}

	agents, err := s.discoverFn()
	if err != nil {
		return nil, err
	}
	s.cacheAgents = agents
	s.cacheTime = time.Now()
	return agents, nil
}

func (s *Server) SetController(ctrl *controller.Controller) {
	s.ctrl = ctrl
}

func (s *Server) SetPluginExecutor(exec *plugin.Executor) {
	s.pluginExec = exec
}

func (s *Server) SetConfig(cfg config.Config) {
	s.cfg = cfg
}

func (s *Server) SetTaskProvider(tp tasks.Provider) {
	s.taskProvider = tp
}

type RecentDirInfo struct {
	Path    string `json:"path"`
	Display string `json:"display"`
	Age     string `json:"age"`
}

func (s *Server) SetRecentDirsFunc(fn func(int) []RecentDirInfo) {
	s.recentDirsFn = fn
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`) )
	})

	mux.HandleFunc("GET /api/events", s.handleSSE)

	mux.HandleFunc("POST /api/agents/launch", s.handleLaunch)
	mux.HandleFunc("POST /api/agents/{id}/annotate", s.handleAnnotateTurn)
	mux.HandleFunc("POST /api/agents/{id}/archive", s.handleArchive)
	mux.HandleFunc("POST /api/sessions/{id}/annotate", s.handleAnnotateTurn)
	mux.HandleFunc("GET /api/sessions/{id}/annotations", s.handleGetAnnotations)
	mux.HandleFunc("POST /api/sessions/meta", s.handleUpdateSessionMeta)
	mux.HandleFunc("GET /api/sessions/meta", s.handleGetSessionMeta)
	mux.HandleFunc("GET /api/agents/{id}/diff", s.handleDiff)
	mux.HandleFunc("GET /api/agents/{id}/trace", s.handleGetTrace)
	mux.HandleFunc("GET /api/trace", s.handleFastTrace)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("POST /api/trace/subscribe/{sessionId}", s.handleTraceSubscribe)
	mux.HandleFunc("POST /api/trace/unsubscribe/{sessionId}", s.handleTraceUnsubscribe)
	mux.HandleFunc("/api/terminal/{session}", s.handleTerminal)
	mux.HandleFunc("/api/terminal-resume/{id}", s.handleTerminalResume)
	mux.HandleFunc("POST /api/sessions/generate-titles", s.handleGenerateTitles)
	mux.HandleFunc("GET /api/sessions/diffs", s.handleSessionDiffs)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/config/roi", s.handleGetROIConfig)
	mux.HandleFunc("POST /api/sessions/{id}/export/jsonl", s.handleExportJSONL)
	mux.HandleFunc("POST /api/sessions/{id}/export/otel", s.handleExportOTEL)

	mux.HandleFunc("GET /api/plugins", s.handlePlugins)
	mux.HandleFunc("GET /api/plugins/{name}/data", s.handlePluginData)
	mux.HandleFunc("POST /api/insight", s.handleInsight)

	mux.HandleFunc("GET /api/costs", s.handleCosts)
	mux.HandleFunc("GET /api/agents", s.handleAgents)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /api/agents/{id}/kill", s.handleKill)

	mux.HandleFunc("GET /api/directories/browse", s.handleBrowseDir)
	mux.HandleFunc("GET /api/directories/recent", s.handleRecentDirs)

	mux.HandleFunc("GET /api/quick-launch", s.handleQuickLaunchDirs)

	mux.HandleFunc("GET /api/teams", s.handleTeams)
	mux.HandleFunc("GET /api/health/providers", s.handleProviderHealth)

	mux.HandleFunc("GET /api/tasks/lists", s.handleTaskLists)
	mux.HandleFunc("GET /api/tasks", s.handleTasks)
	mux.HandleFunc("POST /api/tasks/{id}/complete", s.handleTaskComplete)
	mux.HandleFunc("POST /api/tasks/{id}/reopen", s.handleTaskReopen)

	sub, err := fs.Sub(staticFiles, "dist")
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
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return s.srv.Serve(ln)
}

func (s *Server) Stop() {
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	}
}

func (s *Server) URL() string {
	if s.listener == nil {
		return ""
	}
	return fmt.Sprintf("http://%s", s.listener.Addr().String())
}
