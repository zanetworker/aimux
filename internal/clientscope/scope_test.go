package clientscope

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults_WingmanLimits2And20(t *testing.T) {
	w := Defaults().Clients["wingman"]
	if w.MaxSessions != 2 || w.TurnsPerMinute != 20 {
		t.Fatalf("got %d/%d, want 2/20", w.MaxSessions, w.TurnsPerMinute)
	}
}

func TestDefaults_DeviceStepUpSecureEnclaveOnly(t *testing.T) {
	d := Defaults().Clients["device"]
	if len(d.StepUpKinds) != 1 || d.StepUpKinds[0] != "secure-enclave" {
		t.Fatalf("got %v", d.StepUpKinds)
	}
}

func TestDefaults_WingmanNeverCoding(t *testing.T) {
	s := Defaults()
	if s.Allowed("wingman", "coding") {
		t.Fatal("wingman must not use coding")
	}
	for _, p := range []string{"page", "research", "work-readonly", "research-with-work"} {
		if !s.Allowed("wingman", p) {
			t.Errorf("wingman should allow %s", p)
		}
	}
}

func TestDefaults_DeviceHasCoding(t *testing.T) {
	if !Defaults().Allowed("device", "coding") {
		t.Fatal("device should allow coding")
	}
}

func TestDefaults_WingmanSocketExpandsHome(t *testing.T) {
	t.Setenv("HOME", "/home/x")
	if got := Defaults().Clients["wingman"].Socket; got != "/home/x/.aimux/clients/wingman.sock" {
		t.Fatalf("got %q", got)
	}
}

func TestAllowed_UnknownClientFalse(t *testing.T) {
	if Defaults().Allowed("nobody", "page") {
		t.Fatal("unknown client must be denied")
	}
}

func TestAllowed_CLIAnyProfile(t *testing.T) {
	if !Defaults().Allowed("cli", "anything-at-all") {
		t.Fatal("cli may use any profile")
	}
}

func TestAllowed_NilScopesNoPanic(t *testing.T) {
	var s Scopes
	if s.Allowed("cli", "page") {
		t.Fatal("nil scopes allow nothing")
	}
}

func TestLimit(t *testing.T) {
	s := Defaults()
	if m, r := s.Limit("wingman"); m != 2 || r != 20 {
		t.Fatalf("wingman %d/%d", m, r)
	}
	if m, r := s.Limit("cli"); m != 0 || r != 0 {
		t.Fatalf("cli should be unlimited, got %d/%d", m, r)
	}
	if m, r := s.Limit("nobody"); m != 0 || r != 0 {
		t.Fatalf("unknown %d/%d", m, r)
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clients.yaml")
	if err := writeFile(p, body); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_MissingFileIsDefaults(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Allowed("device", "coding") || s.Clients["wingman"].MaxSessions != 2 {
		t.Fatal("expected defaults")
	}
}

func TestLoad_FileReplacesProfiles(t *testing.T) {
	s, err := Load(write(t, "clients:\n  wingman:\n    profiles: [page]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Allowed("wingman", "page") || s.Allowed("wingman", "research") {
		t.Fatal("profiles should be replaced")
	}
	if !s.Allowed("device", "coding") {
		t.Fatal("untouched clients keep defaults")
	}
}

func TestLoad_WingmanEntryKeepsDefaultLimits(t *testing.T) {
	s, err := Load(write(t, "clients:\n  wingman:\n    profiles: [page]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m, r := s.Limit("wingman"); m != 2 || r != 20 {
		t.Fatalf("got %d/%d, want 2/20", m, r)
	}
	s, err = Load(write(t, "clients:\n  wingman:\n    max_sessions: 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m, r := s.Limit("wingman"); m != 5 || r != 20 {
		t.Fatalf("got %d/%d, want 5/20", m, r)
	}
}

func TestLoad_WingmanZeroLimitRejected(t *testing.T) {
	for _, body := range []string{"max_sessions: 0", "turns_per_minute: 0"} {
		if _, err := Load(write(t, "clients:\n  wingman:\n    "+body+"\n")); err == nil {
			t.Errorf("%s must be rejected for wingman", body)
		}
	}
}

func TestLoad_DeviceEmptyStepUpRejected(t *testing.T) {
	if _, err := Load(write(t, "clients:\n  device:\n    step_up_kinds: []\n")); err == nil {
		t.Fatal("empty step_up_kinds must be rejected for device")
	}
}

func TestLoad_DeviceEntryKeepsStepUp(t *testing.T) {
	s, err := Load(write(t, "clients:\n  device:\n    profiles: [page]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if k := s.Clients["device"].StepUpKinds; len(k) != 1 || k[0] != "secure-enclave" {
		t.Fatalf("got %v", k)
	}
}

func TestLoad_WingmanCannotGetCoding(t *testing.T) {
	_, err := Load(write(t, "clients:\n  wingman:\n    profiles: [page, coding]\n"))
	if err == nil || !strings.Contains(err.Error(), "wingman") || !strings.Contains(err.Error(), "coding") {
		t.Fatalf("want error naming wingman and coding, got %v", err)
	}
}

func TestLoad_NegativeLimitRejected(t *testing.T) {
	for _, c := range []string{"cli", "device", "wingman"} {
		for _, f := range []string{"max_sessions", "turns_per_minute"} {
			_, err := Load(write(t, "clients:\n  "+c+":\n    "+f+": -1\n"))
			if err == nil || !strings.Contains(err.Error(), c) || !strings.Contains(err.Error(), f) {
				t.Errorf("%s %s: want error naming both, got %v", c, f, err)
			}
		}
	}
}

func TestLoad_UnknownFieldRejected(t *testing.T) {
	if _, err := Load(write(t, "clients:\n  wingman:\n    bogus: 1\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_WildcardOnlyForCLI(t *testing.T) {
	if _, err := Load(write(t, "clients:\n  device:\n    profiles: ['*']\n")); err == nil {
		t.Fatal("wildcard for non-cli must be rejected")
	}
	s, err := Load(write(t, "clients:\n  cli:\n    profiles: ['*']\n"))
	if err != nil || !s.Allowed("cli", "x") {
		t.Fatalf("cli wildcard should load: %v", err)
	}
}

func TestLoad_EmptyProfilesMeansNothing(t *testing.T) {
	s, err := Load(write(t, "clients:\n  wingman:\n    profiles: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Allowed("wingman", "page") {
		t.Fatal("empty list allows nothing")
	}
}

func TestLoad_InvalidYAMLErrors(t *testing.T) {
	if _, err := Load(write(t, "clients: [unclosed")); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoad_EmptyFileIsDefaults(t *testing.T) {
	s, err := Load(write(t, ""))
	if err != nil || !s.Allowed("device", "coding") {
		t.Fatalf("empty file: %v", err)
	}
}
