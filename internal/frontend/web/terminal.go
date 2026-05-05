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
type cachedPTY struct {
	ptmx     *os.File
	cmd      *exec.Cmd
	lastUsed time.Time
	mu       sync.Mutex
}

var (
	ptyCache   = make(map[string]*cachedPTY) // sessionID -> cached PTY
	ptyCacheMu sync.Mutex
)

func getCachedPTY(sessionID string) *cachedPTY {
	ptyCacheMu.Lock()
	defer ptyCacheMu.Unlock()
	if c, ok := ptyCache[sessionID]; ok {
		c.mu.Lock()
		c.lastUsed = time.Now()
		c.mu.Unlock()
		return c
	}
	return nil
}

func putCachedPTY(sessionID string, c *cachedPTY) {
	ptyCacheMu.Lock()
	defer ptyCacheMu.Unlock()
	ptyCache[sessionID] = c
}

func removeCachedPTY(sessionID string) {
	ptyCacheMu.Lock()
	defer ptyCacheMu.Unlock()
	delete(ptyCache, sessionID)
}

func init() {
	go func() {
		for {
			time.Sleep(60 * time.Second)
			ptyCacheMu.Lock()
			for id, c := range ptyCache {
				c.mu.Lock()
				if time.Since(c.lastUsed) > 5*time.Minute {
					c.ptmx.Close()
					if c.cmd.Process != nil {
						c.cmd.Process.Kill()
					}
					delete(ptyCache, id)
				}
				c.mu.Unlock()
			}
			ptyCacheMu.Unlock()
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

	// Try to reattach to a cached PTY for this session
	if cached := getCachedPTY(sessionID); cached != nil {
		servePTYCached(conn, cached)
		return
	}

	// No cache: discover agent and spawn claude --resume
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

	cached := &cachedPTY{ptmx: ptmx, cmd: cmd, lastUsed: time.Now()}
	putCachedPTY(sessionID, cached)

	servePTYCached(conn, cached)
}

// servePTYCached bridges a WebSocket to an existing cached PTY.
// When the WebSocket disconnects, the PTY stays alive for reuse.
func servePTYCached(conn *websocket.Conn, cached *cachedPTY) {
	var wg sync.WaitGroup

	// PTY -> WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := cached.ptmx.Read(buf)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// WebSocket -> PTY (with resize handling)
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
				cached.mu.Lock()
				pty.Setsize(cached.ptmx, &pty.Winsize{Cols: rm.Cols, Rows: rm.Rows})
				cached.mu.Unlock()
				continue
			}

			if _, err := cached.ptmx.Write(msg); err != nil {
				return
			}
		}
	}()

	wg.Wait()
	// Don't close the PTY -- it stays cached for reconnection
	cached.mu.Lock()
	cached.lastUsed = time.Now()
	cached.mu.Unlock()
}

// servePTYFresh bridges a WebSocket to a fresh PTY. When the WebSocket
// disconnects, the PTY is killed (used for tmux attach).
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
