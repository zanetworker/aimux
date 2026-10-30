package devicelogin

import (
	"crypto/sha256"
	"regexp"
	"strings"
	"testing"
)

var displayCode = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

func TestNewCode_DisplayFormAndHash(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, hash := NewCode()
		if !displayCode.MatchString(code) {
			t.Fatalf("code %q is not XXXX-XXXX in Crockford base32", code)
		}
		if want := sha256.Sum256([]byte(strings.ReplaceAll(code, "-", ""))); hash != want {
			t.Fatalf("hash of %q is not the SHA-256 of the code without its dash", code)
		}
		seen[code] = true
	}
	if len(seen) < 199 { // 40 random bits: a repeat in 200 is all but impossible
		t.Fatalf("only %d distinct codes in 200", len(seen))
	}
}

func TestParseCode_RoundTripsNewCode(t *testing.T) {
	for range 50 {
		code, hash := NewCode()
		for _, typed := range []string{code, strings.ReplaceAll(code, "-", ""), strings.ToLower(code), "  " + code + "\n"} {
			got, err := ParseCode(typed)
			if err != nil {
				t.Fatalf("ParseCode(%q): %v", typed, err)
			}
			if got != hash {
				t.Fatalf("ParseCode(%q) gave another hash than NewCode", typed)
			}
		}
	}
}

func TestParseCode_CrockfordAliases(t *testing.T) {
	want := sha256.Sum256([]byte("01110ABC"))
	for _, typed := range []string{"OIL1-0abc", "oil10ABC", "0l1i-oAbC"} {
		got, err := ParseCode(typed)
		if err != nil {
			t.Fatalf("ParseCode(%q): %v", typed, err)
		}
		if got != want {
			t.Fatalf("ParseCode(%q) did not map I/L to 1 and O to 0", typed)
		}
	}
}

func TestParseCode_Rejects(t *testing.T) {
	for _, typed := range []string{
		"", "ABCD-EFG", "ABCDEFG", // 7
		"ABCD-EFGHJ", "ABCDEFGHJ", // 9
		"ABCD-EFGU",  // U is not in the alphabet
		"ABCD_EFGH",  // only a dash separates
		"AB-CD-EFGH", // one dash at most
		"ABCD-ÉFGH",
	} {
		if _, err := ParseCode(typed); err == nil {
			t.Errorf("ParseCode(%q) = nil error, want a rejection", typed)
		}
	}
}

func TestParseCode_ErrorNeverEchoesTheCode(t *testing.T) {
	_, err := ParseCode("ZZZZ-ZZZU")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(strings.ToUpper(err.Error()), "ZZZZ") {
		t.Fatalf("error %q repeats the typed code", err)
	}
}

func TestCanonicalCode(t *testing.T) {
	for in, want := range map[string]string{
		"ABCD-1234":  "ABCD1234",
		" abcd1234 ": "ABCD1234",
		"il0o-ABCD":  "1100ABCD",
		"0123456789": "",
		"ABCD-123":   "",
		"ABCU1234":   "",
	} {
		got, err := CanonicalCode(in)
		if want == "" {
			if err == nil {
				t.Errorf("CanonicalCode(%q) = %q, want an error", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("CanonicalCode(%q) = %q, %v; want %q", in, got, err, want)
		}
		if h, _ := ParseCode(in); h != sha256.Sum256([]byte(want)) {
			t.Errorf("ParseCode(%q) is not the hash of its canonical form", in)
		}
	}
}
