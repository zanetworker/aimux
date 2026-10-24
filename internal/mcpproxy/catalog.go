package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// Catalog is the cached tools/list of every backend, so the proxy can list
// tools without starting the servers that provide them.
type Catalog struct {
	Servers map[string]CatalogEntry `json:"servers"`
}

// CatalogEntry holds one backend's tools exactly as the server sent them.
// Raw JSON is kept because mcp.Tool drops schema keywords it does not model,
// such as a top-level anyOf.
type CatalogEntry struct {
	Tools     []json.RawMessage `json:"tools"`
	FetchedAt time.Time         `json:"fetchedAt"`
}

// LoadCatalog reads a catalog file. A missing file is an empty catalog.
func LoadCatalog(path string) (*Catalog, error) {
	c := &Catalog{Servers: map[string]CatalogEntry{}}
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from aimux config
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read MCP catalog %s: %w", path, err)
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse MCP catalog %s: %w", path, err)
	}
	if c.Servers == nil {
		c.Servers = map[string]CatalogEntry{}
	}
	return c, nil
}

// Set replaces a backend's tools.
func (c *Catalog) Set(server string, tools []json.RawMessage, fetchedAt time.Time) {
	if c.Servers == nil {
		c.Servers = map[string]CatalogEntry{}
	}
	c.Servers[server] = CatalogEntry{Tools: tools, FetchedAt: fetchedAt}
}

// Save writes the catalog atomically with owner-only permissions.
func (c *Catalog) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode MCP catalog: %w", err)
	}
	if err := writePrivateFile(path, data); err != nil {
		return fmt.Errorf("write MCP catalog %s: %w", path, err)
	}
	return nil
}

// writePrivateFile replaces path atomically with an owner-only (0600) file.
func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// DecodeTool turns a cached tool into an mcp.Tool whose input and output
// schemas are the server's original JSON.
func DecodeTool(raw json.RawMessage) (mcp.Tool, error) {
	var tool mcp.Tool
	if err := json.Unmarshal(raw, &tool); err != nil {
		return mcp.Tool{}, fmt.Errorf("decode tool: %w", err)
	}
	if tool.Name == "" {
		return mcp.Tool{}, errors.New("decode tool: missing name")
	}
	var schemas struct {
		Input  json.RawMessage `json:"inputSchema"`
		Output json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(raw, &schemas); err != nil {
		return mcp.Tool{}, fmt.Errorf("decode tool %s schemas: %w", tool.Name, err)
	}
	if len(schemas.Input) > 0 {
		tool.InputSchema = mcp.ToolInputSchema{}
		tool.RawInputSchema = schemas.Input
	}
	if len(schemas.Output) > 0 {
		tool.OutputSchema = mcp.ToolOutputSchema{}
		tool.RawOutputSchema = schemas.Output
	}
	return tool, nil
}

// CatalogUpdater keeps a catalog and its file current as backends start, so
// tools a server added appear the next time the proxy starts.
type CatalogUpdater struct {
	mu   sync.Mutex
	cat  *Catalog
	path string
}

// NewCatalogUpdater updates cat and saves it to path.
func NewCatalogUpdater(cat *Catalog, path string) *CatalogUpdater {
	return &CatalogUpdater{cat: cat, path: path}
}

// Refresh re-lists a started backend's tools and saves the catalog. A failed
// listing keeps the previous entry and writes nothing.
func (u *CatalogUpdater) Refresh(ctx context.Context, server string, b Backend) error {
	tools, err := b.ListToolsRaw(ctx)
	if err != nil {
		return fmt.Errorf("refresh catalog for %s: %w", server, err)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cat.Set(server, tools, time.Now())
	return u.cat.Save(u.path)
}

// Snapshot returns a copy of the current catalog.
func (u *CatalogUpdater) Snapshot() *Catalog {
	u.mu.Lock()
	defer u.mu.Unlock()
	c := &Catalog{Servers: make(map[string]CatalogEntry, len(u.cat.Servers))}
	for k, v := range u.cat.Servers {
		c.Servers[k] = v
	}
	return c
}
