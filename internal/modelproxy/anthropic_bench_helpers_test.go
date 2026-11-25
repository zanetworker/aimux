package modelproxy

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/ledger"
)

func benchFixture(b *testing.B) []byte {
	b.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "anthropic", "stream.sse"))
	if err != nil {
		b.Fatal(err)
	}
	return append(bytes.TrimRight(data, "\n"), "\n\n"...)
}

func gzippedBytes(b *testing.B, data []byte) []byte {
	b.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(data)
	if err := zw.Close(); err != nil {
		b.Fatal(err)
	}
	return buf.Bytes()
}

func benchLedger(b *testing.B) *ledger.Writer {
	b.Helper()
	writer, err := ledger.OpenWriter(filepath.Join(b.TempDir(), "ledger"), LedgerName(StablePort))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = writer.Close() })
	return writer
}
