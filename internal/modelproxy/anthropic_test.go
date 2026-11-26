package modelproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testProxyKey = "proxy-key-for-tests"

// seenRequest is what a stand-in for Anthropic received.
type seenRequest struct {
	Method, Path, Query, Host string
	Header                    http.Header
	Body                      []byte
}

// anthropicStandIn serves answer and records every request it gets.
func anthropicStandIn(t *testing.T, answer http.HandlerFunc) (*httptest.Server, func() []seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, seenRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Host, r.Header.Clone(), body})
		mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(server.Close)
	return server, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), seen...)
	}
}

// engineFront serves the engine as the proxy will, and reports each forward's result.
func engineFront(t *testing.T, upstream string) (*httptest.Server, <-chan forwardResult) {
	t.Helper()
	engine, err := newAnthropicEngine(upstream, testProxyKey)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan forwardResult, 8)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		results <- engine.forward(w, r, r.URL.Path, body)
	}))
	t.Cleanup(front.Close)
	return front, results
}

func waitResult(t *testing.T, results <-chan forwardResult) forwardResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the engine did not finish")
		return forwardResult{}
	}
}

func messagesRequest(t *testing.T, base, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v1/messages?beta=true", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer the-callers-login")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Anthropic-Beta", "one,two")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestNewAnthropicEngine_RefusesABadUpstream(t *testing.T) {
	for _, upstream := range []string{"", "api.anthropic.com", "ftp://api.anthropic.com", "https://", "https://user:pass@api.anthropic.com", "https://api.anthropic.com/v1"} {
		if _, err := newAnthropicEngine(upstream, testProxyKey); err == nil {
			t.Errorf("newAnthropicEngine(%q) = nil error", upstream)
		}
	}
	if _, err := newAnthropicEngine(AnthropicUpstream, testProxyKey); err != nil {
		t.Errorf("newAnthropicEngine(AnthropicUpstream) = %v", err)
	}
}

