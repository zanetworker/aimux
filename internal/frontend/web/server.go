package web

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/trace"
)

type Server struct {
	port         int
	listener     net.Listener
	srv          *http.Server
	discoverFn   func() ([]agent.Agent, error)
	launchFn     func(provider, dir, model, mode string) error
	annotateFn   func(sessionID string, turn int, label, note string) error
	providerLookupFn func(providerName string) interface{ ParseTrace(string) ([]trace.Turn, error) }
	killFn           func(pid int, tmuxSession string) error
}

func NewServer(port int) *Server {
	return &Server{port: port}
}

func (s *Server) SetDiscoverFunc(fn func() ([]agent.Agent, error)) {
	s.discoverFn = fn
}

func (s *Server) SetLaunchFunc(fn func(provider, dir, model, mode string) error) {
	s.launchFn = fn
}

func (s *Server) SetAnnotateFunc(fn func(sessionID string, turn int, label, note string) error) {
	s.annotateFn = fn
}

func (s *Server) SetProviderLookup(fn func(string) interface{ ParseTrace(string) ([]trace.Turn, error) }) {
	s.providerLookupFn = fn
}

func (s *Server) SetKillFunc(fn func(pid int, tmuxSession string) error) {
	s.killFn = fn
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /api/events", s.handleSSE)

	mux.HandleFunc("POST /api/agents/launch", s.handleLaunch)
	mux.HandleFunc("POST /api/agents/{id}/annotate", s.handleAnnotate)
	mux.HandleFunc("POST /api/agents/{id}/archive", s.handleArchive)
	mux.HandleFunc("GET /api/agents/{id}/diff", s.handleDiff)
	mux.HandleFunc("GET /api/agents/{id}/trace", s.handleGetTrace)
	mux.HandleFunc("GET /api/trace", s.handleFastTrace)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("POST /api/trace/subscribe/{sessionId}", s.handleTraceSubscribe)
	mux.HandleFunc("POST /api/trace/unsubscribe/{sessionId}", s.handleTraceUnsubscribe)
	mux.HandleFunc("/api/terminal/{session}", s.handleTerminal)
	mux.HandleFunc("GET /api/search", s.handleSearch)

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
