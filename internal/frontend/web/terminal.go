package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"

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

	servePTY(conn, cmd)
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

	agents, err := s.discoverFn()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	var cmd *exec.Cmd
	switch providerName {
	case "claude":
		bin, _ := exec.LookPath("claude")
		if bin == "" {
			http.Error(w, "claude binary not found", http.StatusInternalServerError)
			return
		}
		cmd = exec.Command(bin, "--resume", sessionID)
	case "codex":
		bin, _ := exec.LookPath("codex")
		if bin == "" {
			http.Error(w, "codex binary not found", http.StatusInternalServerError)
			return
		}
		cmd = exec.Command(bin, "--resume", sessionID)
	default:
		http.Error(w, fmt.Sprintf("resume not supported for provider %q", providerName), http.StatusBadRequest)
		return
	}

	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	servePTY(conn, cmd)
}

func servePTY(conn *websocket.Conn, cmd *exec.Cmd) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v", err)))
		return
	}
	defer ptmx.Close()

	var wg sync.WaitGroup

	// PTY -> WebSocket
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