func TestAnthropicEngine_ForwardsUnchanged(t *testing.T) {
	answer := gzipped(t, anthropicFixture(t, "stream.sse"))
	upstream, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sseType)
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Request-Id", "req_placeholder")
		_, _ = w.Write(answer)
	})
	front, results := engineFront(t, upstream.URL)

	const body = `{"model":"claude-sonnet-5-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	req := messagesRequest(t, front.URL, body)
	req.Header.Set("X-Aimux-Key", testProxyKey)
	req.Header.Set("X-Api-Key", testProxyKey)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 || !bytes.Equal(got, answer) {
		t.Fatalf("client got status %d and %d bytes; want 200 and the upstream's %d bytes unchanged", resp.StatusCode, len(got), len(answer))
	}
	if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Content-Type") != sseType || resp.Header.Get("Request-Id") != "req_placeholder" {
		t.Errorf("client headers = %v", resp.Header)
	}
	requests := seen()
	if len(requests) != 1 {
		t.Fatalf("upstream got %d requests", len(requests))
	}
	sent := requests[0]
	if sent.Method != http.MethodPost || sent.Path != "/v1/messages" || sent.Query != "beta=true" || string(sent.Body) != body {
		t.Errorf("upstream got %s %s?%s with body %q", sent.Method, sent.Path, sent.Query, sent.Body)
	}
	if sent.Host != strings.TrimPrefix(upstream.URL, "http://") {
		t.Errorf("upstream Host = %q", sent.Host)
	}
	for header, want := range map[string]string{
		"Authorization": "Bearer the-callers-login", "Anthropic-Version": "2023-06-01", "Anthropic-Beta": "one,two",
		"Content-Type": "application/json", "Accept-Encoding": "gzip", "X-Aimux-Key": "", "X-Api-Key": "",
	} {
		if got := sent.Header.Get(header); got != want {
			t.Errorf("upstream header %s = %q, want %q", header, got, want)
		}
	}
	result := waitResult(t, results)
	want := forwardResult{Status: 200, Model: "claude-sonnet-5-5", Usage: wantAnthropicUsage, UsageKnown: true, Outcome: "ok"}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestAnthropicEngine_KeyHeaders(t *testing.T) {
	for name, tc := range map[string]struct {
		header, value, want string
	}{
		"a caller's own API key is forwarded":            {"X-Api-Key", "the-callers-api-key", "the-callers-api-key"},
		"the proxy key as an API key is removed":         {"X-Api-Key", testProxyKey, ""},
		"the proxy key as a bearer token is removed":     {"Authorization", "Bearer " + testProxyKey, ""},
		"a value with the key anywhere in it is removed": {"Referer", "http://127.0.0.1:7385/k/" + testProxyKey + "/v1/messages", ""},
		"a cookie holding the key is removed":            {"Cookie", "k=" + testProxyKey, ""},
		"an escaped key in an address is removed":        {"Referer", "http://127.0.0.1:7385/k/" + strings.ReplaceAll(testProxyKey, "-", "%2D") + "/v1/messages", ""},
		"a client that takes no compression gets none":   {"Accept-Encoding", "identity", "identity"},
		"a client that refuses gzip is not sent it":      {"Accept-Encoding", "br, gzip;q=0", "identity"},
		"gzip with a weight is still gzip":               {"Accept-Encoding", "br;q=1.0, GZIP;q=0.5", "gzip"},
		"the proxy key under another bearer spelling":    {"Authorization", "bearer  " + testProxyKey, ""},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
			front, results := engineFront(t, upstream.URL)
			req := messagesRequest(t, front.URL, `{}`)
			req.Header.Del("Authorization")
			req.Header.Set(tc.header, tc.value)
			resp, err := http.DefaultTransport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			waitResult(t, results)
			if got := seen()[0].Header.Get(tc.header); got != tc.want {
				t.Errorf("upstream %s = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestAnthropicEngine_RemovesTheProxyKeyFromEveryValue(t *testing.T) {
	upstream, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	front, results := engineFront(t, upstream.URL)
	req := messagesRequest(t, front.URL, `{}`)
	req.Header["Authorization"] = []string{"Bearer the-callers-login", "Bearer " + testProxyKey}
	req.Header["X-Api-Key"] = []string{"the-callers-api-key", testProxyKey}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	waitResult(t, results)
	sent := seen()[0].Header
	if got := sent.Values("Authorization"); len(got) != 1 || got[0] != "Bearer the-callers-login" {
		t.Errorf("upstream Authorization = %q", got)
	}
	if got := sent.Values("X-Api-Key"); len(got) != 1 || got[0] != "the-callers-api-key" {
		t.Errorf("upstream X-Api-Key = %q", got)
	}
}

func TestAnthropicEngine_KeepsThePathAsSent(t *testing.T) {
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, r.URL.EscapedPath()) //nolint:gosec // G705: a stand-in telling the test which path it got
	})
	engine, err := newAnthropicEngine(upstream.URL, testProxyKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/files/a%2Fb", "/v1/messages/", "/v1//messages", "/v1/messages"} {
		recorder := httptest.NewRecorder()
		engine.forward(recorder, httptest.NewRequest(http.MethodGet, "http://proxy.invalid/", nil), path, nil)
		if got := recorder.Body.String(); got != path {
			t.Errorf("upstream path = %q, want %q", got, path)
		}
	}
}

func TestAnthropicEngine_HeadIsNotABrokenBody(t *testing.T) {
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		// What a GET would carry; a HEAD sends the headers and no body.
		w.Header().Set("Content-Type", jsonType)
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", "120")
	})
	front, results := engineFront(t, upstream.URL)
	client := &http.Client{Timeout: 5 * time.Second}
	for range 2 { // twice: the connection must still be usable after the first
		resp, err := client.Head(front.URL + "/api/hello")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Length") != "120" {
			t.Errorf("client got %d, Content-Length %q", resp.StatusCode, resp.Header.Get("Content-Length"))
		}
		want := forwardResult{Status: 200, Outcome: "ok"}
		if result := waitResult(t, results); result != want {
			t.Errorf("result = %+v, want %+v", result, want)
		}
	}
}

func TestAnthropicEngine_BrokenGzipWithAPromisedLength(t *testing.T) {
	body := gzipped(t, anthropicFixture(t, "message.json"))
	body[len(body)-6] ^= 0xff // a wrong checksum
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", jsonType)
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	})
	front, results := engineFront(t, upstream.URL)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(messagesRequest(t, front.URL, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("the client was told a damaged answer ended cleanly")
	}
	if result := waitResult(t, results); result.Outcome != "stream_cut_short" {
		t.Errorf("result = %+v", result)
	}
}

func TestAnthropicEngine_StreamsAsItArrives(t *testing.T) {
	release := make(chan struct{})
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sseType)
		_, _ = fmt.Fprint(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write(anthropicFixture(t, "stream.sse"))
	})
	front, results := engineFront(t, upstream.URL)
	// A deadline on the request: without flushing, the headers never come and Do would wait for good.
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(messagesRequest(t, front.URL, `{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	first := make([]byte, 64)
	got := make(chan int, 1)
	go func() { n, _ := resp.Body.Read(first); got <- n }()
	select {
	case n := <-got:
		if !strings.HasPrefix(string(first[:n]), "event: ping") {
			t.Fatalf("first bytes = %q", first[:n])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first event did not reach the client before the second was sent")
	}
	close(release)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if result := waitResult(t, results); result.Outcome != "ok" || result.Usage != wantAnthropicUsage {
		t.Errorf("result = %+v", result)
	}
}

func TestAnthropicEngine_UpstreamError(t *testing.T) {
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", jsonType)
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(anthropicFixture(t, "error.json"))
	})
	front, results := engineFront(t, upstream.URL)
	resp, err := http.DefaultClient.Do(messagesRequest(t, front.URL, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "7" || !bytes.Equal(got, anthropicFixture(t, "error.json")) {
		t.Errorf("client got %d, Retry-After %q, body %q", resp.StatusCode, resp.Header.Get("Retry-After"), got)
	}
	want := forwardResult{Status: 429, Outcome: "upstream_error", Limit: limitShort}
	if result := waitResult(t, results); result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestAnthropicEngine_DoesNotFollowRedirects(t *testing.T) {
	elsewhere, seenElsewhere := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {})
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v1/messages", http.StatusTemporaryRedirect)
	})
	front, results := engineFront(t, upstream.URL)
	resp, err := http.DefaultTransport.RoundTrip(messagesRequest(t, front.URL, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || len(seenElsewhere()) != 0 {
		t.Errorf("client got %d and the other site got %d requests; the login must not follow a redirect", resp.StatusCode, len(seenElsewhere()))
	}
	if result := waitResult(t, results); result.Outcome != "upstream_error" {
		t.Errorf("result = %+v", result)
	}
}

func TestAnthropicEngine_UpstreamUnreachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	front, results := engineFront(t, "http://"+address)
	resp, err := http.DefaultClient.Do(messagesRequest(t, front.URL, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(got), `"api_error"`) {
		t.Errorf("client got %d %s", resp.StatusCode, got)
	}
	for _, leak := range []string{address, "127.0.0.1", "the-callers-login", testProxyKey} {
		if strings.Contains(string(got), leak) {
			t.Errorf("the error names %q: %s", leak, got)
		}
	}
	want := forwardResult{Status: http.StatusBadGateway, Outcome: "upstream_unreachable"}
	if result := waitResult(t, results); result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestAnthropicEngine_ClientGone(t *testing.T) {
	upstreamCancelled := make(chan struct{})
	upstream, _ := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sseType)
		_, _ = fmt.Fprint(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamCancelled)
	})
	front, results := engineFront(t, upstream.URL)
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := http.DefaultClient.Do(messagesRequest(t, front.URL, `{"stream":true}`).WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := resp.Body.Read(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-upstreamCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream request was not cancelled when the client left")
	}
	if result := waitResult(t, results); result.Outcome != "client_gone" || result.Status != 200 {
		t.Errorf("result = %+v", result)
	}
}

