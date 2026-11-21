package secretref

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An Authorization-style value registers the credential after its scheme,
// in any case, so an error echoing only the token is redacted.
func TestScrub_SchemePrefixedCredential(t *testing.T) {
	s := NewScrubber()
	s.Add("Bearer tok-abc-123456")
	s.Add("token gh-xyz-987654/q")
	s.Add("basic " + base64.StdEncoding.EncodeToString([]byte("bob:pw-secret-42")))
	for _, in := range []string{
		"401: token tok-abc-123456 is invalid",
		"url https://h/x?t=gh-xyz-987654%2Fq",
		`json {"t":"gh-xyz-987654\/q"}`,
		"pass pw-secret-42",
	} {
		if out := s.Scrub(in); strings.Contains(out, "123456") || strings.Contains(out, "987654") || strings.Contains(out, "pw-secret") {
			t.Errorf("%q -> %q", in, out)
		}
	}
	if out := s.Scrub("Bearer is a word"); out != "Bearer is a word" {
		t.Errorf("the scheme word alone must stay: %q", out)
	}
}

// A dotenv value followed by " #" is ambiguous (comment or value?): refused
// with the line number, never the content.
func TestFile_InlineCommentRefused(t *testing.T) {
	p := writeFile(t, 0o600, "A=secret-part # a note\nB=\"quoted # kept\"\n")
	r, _ := newTestResolver(t, nil, nil)
	_, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"A": {File: &FileRef{Path: p, Key: "A"}}}})
	if err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "#") || strings.Contains(err.Error(), "secret-part") {
		t.Fatalf("err = %v, want a refusal naming line 1 and the comment, without the value", err)
	}
	q := writeFile(t, 0o600, "B=\"quoted # kept\"\n")
	v, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"B": {File: &FileRef{Path: q, Key: "B"}}}})
	if err != nil || v.Env["B"] != "quoted # kept" {
		t.Fatalf("a quoted # is the value: %v %#v", err, v.Env)
	}
}

// Each source file is read only up to 1 MiB: a name past it is not found.
func TestDiscover_ReadsAtMost1MiBPerFile(t *testing.T) {
	dir := t.TempDir()
	body := `os.getenv("EARLY_NAME")` + "\n" + strings.Repeat("# filler\n", (1<<20)/9+1) + `os.getenv("LATE_NAME")` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "big.py"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		names = append(names, f.Name)
	}
	got := strings.Join(names, ",")
	if !strings.Contains(got, "EARLY_NAME") || strings.Contains(got, "LATE_NAME") {
		t.Fatalf("findings %s: want EARLY_NAME only", got)
	}
}

// discover never suggests a file reference into a .env the resolver would
// refuse (an unquoted value with " #" after it), even for its clean lines.
func TestDiscover_InlineCommentDotenvNotSuggested(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=x # note\nB=y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Kind == "dotenv_key" && f.FileRefOK {
			t.Fatalf("%s offered as a file reference", f.Name)
		}
	}
}

// A line longer than any read buffer is still checked whole.
func TestDiscover_LongLineInlineCommentNotSuggested(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A="+strings.Repeat("x", 6000)+" # note\nB=y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Kind == "dotenv_key" && f.FileRefOK {
			t.Fatalf("%s offered as a file reference", f.Name)
		}
	}
}

// A quoted value with a comment after its closing quote is refused, even
// when the comment ends in a quote too, and is not suggested.
func TestFile_QuotedValueThenCommentRefused(t *testing.T) {
	p := writeFile(t, 0o600, "API_TOKEN=\"value\" # token for \"dev\"\n")
	r, _ := newTestResolver(t, nil, nil)
	if _, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"API_TOKEN": {File: &FileRef{Path: p, Key: "API_TOKEN"}}}}); err == nil {
		t.Fatal("a comment after a quoted value must be refused")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("API_TOKEN=\"value\" # token for \"dev\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Kind == "dotenv_key" && f.FileRefOK {
			t.Fatalf("%s offered as a file reference", f.Name)
		}
	}
}

// A source file cut at 1 MiB says so.
func TestDiscover_TruncatedFileNote(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.py"), []byte(strings.Repeat("# filler\n", (1<<20)/9+10)), 0o600); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Kind == NoteTruncated && strings.HasSuffix(f.Where, "big.py") {
			found = true
		}
	}
	if !found {
		t.Fatal("want a note that big.py was read only in part")
	}
}

// A backslash before a quote does not close the value; the bytes between
// the outer quotes are returned as written.
func TestFile_EscapedQuoteKept(t *testing.T) {
	p := writeFile(t, 0o600, "TOKEN=\"alpha\\\"beta\"\nOTHER=x\n")
	r, _ := newTestResolver(t, nil, nil)
	v, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{"TOKEN": {File: &FileRef{Path: p, Key: "TOKEN"}}}})
	if err != nil || v.Env["TOKEN"] != `alpha\"beta` {
		t.Fatalf("got %q, %v", v.Env["TOKEN"], err)
	}
}

// Every source file cut at 1 MiB gets its own note.
func TestDiscover_EveryTruncatedFileNoted(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.py", "b.py"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(strings.Repeat("# filler\n", (1<<20)/9+10)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	notes := 0
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if f.Kind == NoteTruncated {
			notes++
		}
	}
	if notes != 2 {
		t.Fatalf("notes = %d, want 2", notes)
	}
}

// A quote after an even run of backslashes closes the value; after an odd
// run it does not.
func TestFile_BackslashRuns(t *testing.T) {
	p := writeFile(t, 0o600, `A="ends\\"`+"\n"+`B="in\\\"side"`+"\n")
	r, _ := newTestResolver(t, nil, nil)
	v, err := r.Resolve(context.Background(), "s", Refs{Env: map[string]Ref{
		"A": {File: &FileRef{Path: p, Key: "A"}}, "B": {File: &FileRef{Path: p, Key: "B"}}}})
	if err != nil || v.Env["A"] != `ends\\` || v.Env["B"] != `in\\\"side` {
		t.Fatalf("got %#v, %v", v.Env, err)
	}
}

// Leading or trailing whitespace does not hide the Authorization scheme.
func TestScrub_SchemeAfterWhitespace(t *testing.T) {
	s := NewScrubber()
	s.Add("  Bearer tok-ws-445566\n")
	if out := s.Scrub("token tok-ws-445566 refused"); strings.Contains(out, "445566") {
		t.Fatalf("%q", out)
	}
}

// A name cut by the 1 MiB limit is not reported as a shorter name.
func TestDiscover_NoPartialNameAtTheCut(t *testing.T) {
	dir := t.TempDir()
	body := "x\n" + strings.Repeat("x", maxSourceBytes-2-len("process.env.ABCDE")) + "process.env.ABCDEFGHIJKLMNOP\n"
	if err := os.WriteFile(filepath.Join(dir, "big.js"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustDiscover(t, ServerDef{Cwd: dir}) {
		if strings.HasPrefix(f.Name, "ABC") {
			t.Fatalf("partial name reported: %s", f.Name)
		}
	}
}
