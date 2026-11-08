package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ProbeHeader must be set (to any value) on POST /status/probe. A web page
// can submit a form to the proxy without an Origin header, which LocalOnly
// lets through; a form cannot set this header, and a script that sets it
// needs a CORS preflight the proxy never allows.
const ProbeHeader = "X-Aimux-Probe"

const (
	defaultProbeTimeout = time.Minute
	maxProbeTimeout     = 5 * time.Minute
)

// StatusReport is the daemon's GET /status answer.
type StatusReport struct {
	Running   bool      `json:"running"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	Port      int       `json:"port"`
	// Version is the aimux version the daemon runs ("" from an older one).
	// Install and status compare it with the binary's: a difference is an
	// upgrade restart pending.
	Version string         `json:"version,omitempty"`
	Servers []ServerReport `json:"servers"`
}

// ServerReport is one server's health plus its tool count.
type ServerReport struct {
	ServerStatus
	Tools int `json:"tools"`
	// Registered is the ToolsFingerprint of the tools the daemon registered
	// for this server ("" from a daemon that does not report it).
	Registered string `json:"registered,omitempty"`
	// Endpoint: the daemon serves /mcp/<server> for it (a configured server
	// without a catalog entry has none).
	Endpoint bool `json:"endpoint"`
	// ProbeError is why a probe could not start the server or list its
	// tools; only set in a probe's answer.
	ProbeError string `json:"probeError,omitempty"`
}

// Monitor reports the proxy's health over HTTP (/status, /status/probe).
type Monitor struct {
	mu      sync.RWMutex // guards Servers and Registered, which a reload replaces
	Pool    *Pool
	Servers []string                // configured servers
	Tools   func(server string) int // tool count from the catalog; nil means 0
	// Registered is each server's ToolsFingerprint as currently served.
	Registered map[string]string
	PID        int
	StartedAt  time.Time
	Port       int
	Version    string // the aimux version this daemon runs
	// hasEndpoint reports whether the daemon serves an endpoint for a
	// server; Serve sets it from its endpoint map (under mu).
	hasEndpoint func(server string) bool
}

// setEndpoints tells the monitor how to ask the daemon's endpoint map.
func (m *Monitor) setEndpoints(has func(server string) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hasEndpoint = has
}

// setServers replaces the configured servers and registered fingerprints.
func (m *Monitor) setServers(servers []string, registered map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Servers, m.Registered = servers, registered
}

func (m *Monitor) current() ([]string, map[string]string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Servers, m.Registered
}

// Report is every configured server's current health.
func (m *Monitor) Report() StatusReport {
	configured, registered := m.current()
	m.mu.RLock()
	has := m.hasEndpoint
	m.mu.RUnlock()
	st := m.Pool.Status(configured)
	servers := make([]ServerReport, len(st))
	for i, s := range st {
		servers[i] = ServerReport{ServerStatus: s, Registered: registered[s.Server]}
		servers[i].Endpoint = has != nil && has(s.Server)
		if m.Tools != nil {
			servers[i].Tools = m.Tools(s.Server)
		}
	}
	return StatusReport{Running: true, PID: m.PID, StartedAt: m.StartedAt, Port: m.Port, Version: m.Version, Servers: servers}
}

// Probe starts each configured server once through the pool, lists its
// tools, and reports. Servers that cannot finish within ctx carry the
// error in ProbeError.
func (m *Monitor) Probe(ctx context.Context) StatusReport {
	return m.ProbeServers(ctx, nil)
}

// ProbeServers is Probe for the named servers only (all configured ones
// when names is empty). A name the proxy does not serve is reported with
// a ProbeError, so a caller checking those servers sees it fail.
func (m *Monitor) ProbeServers(ctx context.Context, names []string) StatusReport {
	servers, _ := m.current()
	targets := servers
	var unknown []string
	if len(names) > 0 {
		configured := map[string]bool{}
		for _, s := range servers {
			configured[s] = true
		}
		targets = nil
		for _, n := range names {
			if configured[n] {
				targets = append(targets, n)
			} else {
				unknown = append(unknown, n)
			}
		}
	}
	listed := &Catalog{}
	errs := RefreshCatalog(ctx, listed, m.Pool.Lease, targets)
	rep := m.Report()
	if len(names) > 0 {
		want := map[string]bool{}
		for _, t := range targets {
			want[t] = true
		}
		kept := rep.Servers[:0]
		for _, s := range rep.Servers {
			if want[s.Server] {
				kept = append(kept, s)
			}
		}
		rep.Servers = kept
		for _, n := range unknown {
			rep.Servers = append(rep.Servers, ServerReport{ServerStatus: ServerStatus{Server: n}, ProbeError: "not configured in the proxy"})
		}
	}
	for i := range rep.Servers {
		s := &rep.Servers[i]
		if err := errs[s.Server]; err != nil {
			s.ProbeError = firstLine(err.Error())
		} else if e, ok := listed.Servers[s.Server]; ok {
			s.Tools = len(e.Tools)
		}
	}
	return rep
}

// handle adds GET /status and POST /status/probe to mux. Probing starts
// servers, so it is POST only and needs ProbeHeader; repeated server=
// parameters limit it to those servers.
func (m *Monitor) handle(mux *http.ServeMux) {
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, m.Report())
	})
	mux.HandleFunc("POST /status/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(ProbeHeader) == "" {
			http.Error(w, "a probe needs the "+ProbeHeader+" header", http.StatusForbidden)
			return
		}
		timeout := defaultProbeTimeout
		if v := r.URL.Query().Get("timeout"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				http.Error(w, "timeout must be a positive duration such as 30s", http.StatusBadRequest)
				return
			}
			timeout = min(d, maxProbeTimeout)
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		writeJSON(w, m.ProbeServers(ctx, r.URL.Query()["server"]))
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v) // the client went away; nothing to do
}

// Ago is t relative to now: "never", "just now", "30s ago", "5m ago",
// "3h ago", "2d ago".
func Ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}