func TestAnthropicEngine_StreamCutShort(t *testing.T) {
	stream := anthropicFixture(t, "stream.sse")
	start := stream[:bytes.Index(stream, []byte("event: content_block_start"))]
	for name, cut := range map[string]http.HandlerFunc{
		"the connection drops": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", sseType)
			_, _ = w.Write(start)
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
		},
		"the stream ends without message_stop": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", sseType)
			w.(http.Flusher).Flush() // streamed, as Anthropic's are: no length is promised
			_, _ = w.Write(start)
		},
		"a stream with a promised length ends without message_stop": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", sseType)
			w.Header().Set("Content-Length", fmt.Sprint(len(start)))
			_, _ = w.Write(start)
		},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, _ := anthropicStandIn(t, cut)
			front, results := engineFront(t, upstream.URL)
			resp, err := http.DefaultClient.Do(messagesRequest(t, front.URL, `{"stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			got, readErr := io.ReadAll(resp.Body)
			if !bytes.Equal(got, start) {
				t.Errorf("client got %q", got)
			}
			if readErr == nil {
				t.Error("the client was told the answer ended cleanly; it should see the connection break")
			}
			result := waitResult(t, results)
			if result.Outcome != "stream_cut_short" || !result.UsageKnown || result.Usage.InputTokens != 1200 || result.Model != "claude-sonnet-5-5" {
				t.Errorf("result = %+v", result)
			}
		})
	}
}

func TestAnthropicEngine_NeverSendsElsewhere(t *testing.T) {
	other, seenOther := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {})
	upstream, seen := anthropicStandIn(t, func(w http.ResponseWriter, r *http.Request) {})
	engine, err := newAnthropicEngine(upstream.URL, testProxyKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{other.URL + "/v1/messages", "//" + strings.TrimPrefix(other.URL, "http://") + "/v1/messages", "v1/messages", "/v1/../../v1/messages", ""} {
		req := httptest.NewRequest(http.MethodPost, "http://proxy.invalid/v1/messages", strings.NewReader(`{}`))
		req.Host = strings.TrimPrefix(other.URL, "http://")
		req.Header.Set("X-Forwarded-Host", strings.TrimPrefix(other.URL, "http://"))
		req.Header.Set("Authorization", "Bearer the-callers-login")
		recorder := httptest.NewRecorder()
		engine.forward(recorder, req, path, []byte(`{}`))
	}
	if got := seenOther(); len(got) != 0 {
		t.Fatalf("another site received %d requests", len(got))
	}
	requests := seen()
	if len(requests) != 5 {
		t.Fatalf("upstream got %d requests, want 5", len(requests))
	}
	for _, sent := range requests {
		if sent.Host != strings.TrimPrefix(upstream.URL, "http://") || !strings.HasPrefix(sent.Path, "/") {
			t.Errorf("upstream got Host %q path %q", sent.Host, sent.Path)
		}
	}
}

func TestUnescapeWhatItCan(t *testing.T) {
	for in, want := range map[string]string{
		"plain": "plain", "a%2Fb": "a/b", "%6b%6B": "kk", "bad%ZZ%41": "bad%ZZA", "end%4": "end%4", "end%": "end%", "%41": "A", "": "",
	} {
		if got := unescapeWhatItCan(in); got != want {
			t.Errorf("unescapeWhatItCan(%q) = %q, want %q", in, got, want)
		}
	}
}
