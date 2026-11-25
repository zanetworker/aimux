package modelproxy

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	sseType  = "text/event-stream; charset=utf-8"
	jsonType = "application/json"
)

var wantAnthropicUsage = Usage{InputTokens: 1200, CacheReadInputTokens: 90000, CacheCreationInputTokens: 300, OutputTokens: 40}

func anthropicFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "anthropic", name)) // #nosec G304 -- a fixture named by the test
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(name, ".sse") {
		// Every event ends with a blank line; the repository's end-of-file rule keeps one newline only.
		data = append(bytes.TrimRight(data, "\n"), "\n\n"...)
	}
	return data
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// feed writes data in pieces of size chunk and fails if Write ever reports anything but the whole piece.
func feed(t *testing.T, u *usageReader, data []byte, chunk int) {
	t.Helper()
	for len(data) > 0 {
		n := min(chunk, len(data))
		got, err := u.Write(data[:n])
		if got != n || err != nil {
			t.Fatalf("Write = %d, %v; want %d, nil", got, err, n)
		}
		data = data[n:]
	}
}

func TestUsageReader_StreamedMessage(t *testing.T) {
	u := newUsageReader(sseType, "")
	feed(t, u, anthropicFixture(t, "stream.sse"), 1<<20)
	model, usage, known := u.result()
	if !known || model != "claude-sonnet-5-5" || usage != wantAnthropicUsage {
		t.Fatalf("result = %q, %+v, %v", model, usage, known)
	}
	if !u.complete() {
		t.Error("a stream that ended with message_stop should be complete")
	}
}

func TestUsageReader_EventsSplitAcrossWrites(t *testing.T) {
	for name, tc := range map[string]struct {
		data     []byte
		encoding string
	}{
		"plain": {anthropicFixture(t, "stream.sse"), ""},
		"gzip":  {gzipped(t, anthropicFixture(t, "stream.sse")), "gzip"},
		"crlf":  {bytes.ReplaceAll(anthropicFixture(t, "stream.sse"), []byte("\n"), []byte("\r\n")), "identity"},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUsageReader(sseType, tc.encoding)
			feed(t, u, tc.data, 1)
			model, usage, known := u.result()
			if !known || model != "claude-sonnet-5-5" || usage != wantAnthropicUsage {
				t.Fatalf("result = %q, %+v, %v", model, usage, known)
			}
		})
	}
}

func TestUsageReader_JSONBody(t *testing.T) {
	for name, tc := range map[string]struct {
		data     []byte
		encoding string
	}{
		"plain": {anthropicFixture(t, "message.json"), ""},
		"gzip":  {gzipped(t, anthropicFixture(t, "message.json")), "gzip"},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUsageReader(jsonType, tc.encoding)
			feed(t, u, tc.data, 7)
			model, usage, known := u.result()
			if !known || model != "claude-sonnet-5-5" || usage != wantAnthropicUsage {
				t.Fatalf("result = %q, %+v, %v", model, usage, known)
			}
		})
	}
}

func TestUsageReader_CutShortKeepsWhatItSaw(t *testing.T) {
	stream := anthropicFixture(t, "stream.sse")
	cut := stream[:bytes.Index(stream, []byte("event: content_block_delta"))]
	for name, tc := range map[string]struct {
		data     []byte
		encoding string
	}{
		"plain": {cut, ""},
		"gzip":  {gzipped(t, stream)[:len(gzipped(t, stream))-12], "gzip"},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUsageReader(sseType, tc.encoding)
			feed(t, u, tc.data, 64)
			model, usage, known := u.result()
			if !known || model != "claude-sonnet-5-5" || usage.InputTokens != 1200 {
				t.Fatalf("result = %q, %+v, %v", model, usage, known)
			}
			if name == "plain" && u.complete() {
				t.Error("a stream with no message_stop is not complete")
			}
		})
	}
}

func TestUsageReader_UnknownShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		contentType, encoding string
		data                  []byte
	}{
		"error body":              {jsonType, "", anthropicFixture(t, "error.json")},
		"stream without usage":    {sseType, "", anthropicFixture(t, "stream_no_usage.sse")},
		"encoding it cannot read": {sseType, "br", anthropicFixture(t, "stream.sse")},
		"gzip that is not gzip":   {sseType, "gzip", anthropicFixture(t, "stream.sse")},
		"empty body":              {jsonType, "", nil},
		"not JSON":                {jsonType, "", []byte("<html>bad gateway</html>")},
		"another content type":    {"text/html", "", anthropicFixture(t, "message.json")},
		"no content type":         {"", "", anthropicFixture(t, "message.json")},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUsageReader(tc.contentType, tc.encoding)
			feed(t, u, tc.data, 5)
			if model, usage, known := u.result(); known || usage != (Usage{}) {
				t.Fatalf("result = %q, %+v, %v; want nothing known", model, usage, known)
			}
			// A second call gives the same answer and does not hang.
			if _, _, known := u.result(); known {
				t.Fatal("second result() changed its answer")
			}
		})
	}
}

