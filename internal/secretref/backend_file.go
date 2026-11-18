package secretref

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// maxFileBytes bounds a secret file read.
const maxFileBytes = 64 << 10

// file reads ref without following a symlink, after checking the opened
// file: regular, yours, mode 0600 or stricter.
func (r *Resolver) file(ref FileRef) (string, error) {
	path := expandHome(ref.Path)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the owner's configured secret file
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return "", fmt.Errorf("%s is a symlink; point the reference at the file itself", path)
		}
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s does not exist", path)
		}
		return "", fmt.Errorf("%s: cannot open", path)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("%s: cannot stat", path)
	}
	switch {
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("%s is not a regular file", path)
	case !r.opt.Owner(fi):
		return "", fmt.Errorf("%s is not owned by you", path)
	case fi.Mode().Perm()&0o077 != 0:
		return "", fmt.Errorf("%s has mode %o; it must be 0600 or stricter (chmod 600 %s)", path, fi.Mode().Perm(), path)
	}
	b := make([]byte, maxFileBytes+1)
	n, _ := f.Read(b)
	if n > maxFileBytes {
		return "", fmt.Errorf("%s is over 64 KiB", path)
	}
	body := string(b[:n])
	if ref.Key == "" {
		v := strings.TrimSpace(body)
		if v == "" {
			return "", fmt.Errorf("%s is empty", path)
		}
		return v, nil
	}
	vals, err := parseDotenv(body)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	v, ok := vals[ref.Key]
	if !ok {
		return "", fmt.Errorf("%s defines no %s", path, ref.Key)
	}
	return v, nil
}

// parseDotenv reads KEY=value lines strictly: # comments and blank lines are
// skipped, a value may be wrapped in matching single or double quotes, and
// nothing is interpolated or run. An export line or a line without "=" is
// an error naming the line number, never its content.
func parseDotenv(body string) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 4096), maxFileBytes+1)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			return nil, fmt.Errorf("line %d uses export, which is not supported; write NAME=value", n)
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !dotenvRE.MatchString(k+"=") {
			return nil, fmt.Errorf("line %d is not NAME=value", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, sc.Err()
}
