package secretref

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("%s: cannot read", path)
	}
	if len(b) > maxFileBytes {
		return "", fmt.Errorf("%s is over 64 KiB", path)
	}
	body := string(b)
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

// dotenvValue is a dotenv value (trimmed, as written) as the resolver
// reads it: the bytes inside its quotes (a quote after an odd run of
// backslashes does not close it, and is kept as written), or as is. ok is false when anything
// follows the closing quote, or an unquoted value holds " #": other dotenv
// readers drop that as a comment, some keep it, so the resolver refuses it
// and discover does not suggest the file.
func dotenvValue(v string) (string, bool) {
	backslashesBefore := func(s string, i int) int {
		n := 0
		for i--; i >= 0 && s[i] == '\\'; i-- {
			n++
		}
		return n
	}
	if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
		for end := 1; end < len(v); end++ {
			if v[end] == v[0] && backslashesBefore(v, end)%2 == 0 { // an escaped quote does not close it
				return v[1:end], end+1 == len(v)
			}
		}
	}
	return v, !strings.Contains(v, " #")
}

// parseDotenv reads KEY=value lines strictly: # comments and blank lines are
// skipped, a value may be wrapped in matching single or double quotes, and
// nothing is interpolated or run. An export line, a line without "=", or an
// unquoted value followed by " #" is an error naming the line number, never its content.
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
		v, ok = dotenvValue(strings.TrimSpace(v))
		if !ok {
			// Other dotenv readers drop " # ..." as a comment; some keep it.
			return nil, fmt.Errorf("line %d has a comment after its value (\" #\" unquoted, or text after the closing quote); quote the value, or move the comment to its own line", n)
		}
		out[k] = v
	}
	return out, sc.Err()
}
