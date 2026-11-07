package mcpproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/mark3labs/mcp-go/mcp"
)

// EditOptions configures editing a moved server's definition.
type EditOptions struct {
	Server       string            // server name
	ServersPath  string            // ~/.aimux/mcp-servers.json
	ManifestPath string            // ~/.aimux/mcp-proxy-install.json
	CatalogPath  string            // ~/.aimux/mcp-catalog.json
	BaseEnv      map[string]string // the proxy's PATH and HOME (its plist); the command is resolved on that PATH
	Out          io.Writer
	Port         int                     // daemon port
	Editor       func(path string) error // opens $EDITOR on path (injectable for tests)
	Probe        func(ctx context.Context, cfg ServerConfig) ([]json.RawMessage, error)
	Reload       func() (ReloadResult, error) // reloads the daemon (injectable for tests)
	Timeout      time.Duration                // probe timeout
}

// EditServer opens the server's definition in $EDITOR, validates the changes,
// probes with the new definition, saves it and the tools it listed, and
// reloads the daemon. Any failure before the save leaves the old definition
// unchanged. It holds the install lock, so it never races install or
// uninstall.
func EditServer(ctx context.Context, opts EditOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	unlock, err := lockInstall(opts.ManifestPath)
	if err != nil {
		return err
	}
	defer unlock()

	// Check that the server is moved
	m, err := LoadManifest(opts.ManifestPath)
	if err != nil {
		return err
	}
	if m == nil || m.Servers[opts.Server].Written == "" {
		return fmt.Errorf("server %q is not behind the proxy; run `aimux mcp proxy install` to move it", opts.Server)
	}

	// Load current definition
	servers, resolved, err := LoadServerFile(opts.ServersPath)
	if err != nil {
		return err
	}
	cfg, ok := servers[opts.Server]
	if !ok {
		return fmt.Errorf("server %q not found in %s; run `aimux mcp proxy install`", opts.Server, opts.ServersPath)
	}

	// From here until the temp copy (it holds env values) is removed,
	// SIGINT and SIGTERM never kill aimux: while the editor runs they belong
	// to it (the terminal signals the whole foreground group), afterwards
	// they cancel the run. stop is deferred before the removal, so the
	// removal runs first.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	var editing atomic.Bool
	var interrupted atomic.Bool
	go func() {
		for {
			select {
			case <-signals:
				if !editing.Load() {
					interrupted.Store(true)
					cancel()
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	temp, originalHash, err := writeTempDefinition(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp) }()

	editing.Store(true)
	editErr := opts.Editor(temp)
	editing.Store(false)
	if editErr != nil {
		return fmt.Errorf("editor failed: %w", editErr)
	}

	newCfg, newHash, err := readTempDefinition(temp)
	if err != nil {
		return err
	}
	if newHash == originalHash {
		_, _ = fmt.Fprintf(out, "Definition of %q unchanged; nothing saved.\n", opts.Server)
		return nil
	}
	path := os.Getenv("PATH")
	if p, ok := opts.BaseEnv["PATH"]; ok {
		path = p
	}
	if err := validateCommand(newCfg.Command, path); err != nil {
		return err
	}

	tools, err := opts.Probe(ctx, newCfg)
	if interrupted.Load() {
		return fmt.Errorf("interrupted while probing %q; nothing was saved", opts.Server)
	}
	if err != nil {
		return fmt.Errorf("probe failed with new definition: %w; nothing was saved", err)
	}

	// The definition and its probed tools (an empty list replaces the old
	// tools), under the catalog lock the daemon's refreshes take, so a
	// refresh of the old definition cannot land between the two writes.
	if tools == nil {
		tools = []json.RawMessage{}
	}
	unlockCatalog := func() {}
	if opts.CatalogPath != "" {
		if unlockCatalog, err = lockCatalog(opts.CatalogPath); err != nil {
			return err
		}
	}
	servers[opts.Server] = newCfg
	if err := SaveServerFile(opts.ServersPath, servers, resolved); err != nil {
		unlockCatalog()
		return fmt.Errorf("save servers file: %w", err)
	}
	if opts.CatalogPath != "" {
		cat, err := LoadCatalog(opts.CatalogPath)
		if err == nil {
			cat.Set(opts.Server, tools, time.Now())
			err = cat.Save(opts.CatalogPath)
		}
		if err != nil {
			unlockCatalog()
			return fmt.Errorf("the definition is saved, but saving its tools failed (%v); the tools refresh when the server next starts", err)
		}
	}
	unlockCatalog()
	if _, err := opts.Reload(); err != nil {
		return fmt.Errorf("the definition of %q is saved; takes effect when the proxy next starts (reload failed: %v)", opts.Server, err)
	}
	_, _ = fmt.Fprintf(out, "Saved the definition of %q; the proxy reloaded it (%d tools).\n", opts.Server, len(tools))
	return nil
}

// writeTempDefinition writes the server config to a temp file, returning the
// file path and a content hash. The full definition including env is written
// to the 0600 temp file.
func writeTempDefinition(cfg ServerConfig) (path string, hash string, err error) {
	// Create temp file
	f, err := os.CreateTemp("", "aimux-mcp-edit-*.json")
	if err != nil {
		return "", "", err
	}
	path = f.Name()
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	// Make it owner-only
	if err := f.Chmod(0600); err != nil {
		return "", "", err
	}

	// Write full definition including env (file is 0600, owner is editing it)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", "", err
	}

	if _, err := f.Write(data); err != nil {
		return "", "", err
	}

	// Compute hash for change detection
	h := sha256.Sum256(data)
	hash = fmt.Sprintf("%x", h)

	return path, hash, nil
}

// readTempDefinition reads and validates the edited definition, returning it
// and its content hash.
func readTempDefinition(path string) (cfg ServerConfig, hash string, err error) {
	data, err := os.ReadFile(path) // #nosec G304 -- temp file we just created
	if err != nil {
		return ServerConfig{}, "", err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return ServerConfig{}, "", fmt.Errorf("the edited definition is not valid (%v); it may hold only command, args and env; nothing was saved", err)
	}
	if dec.More() {
		return ServerConfig{}, "", errors.New("the edited definition has more than one JSON value; nothing was saved")
	}

	// Compute hash
	h := sha256.Sum256(data)
	hash = fmt.Sprintf("%x", h)

	return cfg, hash, nil
}

// validateCommand checks that cmd resolves to an executable file on path
// (the proxy's PATH), as the daemon will resolve it.
func validateCommand(cmd, path string) error {
	if _, err := lookPathIn(cmd, path); err != nil {
		return fmt.Errorf("cannot resolve the command: %w; nothing was saved", err)
	}
	return nil
}

// ProbeServerDirect starts the server once with cfg and lists its tools,
// as the daemon would run it: its environment is base (PATH and HOME as the
// launchd plist sets them) plus cfg.Env, nothing else inherited from this
// shell. A server that starts and initializes is accepted with no tools
// when its initialize result has no tools capability or it answers
// tools/list with "method not found" (-32601): it returns an empty list.
// Any other listing failure (an auth error, a malformed answer) is an
// error. It returns the tools it listed.
func ProbeServerDirect(ctx context.Context, cfg ServerConfig, timeout time.Duration, base map[string]string) ([]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := make([]string, 0, len(base))
	for k, v := range base {
		env = append(env, k+"="+v)
	}
	b, err := startStdioIn(ctx, "probe", cfg, false, env)
	if err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}
	defer func() { _ = b.Close() }()
	if b.noTools {
		return []json.RawMessage{}, nil
	}
	tools, err := b.ListToolsRaw(ctx)
	var answered *RequestError
	if errors.As(err, &answered) && answered.Code == mcp.METHOD_NOT_FOUND {
		return []json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	if tools == nil {
		tools = []json.RawMessage{}
	}
	return tools, nil
}

// DefaultEditor returns a function that opens $EDITOR on the given path,
// falling back to vi if $EDITOR is not set. $EDITOR may carry arguments
// (`code --wait`, `vim -n`), split as a shell would (editorCommand).
func DefaultEditor() func(string) error {
	return func(path string) error {
		argv, err := editorCommand(os.Getenv("EDITOR"), path)
		if err != nil {
			return err
		}
		cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204,G702 -- $EDITOR is user-controlled
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
}

// editorCommand is the command line that opens path in editor (an $EDITOR
// value, vi when empty): editor split into words as a shell would (single
// and double quotes, backslash escapes; no variables or globs), then path
// as the last argument.
func editorCommand(editor, path string) ([]string, error) {
	words, err := splitWords(editor)
	if err != nil {
		return nil, fmt.Errorf("$EDITOR %s", err)
	}
	if len(words) == 0 {
		words = []string{"vi"}
	}
	return append(words, path), nil
}

// splitWords splits s into words the way a POSIX shell does for quotes and
// backslashes: inside single quotes everything is literal; inside double
// quotes a backslash escapes only " and \.
func splitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\'):
				i++
				cur.WriteRune(rs[i])
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			cur.WriteRune(rs[i])
			inWord = true
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("has an unterminated %c quote", quote)
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
