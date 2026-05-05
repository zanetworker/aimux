package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type resizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// cachedPTY holds a PTY process that outlives individual WebSocket connections.
// A single reader goroutine forwards PTY output to the current WebSocket.
type cachedPTY struct {
	ptmx     *os.File
	cmd      *exec.Cmd
	lastUsed time.Time

	mu   sync.Mutex
	conn *websocket.Conn // current active WebSocket (nil = no viewer)
	done chan struct{}    // closed when the PTY process exits
}

var (
	ptyCache   = make(map[string]*cachedPTY)
	ptyCacheMu sync.Mutex
)

func getCachedPTY(sessionID string) *cachedPTY {
	ptyCacheMu.Lock()
	defer ptyCacheMu.Unlock()
	c, ok := ptyCache[sessionID]
	if !ok {
		return nil
	}
	select {
	case <-c.done:
		delete(ptyCache, sessionID)
		return nil
	default:
	}
	c.mu.Lock()
	c.lastUsed = time.Now()
	c.mu.Unlock()
	return c
}

func putCachedPTY(sessionID string, c *cachedPTY) {
	ptyCacheMu.Lock()
	defer ptyCacheMu.Unlock()
	ptyCache[sessionID] = c
}

func init() {
	go func() {
		for {
			time.Sleep(60 * time.Second)
			ptyCacheMu.Lock()
			for id, c := range ptyCache {
				c.mu.Lock()
				idle := time.Since(c.lastUsed) > 5*time.Minute
				c.mu.Unlock()
				if idle {
					c.ptmx.Close()
					if c.cmd.Process != nil {
						c.cmd.Process.Kill()
					}
					delete(ptyCache, id)
				}
			}
			ptyCacheMu.Unlock()
		}
	}()
}

// startPTYReader runs a single goroutine that reads from the PTY and
// forwards to whichever WebSocket is currently attached.
func startPTYReader(c *cachedPTY) {
	go func() {
		defer close(c.done)
		buf := make([]byte, 4096)
		for {
			n, err := c.ptmx.Read(buf)
			if err != nil {
				return
			}
			c.mu.Lock()
			ws := c.conn
			c.mu.Unlock()
			if ws != nil {
				if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					c.mu.Lock()
					if c.conn == ws {
						c.conn = nil
					}
					c.mu.Unlock()
				}
			}
		}
	}()
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	sessionName := r.PathValue("session")
	if sessionName == "" {
		http.Error(w, "missing session name", http.StatusBadRequest)
		return
	}

	if err := exec.Command("tmux", "has-session", "-t", sessionName).Run(); err != nil {
		http.Error(w, fmt.Sprintf("tmux session %q not found", sessionName), http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	cmd := exec.Command("tmux", "attach-session", "-t", sessionName)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	servePTYFresh(conn, cmd)
}

func (s *Server) handleTerminalResume(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Try to reattach to a cached PTY
	if cached := getCachedPTY(sessionID); cached != nil {
		cached.mu.Lock()
		cached.conn = conn
		cached.lastUsed = time.Now()
		cached.mu.Unlock()
		serveWebSocketInput(conn, cached)
		return
	}

	// No cache: discover agent and spawn
	if s.discoverFn == nil {
		conn.WriteMessage(websocket.TextMessage, []byte("not configured"))
		return
	}

	agents, err := s.discoverFn()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v", err)))
		return
	}

	var workingDir, providerName string
	for _, a := range agents {
		if a.SessionID == sessionID || fmt.Sprintf("%d", a.PID) == sessionID {
			workingDir = a.WorkingDir
			providerName = a.ProviderName
			break
		}
	}

	if providerName == "" {
		conn.WriteMessage(websocket.TextMessage, []byte("agent not found"))
		return
	}

	var cmd *exec.Cmd
	switch providerName {
	case "claude":
		bin, _ := exec.LookPath("claude")
		if bin == "" {
			conn.WriteMessage(websocket.TextMessage, []byte("claude binary not found"))
			return
		}
		cmd = exec.Command(bin, "--resume", sessionID)
	case "codex":
		bin, _ := exec.LookPath("codex")
		if bin == "" {
			conn.WriteMessage(websocket.TextMessage, []byte("codex binary not found"))
			return
		}
		cmd = exec.Command(bin, "--resume", sessionID)
	default:
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("resume not supported for %s", providerName)))
		return
	}

	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v", err)))
		return
	}

	cached := &cachedPTY{
		ptmx:     ptmx,
		cmd:      cmd,
		lastUsed: time.Now(),
		conn:     conn,
		done:     make(chan struct{}),
	}
	putCachedPTY(sessionID, cached)
	startPTYReader(cached)

	serveWebSocketInput(conn, cached)
}

// serveWebSocketInput reads from the WebSocket and writes to the cached PTY.
// Blocks until the WebSocket disconnects. Does NOT kill the PTY on exit.
func serveWebSocketInput(conn *websocket.Conn, cached *cachedPTY) {
	defer func() {
		cached.mu.Lock()
		if cached.conn == conn {
			cached.conn = nil
		}
		cached.lastUsed = time.Now()
		cached.mu.Unlock()
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var rm resizeMsg
		if json.Unmarshal(msg, &rm) == nil && rm.Type == "resize" && rm.Cols > 0 && rm.Rows > 0 {
			cached.mu.Lock()
			pty.Setsize(cached.ptmx, &pty.Winsize{Cols: rm.Cols, Rows: rm.Rows})
			cached.mu.Unlock()
			continue
		}

		if _, err := cached.ptmx.Write(msg); err != nil {
			return
		}
	}
}

// servePTYFresh bridges a WebSocket to a fresh PTY (tmux attach).
func servePTYFresh(conn *websocket.Conn, cmd *exec.Cmd) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v", err)))
		return
	}
	defer ptmx.Close()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var rm resizeMsg
			if json.Unmarshal(msg, &rm) == nil && rm.Type == "resize" && rm.Cols > 0 && rm.Rows > 0 {
				pty.Setsize(ptmx, &pty.Winsize{Cols: rm.Cols, Rows: rm.Rows})
				continue
			}

			if _, err := ptmx.Write(msg); err != nil {
				return
			}
		}
	}()

	wg.Wait()
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
