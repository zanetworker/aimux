package mcpproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
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

// Only returns a copy holding the servers keep accepts; c is unchanged.
func (c *Catalog) Only(keep func(server string) bool) *Catalog {
	out := &Catalog{Servers: map[string]CatalogEntry{}}
	for s, e := range c.Servers {
		if keep(s) {
			out.Servers[s] = e
		}
	}
	return out
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

// ToolsFingerprint identifies a backend's tool list as the proxy registers
// it: the tools in order, each as compact JSON, hashed. The daemon reports
// it for what it loaded at start; install compares it with the catalog.
func ToolsFingerprint(tools []json.RawMessage) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\n", len(tools))
	for _, raw := range tools {
		var buf bytes.Buffer
		if json.Compact(&buf, raw) != nil {
			buf.Reset()
			buf.Write(raw)
		}
		_, _ = fmt.Fprintf(h, "%d\n", buf.Len())
		h.Write(buf.Bytes())
	}
	return hex.EncodeToString(h.Sum(nil))
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
	if err := tmp.Sync(); err != nil { // the data is on disk before the rename makes it current
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil { // and so is the rename
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
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
	// Current is a server's definition as a reload would load it now; a
	// refresh from a backend started with another definition is dropped.
	// Nil, or false (unknown), keeps every refresh.
	Current func(server string) (ServerConfig, bool)
}

// lockCatalog takes an exclusive flock beside the catalog file. The
// daemon's refreshes and edit (another process) hold it around their
// checks and writes.
func lockCatalog(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- beside aimux's own catalog
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { // #nosec G115 -- file descriptors are small
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path+".lock", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // #nosec G115 -- file descriptors are small
		_ = f.Close()
	}, nil
}

// NewCatalogUpdater keeps a copy of cat current and saves it to path. The
// copy is its own: callers (the reloader's current set) may go on reading
// cat while backend starts refresh the updater's catalog.
func NewCatalogUpdater(cat *Catalog, path string) *CatalogUpdater {
	return &CatalogUpdater{cat: copyCatalog(cat), path: path}
}

func copyCatalog(cat *Catalog) *Catalog {
	c := &Catalog{Servers: make(map[string]CatalogEntry, len(cat.Servers))}
	for k, v := range cat.Servers {
		c.Servers[k] = v
	}
	return c
}

// Refresh re-lists a started backend's tools and records them. The file is
// read, changed for this server only and saved, so entries other programs
// wrote since (install's refresh, edit) survive; the served copy changes
// for this server only, a reload replaces the rest. A failed listing keeps
// the previous entry and writes nothing.
func (u *CatalogUpdater) Refresh(ctx context.Context, server string, b Backend) error {
	tools, err := b.ListToolsRaw(ctx)
	if err != nil {
		return fmt.Errorf("refresh catalog for %s: %w", server, err)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	unlock, err := lockCatalog(u.path)
	if err != nil {
		return fmt.Errorf("refresh catalog for %s: %w", server, err)
	}
	defer unlock()
	if u.superseded(server, b) {
		return nil // an edit or reload replaced the definition it ran
	}
	now := time.Now()
	u.cat.Set(server, tools, now)
	onDisk, err := LoadCatalog(u.path)
	if err != nil {
		return fmt.Errorf("refresh catalog for %s: %w", server, err)
	}
	onDisk.Set(server, tools, now)
	return onDisk.Save(u.path)
}

// superseded reports whether b was started with a definition of server
// other than the current one.
func (u *CatalogUpdater) superseded(server string, b Backend) bool {
	d, ok := b.(interface{ Definition() ServerConfig })
	if !ok || u.Current == nil {
		return false
	}
	cur, known := u.Current(server)
	return known && !reflect.DeepEqual(cur, d.Definition())
}

// Replace swaps in a copy of cat, e.g. after a reload re-read the file.
func (u *CatalogUpdater) Replace(cat *Catalog) {
	c := copyCatalog(cat)
	u.mu.Lock()
	u.cat = c
	u.mu.Unlock()
}

// Snapshot returns a copy of the current catalog.
func (u *CatalogUpdater) Snapshot() *Catalog {
	u.mu.Lock()
	defer u.mu.Unlock()
	return copyCatalog(u.cat)
}

// ToolCount is how many tools the catalog lists for server.
func (u *CatalogUpdater) ToolCount(server string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.cat.Servers[server].Tools)
}
