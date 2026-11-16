package clientscope

import (
	"path/filepath"
	"strings"
	"testing"
)

// A socket written as ~/... in the file is the home directory's, as the
// default is: never a literal ~ folder under the working directory.
func TestLoad_SocketTildeExpanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := filepath.Join(t.TempDir(), "clients.yaml")
	if err := writeFile(p, "clients:\n  wingman:\n    socket: ~/.aimux/clients/w.sock\n"); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Clients["wingman"].Socket, filepath.Join(home, ".aimux", "clients", "w.sock"); got != want {
		t.Fatalf("socket %q, want %q", got, want)
	}
}

// A socket path that is not absolute after expanding ~/ is refused, naming
// the client and the path: it would depend on serve's working directory.
func TestLoad_RelativeSocketRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// (A bare `socket: ~` is YAML null, so unset; quoted "~" is the path ~.)
	for _, sock := range []string{"clients/w.sock", "~other/w.sock", "./w.sock", "~"} {
		p := filepath.Join(t.TempDir(), "clients.yaml")
		if err := writeFile(p, "clients:\n  wingman:\n    socket: \""+sock+"\"\n"); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), `"wingman"`) || !strings.Contains(err.Error(), sock) || !strings.Contains(err.Error(), "absolute") {
			t.Errorf("socket %q: Load = %v, want an error naming wingman, the path and absolute", sock, err)
		}
	}
	// An absolute path and an empty one (no socket) stay as given.
	p := filepath.Join(t.TempDir(), "clients.yaml")
	if err := writeFile(p, "clients:\n  wingman:\n    socket: /tmp/ax/w.sock\n  cli:\n    socket: \"\"\n"); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil || s.Clients["wingman"].Socket != "/tmp/ax/w.sock" || s.Clients["cli"].Socket != "" {
		t.Fatalf("Load = %+v %v", s.Clients, err)
	}
}
