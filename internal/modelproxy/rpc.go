package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type appServer struct {
	cmd  *exec.Cmd
	cwd  string
	done chan struct{}
	err  error // published by closing done

	writeMu sync.Mutex
	writer  *json.Encoder

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcMessage
	threads map[string]chan rpcMessage
}

func startAppServer(command string, args []string, stderr io.Writer) (*appServer, error) {
	cwd, err := os.MkdirTemp("", "aimux-codex-")
	if err != nil {
		return nil, fmt.Errorf("create Codex working directory: %w", err)
	}
	// Resolve a relative executable before changing the subprocess directory.
	path, err := exec.LookPath(command)
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err != nil {
		_ = os.RemoveAll(cwd)
		return nil, fmt.Errorf("find Codex command: %w", err)
	}
	cmd := exec.Command(path, args...) // #nosec G204 -- the owner-configured Codex command, args built here
	cmd.Dir, cmd.Stderr = cwd, stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(cwd)
		return nil, fmt.Errorf("open Codex stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = os.RemoveAll(cwd)
		return nil, fmt.Errorf("open Codex stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = os.RemoveAll(cwd)
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	p := &appServer{
		cmd: cmd, cwd: cwd, writer: json.NewEncoder(stdin), done: make(chan struct{}),
		pending: make(map[int]chan rpcMessage), threads: make(map[string]chan rpcMessage),
	}
	go p.read(stdout)
	return p, nil
}

func (p *appServer) read(stdout io.Reader) {
	decoder := json.NewDecoder(stdout)
	for {
		var msg rpcMessage
		if err := decoder.Decode(&msg); err != nil {
			// EOF also covers a child that closed its stdout without exiting.
			_ = p.cmd.Process.Kill()
			exitErr := p.cmd.Wait()
			_ = os.RemoveAll(p.cwd)
			if !errors.Is(err, io.EOF) {
				p.err = fmt.Errorf("read Codex app-server output: %w", err)
			} else if exitErr != nil {
				p.err = fmt.Errorf("%w: %w", errAppServerExited, exitErr)
			} else {
				p.err = errAppServerExited
			}
			close(p.done)
			return
		}
		p.route(msg)
	}
}

func (p *appServer) route(msg rpcMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if msg.Method == "" {
		var id int
		if json.Unmarshal(msg.ID, &id) == nil {
			if reply := p.pending[id]; reply != nil {
				delete(p.pending, id)
				reply <- msg
			}
		}
		return
	}
	switch msg.Method {
	case "item/agentMessage/delta", "thread/tokenUsage/updated", "item/tool/call", "turn/completed":
		var target struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(msg.Params, &target) != nil {
			return
		}
		if events := p.threads[target.ThreadID]; events != nil {
			select {
			case events <- msg:
			default:
				// A stalled HTTP reader must not block other turns on this process.
				delete(p.threads, target.ThreadID)
				close(events)
			}
		}
	}
}

func (p *appServer) subscribe(threadID string) chan rpcMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	events := make(chan rpcMessage, 512)
	p.threads[threadID] = events
	return events
}

func (p *appServer) send(ctx context.Context, id int, method string, params any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-p.done:
		return p.err
	default:
	}
	if err := p.writer.Encode(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id,omitempty"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}); err != nil {
		return fmt.Errorf("write %s to Codex app-server: %w", method, err)
	}
	return nil
}

func (p *appServer) call(ctx context.Context, method string, params, result any) error {
	reply := make(chan rpcMessage, 1)
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.pending[id] = reply
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()
	if err := p.send(ctx, id, method, params); err != nil {
		return err
	}
	var msg rpcMessage
	select {
	case msg = <-reply:
	case <-p.done:
		select {
		case msg = <-reply:
		default:
			return p.err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if msg.Error != nil {
		return fmt.Errorf("%s: Codex error %d: %s", method, msg.Error.Code, msg.Error.Message)
	}
	if result != nil {
		if err := json.Unmarshal(msg.Result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
	}
	return nil
}

func (p *appServer) close() {
	_ = p.cmd.Process.Kill()
	<-p.done
}

// errAppServerExited marks a turn that failed because the Codex process went away.
var errAppServerExited = errors.New("codex app-server exited")
