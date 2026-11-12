package sandboxlaunch

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/profile"
)

type recBackend struct {
	ensured []string
	execs   [][]string
	cfgs    []string
	err     error
}

func (r *recBackend) Ensure(_ context.Context, c profile.Compiled) error {
	r.ensured = append(r.ensured, c.Sandbox)
	return r.err
}

func (r *recBackend) Exec(_ context.Context, c profile.Compiled, cfg, _ string, args []string) (io.ReadCloser, error) {
	r.execs = append(r.execs, append([]string{c.Sandbox}, args...))
	r.cfgs = append(r.cfgs, cfg)
	return io.NopCloser(strings.NewReader("out")), r.err
}

func (r *recBackend) Download(context.Context, string, string, string) error { return r.err }

func TestLauncher_Delegates(t *testing.T) {
	rb := &recBackend{}
	l := &Launcher{Backend: rb}
	c := compiled("sha256:one")
	if err := l.Ensure(context.Background(), c); err != nil || !slices.Equal(rb.ensured, []string{"aimux-work-readonly"}) {
		t.Fatalf("Ensure: %v, ensured %v", err, rb.ensured)
	}
	rc, err := l.Exec(context.Background(), c, "{}", "", []string{"claude", "-p"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != "out" || !slices.Equal(rb.execs[0], []string{"aimux-work-readonly", "claude", "-p"}) || rb.cfgs[0] != "{}" {
		t.Fatalf("Exec out=%q execs=%v cfgs=%v", b, rb.execs, rb.cfgs)
	}
	rb.err = errors.New("down")
	if err := l.Ensure(context.Background(), c); err == nil || err.Error() != "down" {
		t.Fatalf("Ensure error = %v, want the backend's", err)
	}
}
