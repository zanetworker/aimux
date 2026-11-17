package mcpproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

const sentinelHeader = "Bearer SENTINEL-9f3a-secret"

func TestRedactHeaders_RoundTripIsByteExact(t *testing.T) {
	cases := map[string]string{
		"compact":       `{"type":"http","url":"https://x/mcp","headers":{"Authorization":"Bearer abc"}}`,
		"pretty-tabs":   "{\n\t\"type\": \"http\",\n\t\"url\": \"https://x/mcp\",\n\t\"headers\": {\n\t\t\"Authorization\" :  \"Bearer abc\"\n\t}\n}",
		"headers-first": `{"headers":{"X-Key":"k1"},"type":"http","url":"https://x"}`,
		"escapes":       `{"type":"http","url":"https://x","headers":{"Authorization":"Bearer A\/b\"c"}}`,
		"two-headers":   `{"type":"http","url":"https://x","headers":{"A":"one","B":"two"}}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			red, lits, err := redactHeaders(json.RawMessage(in))
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(red) {
				t.Fatalf("redacted is not JSON: %s", red)
			}
			if !strings.Contains(string(red), headerPlaceholder) {
				t.Fatalf("no placeholder: %s", red)
			}
			back, err := restoreHeaders(red, lits)
			if err != nil {
				t.Fatal(err)
			}
			if string(back) != in {
				t.Fatalf("round trip:\n got %q\nwant %q", back, in)
			}
		})
	}
}

func TestRedactHeaders_LiteralsAreRawTokens(t *testing.T) {
	_, lits, err := redactHeaders(json.RawMessage(`{"headers":{"A":"x\/y"}}`))
	if err != nil || lits["A"] != `"x\/y"` {
		t.Fatalf("lits = %v, err = %v", lits, err)
	}
}

func TestRedactHeaders_OnlyHeaderValuesChange(t *testing.T) {
	in := `{"url":"https://x/headers","meta":{"headers":{"A":"keep"}},"headers":{"A":"drop"}}`
	red, _, err := redactHeaders(json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"url":"https://x/headers","meta":{"headers":{"A":"keep"}},"headers":{"A":` + headerPlaceholder + `}}`
	if string(red) != want {
		t.Fatalf("got %s", red)
	}
}

func TestRedactHeaders_RedactedHoldsNoValue(t *testing.T) {
	red, _, err := redactHeaders(json.RawMessage(`{"headers":{"Authorization":"` + sentinelHeader + `"}}`))
	if err != nil || strings.Contains(string(red), "SENTINEL") {
		t.Fatalf("red = %s, err = %v", red, err)
	}
}

func TestRedactHeaders_NoHeadersIsUnchanged(t *testing.T) {
	in := `{"type":"http","url":"https://x"}`
	red, lits, err := redactHeaders(json.RawMessage(in))
	if err != nil || string(red) != in || len(lits) != 0 {
		t.Fatalf("red = %s lits = %v err = %v", red, lits, err)
	}
}

func TestRedactHeaders_DuplicateHeaderNameIsError(t *testing.T) {
	if _, _, err := redactHeaders(json.RawMessage(`{"headers":{"A":"1","A":"2"}}`)); err == nil {
		t.Fatal("want error")
	}
}

func TestRedactHeaders_TwoHeadersObjectsIsError(t *testing.T) {
	if _, _, err := redactHeaders(json.RawMessage(`{"headers":{"A":"one"},"headers":{"A":"two"}}`)); err == nil {
		t.Fatal("want error")
	}
}

func TestRedactHeaders_NonStringValueIsError(t *testing.T) {
	if _, _, err := redactHeaders(json.RawMessage(`{"headers":{"A":1}}`)); err == nil {
		t.Fatal("want error")
	}
}

func TestRestoreHeaders_MissingLiteralIsError(t *testing.T) {
	_, err := restoreHeaders(json.RawMessage(`{"headers":{"A":`+headerPlaceholder+`}}`), map[string]string{})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestHeaderSecret_RoundTrip(t *testing.T) {
	s := headerSecret{Headers: map[string]headerItem{
		"Authorization": {Value: sentinelHeader, Literal: `"Bearer ${TOKEN}"`},
		"X-Name":        {Value: "Grüße ✓", Literal: `"Grüße ✓"`},
	}}
	enc, err := encodeHeaderSecret(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range enc {
		if r > 127 {
			t.Fatalf("encoded secret is not ASCII: %q", enc)
		}
	}
	got, err := decodeHeaderSecret(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["X-Name"] != s.Headers["X-Name"] || got.Headers["Authorization"] != s.Headers["Authorization"] {
		t.Fatalf("got %+v", got)
	}
}

func TestDecodeHeaderSecret_ErrorHasNoValue(t *testing.T) {
	_, err := decodeHeaderSecret(`{"headers":{"A":{"value":"` + sentinelHeader + `"` + "garbage")
	if err == nil || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("err = %v", err)
	}
}

func TestHeaderService(t *testing.T) {
	if HeaderService("deepwiki") != "aimux-mcp-proxy/deepwiki" {
		t.Fatal(HeaderService("deepwiki"))
	}
}