func TestUsageReader_VeryLongLineIsSkipped(t *testing.T) {
	long := "event: message_start\ndata: {\"type\":\"message_start\",\"pad\":\"" + strings.Repeat("x", maxUsageLine+10) + "\"}\n\n"
	u := newUsageReader(sseType, "")
	feed(t, u, []byte(long), 4096)
	feed(t, u, anthropicFixture(t, "stream.sse"), 4096)
	if _, usage, known := u.result(); !known || usage != wantAnthropicUsage {
		t.Fatalf("after an oversized line, result = %+v, %v", usage, known)
	}
}

func TestUsageReader_KeepsNoText(t *testing.T) {
	for name, tc := range map[string]struct {
		contentType string
		data        []byte
	}{
		"stream": {sseType, anthropicFixture(t, "stream.sse")},
		"json":   {jsonType, anthropicFixture(t, "message.json")},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUsageReader(tc.contentType, "")
			feed(t, u, tc.data, 9)
			u.result()
			// %+v prints byte slices as numbers, so the buffers are checked as text too, spare capacity included.
			for _, dump := range []string{fmt.Sprintf("%+v", u), string(u.body[:cap(u.body)]), string(u.sse.line[:cap(u.sse.line)])} {
				if strings.Contains(dump, "SECRET") {
					t.Fatalf("the reader kept response text: %s", dump)
				}
			}
		})
	}
}

func TestUsageReader_ModelWithoutUsage(t *testing.T) {
	u := newUsageReader(jsonType, "")
	feed(t, u, []byte(`{"type":"message","model":"claude-haiku-5-5","content":[]}`), 1024)
	if model, usage, known := u.result(); model != "claude-haiku-5-5" || known || usage != (Usage{}) {
		t.Fatalf("result = %q, %+v, %v; want the model and no usage", model, usage, known)
	}
}

func TestUsageReader_UsageWithNoCountIsUnknown(t *testing.T) {
	for _, body := range []string{`{"model":"m","usage":{}}`, `{"model":"m","usage":{"server_tool_use":{"web_search_requests":1}}}`} {
		u := newUsageReader(jsonType, "")
		feed(t, u, []byte(body), 1024)
		if _, _, known := u.result(); known {
			t.Errorf("%s: usage reported as known", body)
		}
	}
	u := newUsageReader(jsonType, "")
	feed(t, u, []byte(`{"model":"m","usage":{"output_tokens":0}}`), 1024)
	if _, _, known := u.result(); !known {
		t.Error("an explicit zero is a count that was read")
	}
}

func TestUsageReader_Complete(t *testing.T) {
	stream := anthropicFixture(t, "stream.sse")
	nameOnly := stream[:bytes.Index(stream, []byte("event: message_stop"))+len("event: message_stop\n")]
	whole := gzipped(t, stream)
	badChecksum := append([]byte(nil), whole...)
	badChecksum[len(badChecksum)-6] ^= 0xff
	for name, tc := range map[string]struct {
		data     []byte
		encoding string
		want     bool
	}{
		"whole stream":                        {stream, "", true},
		"whole gzip stream":                   {whole, "gzip", true},
		"ends after the message_stop name":    {nameOnly, "", false},
		"ends after message_stop's data line": {append(bytes.TrimRight(stream, "\n"), 0x0a), "", false},
		"gzip with a wrong checksum":          {badChecksum, "gzip", false},
		"gzip missing its trailer":            {whole[:len(whole)-8], "gzip", false},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUsageReader(sseType, tc.encoding)
			feed(t, u, tc.data, 512)
			_, usage, known := u.result()
			if !known || usage.InputTokens != 1200 {
				t.Errorf("usage = %+v, %v; the figures read before the end are kept", usage, known)
			}
			if got := u.complete(); got != tc.want {
				t.Errorf("complete() = %v, want %v", got, tc.want)
			}
		})
	}
}
