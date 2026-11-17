package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ControlSocketName is the gateway worker's control socket in ~/.aimux.
const ControlSocketName = "gateway.sock"

// ControlSocketPath is home's control socket: <home>/.aimux/gateway.sock.
func ControlSocketPath(home string) string {
	return filepath.Join(home, ".aimux", ControlSocketName)
}

// ControlHandler is what the control socket serves: remote serve puts,
// renews and deletes turn leases, and reads the gateway's listings and
// status.
type ControlHandler interface {
	PutLease(ctx context.Context, l Lease) error
	Renew(session string, until time.Time) error
	DeleteLease(session string)
	Observed() map[string]Observed
	Status() ControlStatus
}

// ControlStatus is the gateway worker at a glance.
type ControlStatus struct {
	Version   string    `json:"version"`
	Leases    int       `json:"leases"`
	Backends  []string  `json:"backends"`
	Down      []string  `json:"down"`
	StartedAt time.Time `json:"startedAt"`
}

// observedWire is Observed on the socket: an error travels as its text.
type observedWire struct {
	Tools        map[string]json.RawMessage `json:"tools,omitempty"`
	Err          string                     `json:"err,omitempty"`
	Endpoint     string                     `json:"endpoint,omitempty"`
	RelistFailed bool                       `json:"relistFailed,omitempty"`
}

// ListenControl listens on the control socket at path, mode 0600, making
// its directory (0700) when missing. A file there that is not a socket, or
// is not the current user's, is refused; a socket that answers belongs to
// another running gateway and is refused; one that does not answer is left
// over from a crash and replaced.
func ListenControl(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("control socket directory: %w", err)
	}
	// MkdirAll leaves an existing folder as it is: tighten it, so only this
	// user can reach the socket and its lock.
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil { //nolint:gosec // a folder needs x; 0700 is owner-only
		return nil, fmt.Errorf("control socket directory: %w", err)
	}
	// The lock serializes the probe, the removal of a stale socket and the
	// bind across starting gateways; it is held as long as the listener.
	lock, err := lockControl(path + ".lock")
	if err != nil {
		return nil, err
	}
	ln, err := listenLocked(path)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return &peerListener{Listener: ln, lock: lock}, nil
}

func listenLocked(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket; move it away", path)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
			return nil, fmt.Errorf("%s belongs to another user", path)
		}
		if c, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("another tool gateway is running (%s answers)", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove the stale control socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("control socket mode: %w", err)
	}
	return ln, nil
}

// peerListener closes every connection whose peer is not this user, and
// holds the control socket's lock until it is closed.
type peerListener struct {
	net.Listener
	lock *os.File
}

func (l *peerListener) Close() error {
	err := l.Listener.Close()
	_ = l.lock.Close()
	return err
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, err := peerUID(c)
		if err == nil && uid == os.Getuid() {
			return c, nil
		}
		_ = c.Close()
	}
}

// maxControlBody bounds a control request body.
const maxControlBody = 1 << 20

// ServeControl serves h on ln until ctx ends. Errors never carry a token.
func ServeControl(ctx context.Context, ln net.Listener, h ControlHandler) error {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/leases/{session}", func(w http.ResponseWriter, r *http.Request) {
		var l Lease
		if err := json.NewDecoder(io.LimitReader(r.Body, maxControlBody)).Decode(&l); err != nil {
			controlError(w, http.StatusBadRequest, "bad_request", "the lease is not valid JSON")
			return
		}
		if l.Session != r.PathValue("session") {
			controlError(w, http.StatusBadRequest, "bad_request", "the lease's session does not match the path")
			return
		}
		if err := h.PutLease(r.Context(), l); err != nil {
			controlError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/leases/{session}/renew", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Until time.Time `json:"until"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxControlBody)).Decode(&body); err != nil {
			controlError(w, http.StatusBadRequest, "bad_request", "the renew body is not valid JSON")
			return
		}
		switch err := h.Renew(r.PathValue("session"), body.Until); {
		case errors.Is(err, ErrUnknownLease):
			controlError(w, http.StatusNotFound, "unknown_lease", err.Error())
		case err != nil:
			controlError(w, http.StatusBadRequest, "bad_request", err.Error())
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("DELETE /v1/leases/{session}", func(w http.ResponseWriter, r *http.Request) {
		h.DeleteLease(r.PathValue("session"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/observed", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]observedWire{}
		for name, o := range h.Observed() {
			ow := observedWire{Tools: o.Tools, Endpoint: o.Endpoint, RelistFailed: o.RelistFailed}
			if o.Err != nil {
				ow.Err = o.Err.Error()
			}
			out[name] = ow
		}
		controlJSON(w, out)
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		controlJSON(w, h.Status())
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shut)
}

func controlJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("tool gateway control: write response: %v", err)
	}
}

func controlError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}
