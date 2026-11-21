package toolreg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ManagedHeldReason is why a set naming a managed server (command and url)
// is held: the gateway reaches it by url only, so it cannot tell whether
// the process answering is the one approved. The supervisor lifts this
// once it can tie a listing to the process it started.
const ManagedHeldReason = "managed server: approval waits until aimux runs it and can tie the listing to the process it started"

// FloatingReason is why a set's server is refused when its command fetches
// whatever version is current at each start.
const FloatingReason = "floating command; pin a version or a built binary"

// HashFile is "sha256:<hex>" over the regular executable file at path
// (links followed). It opens without blocking, so a FIFO is refused, not
// waited on.
func HashFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: a server's own executable
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return "", errors.New("not an executable file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Identity is "id:<sha256 hex>" over what makes the server the one that was
// approved: transport, normalized url, command and args, cwd, the sorted
// secret names (never where they come from or their values) and, for a
// server aimux starts, hashFile of the executable. A bare program name is
// looked up on the server's own env PATH only, so the CLI that approves and
// the gateway that serves find the same file; with no env PATH it is an
// error. Approvals are bound to it, so changing any of these needs a new
// approval.
func (s Server) Identity(hashFile func(path string) (string, error)) (string, error) {
	in := struct {
		Transport string   `json:"transport"`
		URL       string   `json:"url,omitempty"`
		Command   []string `json:"command,omitempty"`
		Cwd       string   `json:"cwd,omitempty"`
		Secrets   []string `json:"secrets,omitempty"`
		Exe       string   `json:"exe,omitempty"`
	}{Transport: s.Transport(), URL: normalURL(s.URL), Command: s.Command, Cwd: s.Cwd, Secrets: s.Secrets.Names()}
	if len(s.Command) > 0 {
		exe, err := s.executable()
		if err != nil {
			return "", err
		}
		if in.Exe, err = hashFile(exe); err != nil {
			return "", fmt.Errorf("hash %s: %w", exe, err)
		}
	}
	canon, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return "id:" + hex.EncodeToString(sum[:]), nil
}

// normalURL is u with its scheme and host lower-cased, except an IPv6
// zone, which is case-sensitive; u as given when it does not parse.
func normalURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || u == "" {
		return u
	}
	p.Scheme = strings.ToLower(p.Scheme)
	host, zone, hasZone := strings.Cut(p.Host, "%")
	p.Host = strings.ToLower(host)
	if hasZone {
		p.Host += "%" + zone
	}
	return p.String()
}

// executable is the file Command[0] runs: a path with a slash taken as is
// (relative to Cwd, which must then be absolute), a bare name searched on the server's env PATH,
// skipping relative entries as exec does.
func (s Server) executable() (string, error) {
	prog := s.Command[0]
	if strings.Contains(prog, "/") {
		if !filepath.IsAbs(prog) {
			if !filepath.IsAbs(s.Cwd) {
				return "", fmt.Errorf("program %q is relative; set an absolute cwd for the server", prog)
			}
			prog = s.Cwd + string(filepath.Separator) + prog // not Join: cleaning would undo a symlink before ".."
		}
		return prog, nil
	}
	path, ok := s.Env["PATH"]
	if !ok {
		return "", fmt.Errorf("program %q is a bare name; give its absolute path or set env PATH for the server", prog)
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, prog)
		// As exec.LookPath: a regular file this user may execute.
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && unix.Access(p, unix.X_OK) == nil { //nolint:gosec // G703: a PATH entry of the user's own server
			return p, nil
		}
	}
	return "", fmt.Errorf("program %q is not on the server's PATH", prog)
}

// Fixed versions per launcher: a whole semver (npm), a whole module
// version or pseudo-version (go), a release with no wildcard, range or
// list (Python).
var (
	npmVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	goVersion  = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	pyVersion  = regexp.MustCompile(`^(\d+!)?\d+(\.\d+)*((a|b|rc)\d+)?(\.post\d+)?(\.dev\d+)?(\+[A-Za-z0-9]+(\.[A-Za-z0-9]+)*)?$`)
)

// goValueFlags are the go run flags that take the next argument as value.
var goValueFlags = flagSet("-C", "-asmflags", "-buildmode", "-compiler", "-coverpkg", "-covermode", "-exec",
	"-gccgoflags", "-gcflags", "-installsuffix", "-ldflags", "-mod", "-modfile", "-o", "-overlay", "-p", "-pgo", "-pkgdir", "-tags", "-toolexec")

// npxValueFlags are the npx options that take a value and name no
// package.
var npxValueFlags = flagSet("--cache", "--userconfig", "--registry", "--prefix", "--workspace", "-w", "--node-options", "--loglevel", "--script-shell")

// npmPackage is a registry package name, optionally scoped: not a git
// URL, a GitHub shorthand (user/repo) or a path, whose "@x.y.z" suffix
// would be part of a ref, not a version.
var npmPackage = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)

// uvxValueFlags are the uvx flags that take a value and name no package
// (--from and --with name packages and are checked).
var uvxValueFlags = flagSet("--python", "-p", "--index", "--index-url", "-i", "--extra-index-url", "--default-index", "--find-links", "-f",
	"--constraints", "-c", "--overrides", "--cache-dir", "--directory", "--project", "--config-file", "--env-file", "--python-platform",
	"--refresh-package", "--reinstall-package", "--upgrade-package", "-P", "--index-strategy", "--keyring-provider", "--resolution",
	"--prerelease", "--exclude-newer", "--link-mode", "--color")

