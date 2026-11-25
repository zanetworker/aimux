package modelproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What the Anthropic engine adds to a request: the same streamed, gzip-compressed answer fetched from a local
// stand-in directly, and through the handler with the ledger on. The difference is the proxy's own time: one more
// loopback hop, reading the credentials, the ledger line and the usage reader. Compare the two ns/op figures.
//
//	go test ./internal/modelproxy/ -run '^$' -bench AnthropicEngine -benchtime 2000x
func benchAnswer(b *testing.B, url string, headers map[string]string) {
	b.Helper()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 4}}
	const body = `{"model":"claude-sonnet-5-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	b.ResetTimer()
	for b.Loop() {
		req, err := http.NewRequest(http.MethodPost, url+"/v1/messages?beta=true", strings.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil || resp.StatusCode != http.StatusOK {
			b.Fatalf("HTTP %d, %v", resp.StatusCode, err)
		}
		_ = resp.Body.Close()
	}
}

func benchStandIn(b *testing.B) *httptest.Server {
	b.Helper()
	answer := gzippedBytes(b, benchFixture(b))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", sseType)
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write(answer)
	}))
	b.Cleanup(server.Close)
	return server
}

func BenchmarkAnthropicEngine_Direct(b *testing.B) {
	benchAnswer(b, benchStandIn(b).URL, map[string]string{"Authorization": callersLogin})
}

func BenchmarkAnthropicEngine_ThroughTheProxy(b *testing.B) {
	upstream := benchStandIn(b)
	writer := benchLedger(b)
	engine, err := WithAnthropic(upstream.URL)
	if err != nil {
		b.Fatal(err)
	}
	front := httptest.NewServer(NewHandler(nil, testKey, WithLedger(writer), engine))
	b.Cleanup(front.Close)
	benchAnswer(b, front.URL+"/k/"+testKey, map[string]string{"Authorization": callersLogin})
}
