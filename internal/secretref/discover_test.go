package secretref

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func findingSet(fs []Finding) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Kind+":"+f.Name] = true
	}
	return out
}

func TestDiscover_ConfigRefs(t *testing.T) {
	def := ServerDef{
		Command: []string{"/bin/srv", "--token", "${API_TOKEN}"},
		URL:     "https://host/mcp?x=${QUERY_PARAM:-d}",
		Env:     map[string]string{"A": "$PLAIN", "B": "${FROM_ENV}"},
		Headers: map[string]string{"Authorization": "Bearer ${HDR_TOKEN}"},
	}
	fs, err := Discover(def)
	if err != nil {
		t.Fatal(err)
	}
	got := findingSet(fs)
	for _, want := range []string{"config_ref:API_TOKEN", "config_ref:QUERY_PARAM", "config_ref:FROM_ENV", "config_ref:HDR_TOKEN"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if got["config_ref:PLAIN"] {
		t.Errorf("$PLAIN without braces is not a reference")
	}
}

func TestDiscover_DotenvKeysOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("# c\nJIRA_TOKEN=sentinel-xyz\nexport OTHER=\"q\"\n\nBAD LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := mustDiscover(t, ServerDef{Cwd: dir})
	got := findingSet(fs)
	if !got["dotenv_key:JIRA_TOKEN"] || !got["dotenv_key:OTHER"] {
		t.Fatalf("dotenv names missing: %v", got)
	}
	for _, f := range fs {
		if strings.Contains(fmt.Sprintf("%+v", f), "sentinel-xyz") {
			t.Fatalf("value leaked into finding %+v", f)
		}
	}
}

func TestDiscover_ParentDotenv(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "mcp-server")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("UP=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !findingSet(mustDiscover(t, ServerDef{Cwd: sub}))["dotenv_key:UP"] {
		t.Fatal("cwd/../.env name not found")
	}
}

func TestDiscover_SourceReadsPythonGoNode(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.py":  "import os\nx = os.getenv(\"PY_A\")\ny = os.environ[\"PY_B\"]\nz = os.environ.get('PY_C')\n",
		"b.go":  "package x\nimport \"os\"\nvar v = os.Getenv(\"GO_A\")\n",
		"c.js":  "const t = process.env.NODE_A;\nconst u = process.env[\"NODE_B\"];\n",
		"d.txt": "os.getenv(\"NOT_SOURCE\")\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := findingSet(mustDiscover(t, ServerDef{Cwd: dir}))
	for _, want := range []string{"PY_A", "PY_B", "PY_C", "GO_A", "NODE_A", "NODE_B"} {
		if !got["source_read:"+want] {
			t.Errorf("missing source_read:%s in %v", want, got)
		}
	}
	if got["source_read:NOT_SOURCE"] {
		t.Error("a .txt file is not source")
	}
}

func TestDiscover_ScanCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxScanFiles+1; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.py", i)), []byte("x=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	notes := 0
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Kind == "note" {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("want one scan-cap note, got %d", notes)
	}
}

