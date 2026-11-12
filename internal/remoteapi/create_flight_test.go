package remoteapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/profilerun"
)

// post sends body as client from any goroutine (no t.Fatal).
func (r *profileRig) post(client, body string) (int, map[string]any, error) {
	req, _ := http.NewRequest("POST", r.srv.URL+"/v1/sessions", strings.NewReader(body))
	req.Header.Set("Tailscale-User-Login", testLogin)
	req.Header.Set("X-Test-Client", client)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

// slowEngineRig is a profile rig whose engine creation blocks until
// release is closed; entered counts creations in progress.
func slowEngineRig(t *testing.T) (r *profileRig, entered chan string, release chan struct{}, calls *atomic.Int32) {
	r = newProfileRig(t)
	entered, release, calls = make(chan string, 8), make(chan struct{}), &atomic.Int32{}
	setClientOf(r.api, func(req *http.Request) string { return req.Header.Get("X-Test-Client") })
	r.api.NewProfileSession = func(_ context.Context, m profilerun.Meta, _ string) (string, error) {
		n := calls.Add(1)
		entered <- m.URL
		<-release
		return fmt.Sprintf("slow-%d", n), nil
	}
	r.serve()
	return r, entered, release, calls
}

// A slow profile session creation (sandbox Ensure can take minutes) must
// not hold up the phone's legacy POST /v1/sessions.
func TestCreate_SlowEngineDoesNotDelayLegacyPost(t *testing.T) {
	r, entered, release, _ := slowEngineRig(t)
	defer close(release)
	go func() { _, _, _ = r.post("wingman", `{"profile":"page","url":"https://a.example/x","text":"q"}`) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("profile creation never started")
	}
	done := make(chan int, 1)
	go func() {
		code, _, _ := r.post("device", `{"cwd":"/w/app","text":"hi"}`)
		done <- code
	}()
	select {
	case code := <-done:
		if code != 202 {
			t.Fatalf("legacy POST = %d, want 202", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("legacy POST waited behind a slow profile session creation")
	}
}

// Requests with the same key wait for the one creation in flight and get
// its session; different pages never wait on each other.
func TestCreate_SameKeySharesOneCreationOthersRunInParallel(t *testing.T) {
	r, entered, release, calls := slowEngineRig(t)
	var wg sync.WaitGroup
	ids := make([]string, 2)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out, _ := r.post("wingman", `{"profile":"page","url":"https://a.example/x","text":"q","key":"k1"}`)
			ids[i], _ = out["id"].(string)
		}()
	}
	<-entered
	// cli: wingman's two session slots are held by the requests above.
	go func() { _, _, _ = r.post("cli", `{"profile":"page","url":"https://b.example/y","text":"q"}`) }()
	select {
	case u := <-entered:
		if u != "https://b.example/y" {
			t.Fatalf("second creation for %s, want the other page", u)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("a creation for another page waited on the first")
	}
	close(release)
	wg.Wait()
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("same key got sessions %q", ids)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d engine creations, want 2 (one per page)", n)
	}
}
