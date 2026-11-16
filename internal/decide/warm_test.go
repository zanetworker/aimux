package decide

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingKey is a key source that counts its reads and takes delay.
func countingKey(n *atomic.Int32, delay time.Duration) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		n.Add(1)
		select {
		case <-time.After(delay):
			return "k", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// A slow key read (a cold Keychain) has its own budget: only the model
// call counts against the decider's timeout, and the answer reports the
// two stages.
func TestDecider_KeyReadNotCountedAgainstTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, okReply) }))
	defer srv.Close()
	var reads atomic.Int32
	b := NewOpenRouter(countingKey(&reads, 300*time.Millisecond), "m", true)
	b.URL = srv.URL
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: 200 * time.Millisecond, Usage: noUsage}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if !a.Decided || a.Choice != "web" {
		t.Fatalf("answer %+v: a 300 ms key read must not use up the 200 ms decision budget", a)
	}
	if a.KeyMs < 300 || a.CallMs >= 200 || a.Ms < a.KeyMs {
		t.Fatalf("stages key=%d call=%d total=%d ms", a.KeyMs, a.CallMs, a.Ms)
	}
}

// A key read that hangs past its own budget is a key error, not a hang.
func TestDecider_KeyReadBudgetEnds(t *testing.T) {
	var reads atomic.Int32
	b := NewOpenRouter(countingKey(&reads, time.Hour), "m", true)
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, KeyTimeout: 50 * time.Millisecond, Usage: noUsage}
	start := time.Now()
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if a.Undecided != UndecidedKeyError || time.Since(start) > time.Second {
		t.Fatalf("answer %+v after %v: want key_error within the key budget", a, time.Since(start))
	}
}

// The key is read once and kept; a key OpenRouter rejects is read again.
func TestOpenRouter_KeyCachedUntilRejected(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if s := int(status.Load()); s != http.StatusOK {
			w.WriteHeader(s)
			return
		}
		_, _ = io.WriteString(w, okReply)
	}))
	defer srv.Close()
	var reads atomic.Int32
	b := NewOpenRouter(countingKey(&reads, 0), "m", true)
	b.URL = srv.URL
	ask := func() { _, _ = b.Decide(context.Background(), Request{State: "s", Question: testQuestion}) }
	ask()
	ask()
	if reads.Load() != 1 {
		t.Fatalf("%d key reads for two calls, want 1", reads.Load())
	}
	status.Store(http.StatusUnauthorized)
	ask()
	status.Store(http.StatusOK)
	ask()
	if reads.Load() != 2 {
		t.Fatalf("%d key reads, want the rejected key read again (2)", reads.Load())
	}
}

// Warm reads the key once and opens the connection with a HEAD on the
// API's host, which decides nothing and costs nothing; the first decision
// then reuses that connection and reads no key.
func TestDecider_WarmPrimesKeyAndConnection(t *testing.T) {
	var conns, heads, posts atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			heads.Add(1)
			if r.URL.Path != "/" {
				t.Errorf("warm-up HEAD on %q, want the host root", r.URL.Path)
			}
		case http.MethodPost:
			posts.Add(1)
			_, _ = io.WriteString(w, okReply)
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	var reads atomic.Int32
	b := NewOpenRouter(countingKey(&reads, 0), "m", true)
	b.URL = srv.URL + "/api/alpha/decisions"
	b.HTTP = &http.Client{Transport: &http.Transport{}}
	d := &Decider{Name: "t", Backends: []Backend{WithThreshold(b, 0.25)}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage}
	d.Warm(context.Background())
	if reads.Load() != 1 || heads.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("warm: %d key reads, %d HEADs, %d decisions; want 1, 1, 0", reads.Load(), heads.Load(), posts.Load())
	}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if !a.Decided || reads.Load() != 1 || conns.Load() != 1 {
		t.Fatalf("after warm: %+v, %d key reads, %d connections; want decided on the warm connection with no new read", a, reads.Load(), conns.Load())
	}
}

// A key OpenRouter rejects is read again from its source: after the owner
// replaces the key file, the next Ask sends the new key, without a restart
// (the built decider's key chain keeps no cache of its own).
func TestBuild_RejectedKeyRereadsTheSource(t *testing.T) {
	var gotAuth []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer new-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, okReply)
	}))
	defer srv.Close()
	keyFile := filepath.Join(t.TempDir(), "openrouter")
	if err := os.WriteFile(keyFile, []byte("old-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Build("t", DeciderConfig{Backends: []BackendConfig{{Kind: "openrouter", Model: "m"}}, Threshold: 0.25,
		Timeout: time.Second, Key: KeyConfig{File: keyFile}}, Deps{Usage: noUsage})
	if err != nil {
		t.Fatal(err)
	}
	d.Backends[0].(*OpenRouter).URL = srv.URL
	req := Request{State: "s", Question: testQuestion}
	if a := d.Ask(context.Background(), req); a.Decided {
		t.Fatalf("old key decided: %+v", a)
	}
	if err := os.WriteFile(keyFile, []byte("new-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := d.Ask(context.Background(), req); !a.Decided {
		t.Fatalf("after replacing the key file: %+v, auth headers sent %v", a, gotAuth)
	}
}
