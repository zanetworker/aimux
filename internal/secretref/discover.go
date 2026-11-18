// Package secretref resolves secret references (Keychain, file, command,
// environment) on the trusted host and finds which secrets a server needs.
// It holds references and the resolver, never stored values.
package secretref

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ServerDef is the part of a server definition Discover reads.
type ServerDef struct {
	Command []string
	Cwd     string
	URL     string
	Env     map[string]string
	Headers map[string]string
}

// Finding is one variable a server appears to need. Kind is config_ref (a
// ${NAME} in the definition), dotenv_key (a name in cwd/.env or
// cwd/../.env), source_read (an environment read in the server's source) or
// note. Where says where it was seen. A Finding never holds a value.
type Finding struct {
	Name  string
	Where string
	Kind  string
	// FileRefOK says a dotenv_key can be used as a file reference: a plain
	// NAME=value line in a regular file you own with mode 0600 or stricter.
	FileRefOK bool
}

// maxScanFiles bounds the source scan under cwd.
const maxScanFiles = 2000

var (
	configRefRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-[^}]*)?\}`)
	dotenvRE    = regexp.MustCompile(`^\s*(export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	sourceREs   = []*regexp.Regexp{
		regexp.MustCompile(`os\.getenv\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`),
		regexp.MustCompile(`os\.environ\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`),
		regexp.MustCompile(`os\.environ\.get\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`),
		regexp.MustCompile(`os\.Getenv\(\s*"([A-Za-z_][A-Za-z0-9_]*)"`),
		regexp.MustCompile(`os\.LookupEnv\(\s*"([A-Za-z_][A-Za-z0-9_]*)"`),
		regexp.MustCompile(`process\.env\.([A-Za-z_][A-Za-z0-9_]*)`),
		regexp.MustCompile(`process\.env\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`),
	}
	sourceExts = map[string]bool{".py": true, ".go": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true}
	skipDirs   = map[string]bool{".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, "vendor": true, "dist": true}
)

// Discover lists the variables def appears to need, sorted by kind then
// name. It reads names only: a .env file is read up to each "=", and source
// files are matched for environment reads. A cwd that is not a readable
// directory is an error.
func Discover(def ServerDef) ([]Finding, error) {
	seen := map[string]bool{}
	var out []Finding
	add := func(f Finding) {
		k := f.Kind + ":" + f.Name
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, f)
	}
	scanRefs := func(where, s string) {
		for _, m := range configRefRE.FindAllStringSubmatch(s, -1) {
			add(Finding{Name: m[1], Where: where, Kind: "config_ref"})
		}
	}
	for _, a := range def.Command {
		scanRefs("command", a)
	}
	scanRefs("url", def.URL)
	for k, v := range def.Env {
		scanRefs("env "+k, v)
	}
	for k, v := range def.Headers {
		scanRefs("header "+k, v)
	}
	if def.Cwd != "" {
		cwd, err := resolveDir(def.Cwd)
		if err != nil {
			return nil, err
		}
		for _, p := range []string{filepath.Join(cwd, ".env"), filepath.Join(filepath.Dir(cwd), ".env")} {
			names, strict := dotenvNames(p)
			usable := strict && usableFile(p)
			for _, d := range names {
				add(Finding{Name: d.name, Where: p, Kind: "dotenv_key", FileRefOK: usable && !d.export})
			}
		}
		for _, f := range scanSource(cwd) {
			add(f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// resolveDir is dir made absolute with symlinks resolved, or an error when
// it is not a readable directory.
func resolveDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("--cwd %s: %w", dir, err)
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return "", fmt.Errorf("--cwd %s: %w", dir, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("--cwd %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("--cwd %s is not a directory", dir)
	}
	f, err := os.Open(abs) //nolint:gosec // G304: the directory the owner named, opened to prove it is readable
	if err != nil {
		return "", fmt.Errorf("--cwd %s: %w", dir, err)
	}
	_ = f.Close()
	return abs, nil
}

// usableFile reports whether path is a regular file the user owns with mode
// 0600 or stricter, as the file backend requires.
func usableFile(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return false
	}
	return ownedByCurrentUser(fi)
}

type dotenvName struct {
	name   string
	export bool
}

// dotenvNames is the names a dotenv file defines, and whether every line
// has the strict form the file backend accepts (NAME=value, a comment or
// blank, no export, at most 64 KiB); a missing or non-regular file is none.
// Values are never kept: each line is read only up to its name, and the
// rest is skipped in bounded reads.
func dotenvNames(path string) ([]dotenvName, bool) {
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	f, err := os.Open(path) //nolint:gosec // G304: a dotenv beside the server's own cwd; only names are read
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	var names []dotenvName
	strict, total := true, 0
	r := bufio.NewReaderSize(f, 4096)
	for {
		line, err := r.ReadSlice('\n')
		total += len(line)
		head := string(line)
		for errors.Is(err, bufio.ErrBufferFull) { // skip the rest of a long line
			line, err = r.ReadSlice('\n')
			total += len(line)
		}
		trimmed := strings.TrimSpace(head)
		switch m := dotenvRE.FindStringSubmatch(head); {
		case m != nil:
			names = append(names, dotenvName{name: m[2], export: m[1] != ""})
			if m[1] != "" {
				strict = false
			}
		case trimmed != "" && !strings.HasPrefix(trimmed, "#"):
			strict = false
		}
		if err != nil {
			return names, strict && total <= 64<<10
		}
	}
}

// scanSource matches environment reads in source files under root, at most
// maxScanFiles of them; a larger tree adds one note.
func scanSource(root string) []Finding {
	var out []Finding
	n := 0
	capped := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is skipped; the scan is best effort
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !sourceExts[filepath.Ext(p)] {
			return nil
		}
		if n >= maxScanFiles {
			capped = true
			return filepath.SkipAll
		}
		n++
		data, err := os.ReadFile(p) //nolint:gosec // G304: the server's own source tree, scanned for names
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		text := string(data)
		var nl []int // newline offsets, so a match's line is a binary search
		for i := 0; i < len(text); i++ {
			if text[i] == '\n' {
				nl = append(nl, i)
			}
		}
		for _, re := range sourceREs { // over the whole file: a call may span lines
			for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
				line := sort.SearchInts(nl, m[2]) + 1
				out = append(out, Finding{Name: text[m[2]:m[3]], Where: fmt.Sprintf("%s:%d", rel, line), Kind: "source_read"})
			}
		}
		return nil
	})
	if capped {
		out = append(out, Finding{Name: "", Where: root, Kind: "note"})
	}
	return out
}

// Suggest is a typed reference per variable name, as a YAML flow mapping
// for servers.yaml: a file reference when a usable dotenv defines it, else
// the field env.<NAME> of the Keychain item aimux/<server>.
func Suggest(server string, fs []Finding) map[string]string {
	out := map[string]string{}
	for _, f := range fs {
		if f.Kind == "dotenv_key" && f.FileRefOK {
			out[f.Name] = fmt.Sprintf("{file: {path: %s, key: %s}}", strconv.Quote(f.Where), strconv.Quote(f.Name))
		}
	}
	for _, f := range fs {
		if f.Name == "" || f.Kind == "note" {
			continue
		}
		if _, ok := out[f.Name]; !ok {
			out[f.Name] = fmt.Sprintf("{keychain: {item: %s, field: %s}}", strconv.Quote("aimux/"+server), strconv.Quote("env."+f.Name))
		}
	}
	return out
}
