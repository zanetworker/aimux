package provider

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/discovery"
)

func providerNames(ps []Provider) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return strings.Join(out, ",")
}

func k8sActive() config.Config {
	cfg := config.Config{Providers: map[string]config.ProviderConfig{}}
	cfg.Kubernetes.Enabled = true
	cfg.Kubernetes.RedisURL = "redis://localhost:6379"
	return cfg
}

func TestRegistry_LocalIsClaudeThenCodexWhateverTheConfig(t *testing.T) {
	cfg := k8sActive()
	cfg.Providers["claude"] = config.ProviderConfig{Enabled: false}
	if got := providerNames(NewRegistry("", cfg).Local()); got != "claude,codex" {
		t.Fatalf("Local() = %s, want claude,codex (enabled flags and k8s do not apply)", got)
	}
}

func TestRegistry_EnabledOrderAndFlags(t *testing.T) {
	disabledClaude := k8sActive()
	disabledClaude.Providers["claude"] = config.ProviderConfig{Enabled: false}
	noRedis := config.Config{}
	noRedis.Kubernetes.Enabled = true
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"empty config enables every local provider", config.Config{}, "claude,codex"},
		{"k8s active joins last", k8sActive(), "claude,codex,k8s"},
		{"disabled claude dropped, order kept", disabledClaude, "codex,k8s"},
		{"k8s enabled without redis is not active", noRedis, "claude,codex"},
		{"all disabled", config.Config{Providers: map[string]config.ProviderConfig{
			"claude": {Enabled: false}, "codex": {Enabled: false}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerNames(NewRegistry("", tt.cfg).Enabled()); got != tt.want {
				t.Fatalf("Enabled() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegistry_ByName(t *testing.T) {
	r := NewRegistry("", k8sActive())
	for _, n := range []string{"claude", "codex", "k8s"} {
		if p := r.ByName(n); p == nil || p.Name() != n {
			t.Fatalf("ByName(%q) = %v", n, p)
		}
	}
	if p := r.ByName("gemini"); p != nil {
		t.Fatalf("ByName(gemini) = %v, want nil", p)
	}
	if p := r.ByName(""); p != nil {
		t.Fatalf("ByName(\"\") = %v, want nil", p)
	}
	if p := NewRegistry("", config.Config{}).ByName("k8s"); p != nil {
		t.Fatalf("ByName(k8s) with k8s inactive = %v, want nil", p)
	}
	// one instance per provider: the lists and the lookup agree
	if r.ByName("claude") != r.Local()[0] || r.ByName("k8s") != r.Enabled()[2] {
		t.Fatal("ByName, Local and Enabled return different instances")
	}
	// a disabled provider is still found (the web trace lookup falls back to Claude)
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"claude": {Enabled: false}}}
	if NewRegistry("", cfg).ByName("claude") == nil {
		t.Fatal("ByName(claude) nil when claude is disabled")
	}
}

func TestRegistry_ClaudePathsComeFromHome(t *testing.T) {
	home := t.TempDir()
	c := NewRegistry(home, config.Config{}).ByName("claude")
	tr, ok := c.(TranscriptRoot)
	if !ok {
		t.Fatal("claude does not implement TranscriptRoot")
	}
	if got, want := tr.TranscriptsDir(), filepath.Join(home, ".claude", "projects"); got != want {
		t.Fatalf("TranscriptsDir = %q, want %q", got, want)
	}
	ls, ok := c.(LiveSource)
	if !ok {
		t.Fatal("claude does not implement LiveSource")
	}
	live, ok := ls.LiveSessions(time.Second).(*discovery.LiveCache)
	if !ok || live.Dir != filepath.Join(home, ".claude", "sessions") || live.TTL != time.Second {
		t.Fatalf("LiveSessions = %#v, want the registry under %s, ttl 1s", live, home)
	}
	// no home: the defaults under the user's home, as an empty Claude has
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	c = NewRegistry("", config.Config{}).ByName("claude")
	if got := c.(TranscriptRoot).TranscriptsDir(); got != filepath.Join(userHome, ".claude", "projects") {
		t.Fatalf("default TranscriptsDir = %q", got)
	}
	if got := c.(LiveSource).LiveSessions(0).(*discovery.LiveCache).Dir; got != filepath.Join(userHome, ".claude", "sessions") {
		t.Fatalf("default registry dir = %q", got)
	}
}

// Providers are built in one place, the registry: no package outside
// internal/provider writes a concrete provider's struct literal or calls
// its New constructor (tests excepted).
func TestNoProviderConstructionOutsideRegistry(t *testing.T) {
	const pkgPath = "github.com/zanetworker/aimux/internal/provider"
	concrete := concreteProviders(t)
	if !concrete["Claude"] || !concrete["Codex"] || !concrete["K8s"] {
		t.Fatalf("concrete providers %v, want Claude, Codex, K8s among them", concrete)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "docs-site":
				return filepath.SkipDir
			}
			if path == filepath.Join(root, "internal", "provider") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		local := ""
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == pkgPath {
				local = "provider"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		if local == "" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			var sel *ast.SelectorExpr
			var typeName string
			switch x := n.(type) {
			case *ast.CompositeLit:
				if s, ok := x.Type.(*ast.SelectorExpr); ok {
					sel, typeName = s, s.Sel.Name
				}
			case *ast.CallExpr:
				if s, ok := x.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(s.Sel.Name, "New") {
					sel, typeName = s, strings.TrimPrefix(s.Sel.Name, "New")
				}
			}
			if sel == nil || !concrete[typeName] {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
				bad = append(bad, rel+": "+local+"."+sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("providers built outside the registry (use provider.NewRegistry):\n%s", strings.Join(bad, "\n"))
	}
}

// concreteProviders is every type in this package with a Discover method.
func concreteProviders(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "Discover" || len(fn.Recv.List) != 1 {
				continue
			}
			typ := fn.Recv.List[0].Type
			if st, ok := typ.(*ast.StarExpr); ok {
				typ = st.X
			}
			if id, ok := typ.(*ast.Ident); ok {
				out[id.Name] = true
			}
		}
	}
	return out
}
