package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	data   map[string]json.RawMessage
	expiry time.Time
}

type Executor struct {
	plugins map[string]Plugin
	mu      sync.Mutex
	cache   map[string]cacheEntry
}

func NewExecutor(plugins []Plugin) *Executor {
	m := make(map[string]Plugin, len(plugins))
	for _, p := range plugins {
		m[p.Name] = p
	}
	return &Executor{
		plugins: m,
		cache:   make(map[string]cacheEntry),
	}
}

func (e *Executor) Plugins() []Plugin {
	result := make([]Plugin, 0, len(e.plugins))
	for _, p := range e.plugins {
		result = append(result, p)
	}
	return result
}

func (e *Executor) Execute(name string) (map[string]json.RawMessage, error) {
	p, ok := e.plugins[name]
	if !ok {
		return nil, fmt.Errorf("unknown plugin: %s", name)
	}

	e.mu.Lock()
	if cached, ok := e.cache[name]; ok && time.Now().Before(cached.expiry) {
		e.mu.Unlock()
		return cached.data, nil
	}
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmdStr := expandHome(p.Command)
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr) // #nosec G204
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("plugin %s command failed: %w", name, err)
	}

	var data map[string]json.RawMessage
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("plugin %s invalid JSON: %w (output: %s)", name, err, truncateStr(string(out), 200))
	}

	ttl := p.CacheSecs
	if ttl <= 0 {
		ttl = 30
	}

	e.mu.Lock()
	e.cache[name] = cacheEntry{data: data, expiry: time.Now().Add(time.Duration(ttl) * time.Second)}
	e.mu.Unlock()

	return data, nil
}

func truncateStr(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
