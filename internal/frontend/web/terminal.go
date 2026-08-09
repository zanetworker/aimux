package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/terminal"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type resizeMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	sessionName := r.PathValue("session")
	if sessionName == "" {
		http.Error(w, "missing session name", http.StatusBadRequest)
		return
	}

	if err := exec.Command("tmux", "has-session", "-t", sessionName).Run(); err != nil { // #nosec G204 #nosec G702
		http.Error(w, fmt.Sprintf("tmux session %q not found", sessionName), http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	cmd := exec.Command("tmux", "attach-session", "-t", sessionName) // #nosec G204 #nosec G702
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	servePTY(conn, cmd, true)
}

func (s *Server) handleTerminalResume(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
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

	var workingDir, providerName, location string
	for _, a := range agents {
		if a.SessionID == sessionID || fmt.Sprintf("%d", a.PID) == sessionID {
			workingDir = a.WorkingDir
			providerName = a.ProviderName
			location = a.Location
			break
		}
	}

	// Fall back to query params for history sessions not in running agents
	if providerName == "" {
		providerName = r.URL.Query().Get("provider")
	}
	if workingDir == "" {
		workingDir = r.URL.Query().Get("dir")
	}

	if providerName == "" {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	skipPerms := r.URL.Query().Get("skipPermissions") == "true"

	var cmd *exec.Cmd
	if location == "remote" {
		cmdStr := controller.RemoteAgentCommand(providerName, sessionID, true)
		parts := strings.Fields(cmdStr)
		bin, _ := exec.LookPath(parts[0])
		if bin == "" {
			http.Error(w, fmt.Sprintf("%s binary not found", parts[0]), http.StatusInternalServerError)
			return
		}
		cmd = exec.Command(bin, parts[1:]...) // #nosec G204 G702
	} else {
		switch providerName {
		case "claude":
			bin, _ := exec.LookPath("claude")
			if bin == "" {
				http.Error(w, "claude binary not found", http.StatusInternalServerError)
				return
			}
			args := []string{"--resume", sessionID}
			if skipPerms {
				args = append(args, "--dangerously-skip-permissions")
			}
			cmd = exec.Command(bin, args...) // #nosec G204 #nosec G702
		case "codex":
			bin, _ := exec.LookPath("codex")
			if bin == "" {
				http.Error(w, "codex binary not found", http.StatusInternalServerError)
				return
			}
			args := []string{"resume", "--no-alt-screen", sessionID}
			if skipPerms {
				args = append(args, "--full-auto")
			}
			cmd = exec.Command(bin, args...) // #nosec G204 #nosec G702
		case "gemini":
			bin, _ := exec.LookPath("gemini")
			if bin == "" {
				http.Error(w, "gemini binary not found", http.StatusInternalServerError)
				return
			}
			cmd = exec.Command(bin, "--resume", "latest") // #nosec G204
		default:
			http.Error(w, fmt.Sprintf("resume not supported for provider %q", providerName), http.StatusBadRequest)
			return
		}
	}

	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	servePTY(conn, cmd, false)
}

func servePTY(conn *websocket.Conn, cmd *exec.Cmd, killOnClose bool) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v", err)))
		return
	}

	// Cleanup: close PTY and WebSocket to unblock all goroutines.
	// Only kill the process for tmux attach (killOnClose=true) where the
	// process is just a viewer. For resume sessions the PTY close sends
	// SIGHUP so the agent can save state and exit gracefully.
	cleanup := sync.OnceFunc(func() {
		if killOnClose && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = ptmx.Close()
		_ = conn.Close()
	})
	defer cleanup()

	// WebSocket keepalive: ping every 30s, close if no pong within 10s
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	done := make(chan struct{})

	// Ping ticker
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup

	// PTY -> WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				cleanup()
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				cleanup()
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
				cleanup()
				return
			}

			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			var rm resizeMsg
			if json.Unmarshal(msg, &rm) == nil && rm.Type == "resize" && rm.Cols > 0 && rm.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: rm.Cols, Rows: rm.Rows})
				continue
			}

			if _, err := ptmx.Write(msg); err != nil {
				cleanup()
				return
			}
		}
	}()

	wg.Wait()
	close(done)
}

func parseTermSize(r *http.Request) (cols, rows int) {
	cols, rows = 120, 40
	if v := r.URL.Query().Get("cols"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cols = n
		}
	}
	if v := r.URL.Query().Get("rows"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			rows = n
		}
	}
	return cols, rows
}

func (s *Server) handleTerminalSandbox(w http.ResponseWriter, r *http.Request) {
	sandboxName := r.PathValue("sandbox")
	if sandboxName == "" {
		http.Error(w, "missing sandbox name", http.StatusBadRequest)
		return
	}

	cols, rows := parseTermSize(r)

	backend, err := terminal.NewOpenShellExec(sandboxName, "", false, cols, rows)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = backend.Close()
		return
	}

	servePTYBackend(conn, backend)
}

func servePTYBackend(conn *websocket.Conn, backend terminal.SessionBackend) {
	cleanup := sync.OnceFunc(func() {
		_ = backend.Close()
		_ = conn.Close()
	})
	defer cleanup()

	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	done := make(chan struct{})

	// Ping ticker
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()

	var wg sync.WaitGroup

	// Backend -> WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := backend.Read(buf)
			if err != nil {
				cleanup()
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				cleanup()
				return
			}
		}
	}()

	// WebSocket -> Backend (with resize handling)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cleanup()
				return
			}

			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			var rm resizeMsg
			if json.Unmarshal(msg, &rm) == nil && rm.Type == "resize" && rm.Cols > 0 && rm.Rows > 0 {
				_ = backend.Resize(int(rm.Cols), int(rm.Rows))
				continue
			}

			if _, err := backend.Write(msg); err != nil {
				cleanup()
				return
			}
		}
	}()

	wg.Wait()
	close(done)
}
