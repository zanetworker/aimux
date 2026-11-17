package mcpproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

// headerPlaceholder stands in for a header value token in every copy of a
// moved HTTP entry aimux keeps (manifest, backup); the value is in the
// Keychain.
const headerPlaceholder = `"[in Keychain]"`

// HeaderService is the Keychain item that holds server's header values.
func HeaderService(server string) string { return "aimux-mcp-proxy/" + server }

// headerSpan is one header value token in an entry: its name and byte span.
type headerSpan struct {
	name       string
	start, end int
}

// headerSpans finds the value tokens of the entry's top-level "headers"
// object. A value that is not a string, or a header named twice, is an
// error; an entry without headers has none.
func headerSpans(raw []byte) ([]headerSpan, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("server entry is not a JSON object")
	}
	var spans []headerSpan
	sawHeaders := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, errors.New("server entry is not valid JSON")
		}
		if key != "headers" {
			if err := skipValue(dec); err != nil {
				return nil, err
			}
			continue
		}
		if sawHeaders {
			return nil, errors.New("server entry has two headers objects")
		}
		sawHeaders = true
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			return nil, errors.New("headers is not a map of strings")
		}
		seen := map[string]bool{}
		for dec.More() {
			nameTok, err := dec.Token()
			if err != nil {
				return nil, errors.New("server entry is not valid JSON")
			}
			name, _ := nameTok.(string)
			if seen[name] {
				return nil, fmt.Errorf("header %s is named twice", name)
			}
			seen[name] = true
			afterKey := int(dec.InputOffset())
			v, err := dec.Token()
			if _, ok := v.(string); err != nil || !ok {
				return nil, errors.New("headers is not a map of strings")
			}
			start := afterKey + bytes.IndexByte(raw[afterKey:], '"')
			spans = append(spans, headerSpan{name: name, start: start, end: int(dec.InputOffset())})
		}
		if _, err := dec.Token(); err != nil { // the headers object's '}'
			return nil, errors.New("server entry is not valid JSON")
		}
	}
	return spans, nil
}

// skipValue consumes one whole JSON value from dec.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return errors.New("server entry ends early")
			}
			return errors.New("server entry is not valid JSON")
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}

// redactHeaders returns raw with each value token of its "headers" object
// replaced by headerPlaceholder, and the replaced tokens (raw JSON string
// literals, quotes included) by header name. Every other byte is kept.
func redactHeaders(raw json.RawMessage) (json.RawMessage, map[string]string, error) {
	spans, err := headerSpans(raw)
	if err != nil {
		return nil, nil, err
	}
	literals := make(map[string]string, len(spans))
	var out bytes.Buffer
	last := 0
	for _, s := range spans {
		literals[s.name] = string(raw[s.start:s.end])
		out.Write(raw[last:s.start])
		out.WriteString(headerPlaceholder)
		last = s.end
	}
	out.Write(raw[last:])
	return json.RawMessage(out.Bytes()), literals, nil
}

// restoreHeaders is the inverse of redactHeaders: each placeholder under
// "headers" becomes literals[name]; a missing literal is an error.
func restoreHeaders(redacted json.RawMessage, literals map[string]string) (json.RawMessage, error) {
	spans, err := headerSpans(redacted)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	last := 0
	for _, s := range spans {
		if string(redacted[s.start:s.end]) != headerPlaceholder {
			continue
		}
		lit, ok := literals[s.name]
		if !ok {
			return nil, fmt.Errorf("no saved value for header %s", s.name)
		}
		out.Write(redacted[last:s.start])
		out.WriteString(lit)
		last = s.end
	}
	out.Write(redacted[last:])
	return json.RawMessage(out.Bytes()), nil
}

// headerSecret is the Keychain item of one moved HTTP server.
type headerSecret struct {
	Headers map[string]headerItem `json:"headers"`
}

type headerItem struct {
	Value string `json:"value"` // resolved; what the daemon sends
	// Literal is the token as in the file, what uninstall writes back;
	// empty when it is plainTokenOf(Value), so a long token is not kept
	// twice in an item of limited size.
	Literal string `json:"literal,omitempty"`
}

// literal is the token uninstall writes back.
func (h headerItem) literal() string {
	if h.Literal != "" {
		return h.Literal
	}
	return plainTokenOf(h.Value)
}

// plainTokenOf is v as a JSON string token written the plain way: no HTML
// escaping, no optional escapes.
func plainTokenOf(v string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// headerSecretOf is the Keychain item of one HTTP entry.
func headerSecretOf(e HTTPEntry) headerSecret {
	s := headerSecret{Headers: map[string]headerItem{}}
	for h, v := range e.Headers {
		it := headerItem{Value: v}
		if lit := e.Literals[h]; lit != plainTokenOf(v) {
			it.Literal = lit
		}
		s.Headers[h] = it
	}
	return s
}

// encodeHeaderSecret is s as ASCII-only JSON (non-ASCII as \u escapes), so
// `security find-generic-password -w` prints it as text, never as hex.
func encodeHeaderSecret(s headerSecret) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", errors.New("encode header secret")
	}
	var out strings.Builder
	for _, r := range string(b) {
		if r < 128 {
			out.WriteRune(r)
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(&out, `\u%04x`, u)
		}
	}
	return out.String(), nil
}

// decodeHeaderSecret parses an item encodeHeaderSecret wrote. Its error
// never quotes the input, which holds secrets.
func decodeHeaderSecret(v string) (headerSecret, error) {
	var s headerSecret
	if err := json.Unmarshal([]byte(v), &s); err != nil || s.Headers == nil {
		return headerSecret{}, errors.New("the keychain item is not an aimux header secret")
	}
	return s, nil
}