func flagSet(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Floating reports whether argv fetches a version chosen at start time:
// go run of anything but module@version, npx or pnpm dlx of any package
// without a fixed @version, uvx of any package (the tool, --from, --with)
// without ==version or @version. Unknown options are read as switches, so
// a value they take is checked as a package: an unknown launcher option
// can only make a command look floating, never pinned.
func Floating(argv []string) (bool, string) {
	if len(argv) == 0 {
		return false, ""
	}
	var pkgs []string
	pinned := func(p string) bool {
		i := strings.LastIndex(p, "@")
		return i > 0 && npmPackage.MatchString(p[:i]) && fixedAt(p, npmVersion)
	}
	args := argv[1:]
	switch filepath.Base(argv[0]) {
	case "go":
		for len(args) > 0 && (args[0] == "-C" || strings.HasPrefix(args[0], "-C=")) { // go -C dir run
			if args[0] == "-C" && len(args) > 1 {
				args = args[1:]
			}
			args = args[1:]
		}
		if len(args) == 0 || args[0] != "run" {
			return false, ""
		}
		pkgs, _ = operands(args[1:], goValueFlags, nil)
		pinned = func(p string) bool { return fixedAt(p, goVersion) }
	case "npx":
		pkgs = launcherPackages(args, []string{"-p", "--package"}, npxValueFlags, []string{"-c", "--call"}, nil)
	case "pnpm":
		dlx := slices.Index(args, "dlx")
		if dlx < 0 {
			return false, ""
		}
		// --package may come before dlx too: pnpm --package=a dlx tool.
		pkgs = launcherPackages(append(slices.Clone(args[:dlx]), args[dlx+1:]...), []string{"-p", "--package"}, nil, nil, []string{"-c", "--shell-mode"})
	case "uvx":
		var from, withs, opaque []string
		tool, _ := operands(args, uvxValueFlags, map[string]*[]string{"--from": &from, "--with": &withs, "-w": &withs,
			"--with-requirements": &opaque, "--with-editable": &opaque})
		if len(opaque) > 0 { // a requirements file or a local tree: no version to check
			return true, FloatingReason
		}
		pkgs = tool
		if len(from) > 0 {
			pkgs = from
		}
		pkgs = append(pkgs, withs...)
		pinned = func(p string) bool {
			if name, v, ok := strings.Cut(p, "=="); ok {
				return name != "" && pyVersion.MatchString(v)
			}
			return fixedAt(p, pyVersion)
		}
	default:
		return false, ""
	}
	if len(pkgs) == 0 {
		return true, FloatingReason
	}
	for _, p := range pkgs {
		if !pinned(p) {
			return true, FloatingReason
		}
	}
	return false, ""
}

// launcherPackages is what npx or pnpm dlx fetches: every --package value
// when one is given, else the first operand. npx's shell flag (-c, given
// the command) with no --package runs whatever that names: nil, so it
// counts as floating. pnpm's shell mode is a switch, and its command's
// first word is the package.
func launcherPackages(args, pkgFlags []string, valueFlags map[string]bool, shellValueFlags, shellSwitches []string) []string {
	var named, shellCmd []string
	collect := map[string]*[]string{}
	for _, f := range pkgFlags {
		collect[f] = &named
	}
	for _, f := range shellValueFlags {
		collect[f] = &shellCmd
	}
	first, _ := operands(args, valueFlags, collect)
	shell := len(shellCmd) > 0
	if len(named) > 0 {
		return named
	}
	if shell {
		return nil
	}
	if len(first) == 1 && slices.ContainsFunc(args, func(a string) bool { return slices.Contains(shellSwitches, a) }) {
		if words := strings.Fields(first[0]); len(words) > 0 {
			return words[:1]
		}
		return nil
	}
	return first
}

// operands walks args up to the first one that is not a flag, which it
// returns (nil when there is none). A flag in collect has its value (the
// next argument, or =value) appended to the slice it maps to; a flag in
// skip has its value skipped, and skipped reports whether one was seen.
func operands(args []string, skip map[string]bool, collect map[string]*[]string) (first []string, skipped bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return []string{a}, skipped
		}
		name, val, hasVal := strings.Cut(a, "=")
		if collect[name] == nil && !skip[name] && !strings.HasPrefix(a, "--") && len(a) > 2 {
			// An attached short value: -wVALUE.
			if short := a[:2]; collect[short] != nil || skip[short] {
				name, val, hasVal = short, a[2:], true
			}
		}
		if collect[name] == nil && !skip[name] {
			continue // a switch
		}
		if !hasVal && i+1 < len(args) {
			i++
			val = args[i]
		}
		if collect[name] != nil {
			*collect[name] = append(*collect[name], val)
		} else {
			skipped = true
		}
	}
	return nil, skipped
}

// fixedAt reports whether p ends in @<version> with version matching re; a
// leading @ is an npm scope, not a version.
func fixedAt(p string, re *regexp.Regexp) bool {
	i := strings.LastIndex(p, "@")
	if i <= 0 {
		return false
	}
	return re.MatchString(p[i+1:])
}