func mustDiscover(t *testing.T, def ServerDef) []Finding {
	t.Helper()
	fs, err := Discover(def)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestSuggest(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	fs := []Finding{
		{Name: "JIRA_TOKEN", Kind: "dotenv_key", Where: env, FileRefOK: true},
		{Name: "EXPORTED", Kind: "dotenv_key", Where: env},
		{Name: "OTHER", Kind: "source_read", Where: "a.py:2"},
	}
	s := Suggest("JirAIa", fs)
	if s["JIRA_TOKEN"] != `{file: {path: "`+env+`", key: "JIRA_TOKEN"}}` {
		t.Errorf("dotenv suggestion: %q", s["JIRA_TOKEN"])
	}
	if s["EXPORTED"] != `{keychain: {item: "aimux/JirAIa", field: "env.EXPORTED"}}` {
		t.Errorf("an unusable dotenv line gets a keychain suggestion: %q", s["EXPORTED"])
	}
	if s["OTHER"] != `{keychain: {item: "aimux/JirAIa", field: "env.OTHER"}}` {
		t.Errorf("keychain suggestion: %q", s["OTHER"])
	}
	for n, v := range s {
		var m map[string]Ref
		if err := yaml.Unmarshal([]byte(strconv.Quote(n)+": "+v), &m); err != nil || m[n].Validate() != nil {
			t.Errorf("%s: does not decode as a typed reference: %v", n, err)
		}
	}
}

func TestSuggest_NullNameRoundTrips(t *testing.T) {
	fs := []Finding{{Name: "NULL", Kind: "source_read", Where: "a.py:1"}}
	v := Suggest("srv", fs)["NULL"]
	var m map[string]Ref
	if err := yaml.Unmarshal([]byte(strconv.Quote("NULL")+": "+v), &m); err != nil {
		t.Fatal(err)
	}
	if r, ok := m["NULL"]; !ok || r.Keychain == nil || r.Keychain.Field != "env.NULL" {
		t.Fatalf("NULL did not round-trip: %+v", m)
	}
}

func TestDiscover_ExportPrefixIsNotTheKeyword(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("export_token=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Name == "export_token" && !f.FileRefOK {
			t.Fatal("export_token= is a plain assignment")
		}
	}
}

func TestDiscover_ReadSpanningLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.py"), []byte("x = os.getenv(\n    \"SPLIT_A\"\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !findingSet(mustDiscover(t, ServerDef{Cwd: dir}))["source_read:SPLIT_A"] {
		t.Fatal("a read whose argument is on the next line was missed")
	}
}

func TestDiscover_BadCwdIsAnError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{filepath.Join(t.TempDir(), "missing"), f} {
		if _, err := Discover(ServerDef{Cwd: cwd}); err == nil {
			t.Errorf("%s: want an error", cwd)
		}
	}
}

func TestDiscover_RelativeCwdReadsParent(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "srv")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("UPPER=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if !findingSet(mustDiscover(t, ServerDef{Cwd: "."}))["dotenv_key:UPPER"] {
		t.Fatal("--cwd . must read ../.env")
	}
}

func TestDiscover_SkipsNamedPipes(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "p.py"), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, ".env"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _, _ = Discover(ServerDef{Cwd: dir}); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("discover blocked on a named pipe")
	}
}

func TestDiscover_LongDotenvValueKeepsLaterNames(t *testing.T) {
	dir := t.TempDir()
	body := "BIG=" + strings.Repeat("a", 200_000) + "\nAFTER=1\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := findingSet(mustDiscover(t, ServerDef{Cwd: dir}))
	if !got["dotenv_key:BIG"] || !got["dotenv_key:AFTER"] {
		t.Fatalf("names lost after a long value: %v", got)
	}
}

func TestDiscover_FileRefOKOnlyForUsableLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GOOD=1\nexport BAD=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok := map[string]bool{}
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Kind == "dotenv_key" {
			ok[f.Name] = f.FileRefOK
		}
	}
	if ok["GOOD"] || ok["BAD"] {
		t.Fatalf("an export line makes the whole file unusable for the file backend: %v", ok)
	}
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, ".env"), []byte("# c\nGOOD=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustDiscover(t, ServerDef{Cwd: clean}) {
		if f.Name == "GOOD" && !f.FileRefOK {
			t.Fatal("a strict 0600 .env is usable")
		}
	}
	loose := t.TempDir()
	if err := os.WriteFile(filepath.Join(loose, ".env"), []byte("G=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(loose, ".env"), 0o644); err != nil { //nolint:gosec // G302: the test needs a world-readable file
		t.Fatal(err)
	}
	for _, f := range mustDiscover(t, ServerDef{Cwd: loose}) {
		if f.Name == "G" && f.FileRefOK {
			t.Fatal("a world-readable .env is not usable as a file reference")
		}
	}
}

func TestDiscover_FileRefOKNeedsAStrictFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GOOD=1\nnot a line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Name == "GOOD" && f.FileRefOK {
			t.Fatal("a .env the file backend would refuse is not usable as a file reference")
		}
	}
}
