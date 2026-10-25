package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type apnsCall struct {
	host, path, proto string
	header            http.Header
	body              map[string]any
}

// fakeAPNs is an HTTP/2 TLS server answering like Apple's: status per token.
type fakeAPNs struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []apnsCall
	status map[string]int // token -> status; default 200
	reason map[string]string
}

func newFakeAPNs(t *testing.T) *fakeAPNs {
	f := &fakeAPNs{status: map[string]int{}, reason: map[string]string{}}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		token := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.mu.Lock()
		f.calls = append(f.calls, apnsCall{host: r.Host, path: r.URL.Path, proto: r.Proto, header: r.Header.Clone(), body: body})
		code, reason := f.status[token], f.reason[token]
		f.mu.Unlock()
		if code != 0 && code != 200 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
		}
	}))
	f.srv.EnableHTTP2 = true
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func testKey(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newTestAPNs(t *testing.T, f *fakeAPNs, key *ecdsa.PrivateKey, devices ...Device) *APNs {
	store := &DeviceStore{Path: filepath.Join(t.TempDir(), "devices.json")}
	for _, d := range devices {
		if err := store.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	logins := []string{""}
	for _, d := range devices {
		logins = append(logins, d.Login)
	}
	return &APNs{
		Key: key, KeyID: "KEY123", TeamID: "TEAM456", Topic: "me.example.aimux", Devices: store, AllowedLogins: logins,
		Client: f.srv.Client(),
		hosts:  map[string]string{EnvSandbox: f.srv.URL + "/sandbox", EnvProduction: f.srv.URL + "/prod"},
	}
}

// verifyJWT checks an ES256 provider token the way Apple does.
func verifyJWT(t *testing.T, auth string, pub *ecdsa.PublicKey) (header, claims map[string]any) {
	t.Helper()
	tok, ok := strings.CutPrefix(auth, "bearer ")
	if !ok {
		t.Fatalf("authorization %q is not a bearer token", auth)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q has %d parts", tok, len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: %d bytes, %v", len(sig), err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, sum[:], r, s) {
		t.Fatal("the provider token's signature does not verify")
	}
	for i, dst := range []*map[string]any{&header, &claims} {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, dst); err != nil {
			t.Fatal(err)
		}
	}
	return header, claims
}

func TestAPNs_SendsASignedAlertToEachDeviceOnItsServer(t *testing.T) {
	f := newFakeAPNs(t)
	key := testKey(t)
	a := newTestAPNs(t, f, key, Device{Token: tokenA, Environment: EnvSandbox}, Device{Token: tokenB, Environment: EnvProduction})
	expires := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	err := a.Notify(context.Background(), Message{
		Title: "Approval needed", Body: "Fix the build: Bash", ApprovalID: "ap1", SessionID: "s1", Expires: expires,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("%d calls, want one per device", len(f.calls))
	}
	paths := map[string]bool{f.calls[0].path: true, f.calls[1].path: true}
	if !paths["/sandbox/3/device/"+tokenA] || !paths["/prod/3/device/"+tokenB] {
		t.Fatalf("paths %v: each device must go to its own environment", paths)
	}
	c := f.calls[0]
	if c.proto != "HTTP/2.0" {
		t.Errorf("protocol %s; APNs only speaks HTTP/2", c.proto)
	}
	h := c.header
	if h.Get("apns-topic") != "me.example.aimux" || h.Get("apns-push-type") != "alert" || h.Get("apns-priority") != "10" ||
		h.Get("apns-collapse-id") != "ap1" || h.Get("apns-expiration") != itoa(expires.Unix()) {
		t.Errorf("headers %v", h)
	}
	jh, claims := verifyJWT(t, h.Get("authorization"), &key.PublicKey)
	if jh["alg"] != "ES256" || jh["kid"] != "KEY123" || claims["iss"] != "TEAM456" {
		t.Errorf("token header %v claims %v", jh, claims)
	}
	if iat, _ := claims["iat"].(float64); time.Since(time.Unix(int64(iat), 0)) > time.Minute {
		t.Errorf("iat %v is not now", claims["iat"])
	}
	aps, _ := c.body["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	if alert["title"] != "Approval needed" || alert["body"] != "Fix the build: Bash" || aps["sound"] != "default" ||
		aps["interruption-level"] != "time-sensitive" || aps["category"] != "AIMUX_APPROVAL" || aps["thread-id"] != "s1" {
		t.Errorf("aps %v", aps)
	}
	if c.body["approvalId"] != "ap1" || c.body["sessionId"] != "s1" {
		t.Errorf("custom keys %v", c.body)
	}
}

func itoa(n int64) string { return big.NewInt(n).String() }

func TestAPNs_ReusesTheProviderTokenUntilItAges(t *testing.T) {
	f := newFakeAPNs(t)
	a := newTestAPNs(t, f, testKey(t), Device{Token: tokenA, Environment: EnvSandbox})
	now := time.Now()
	a.now = func() time.Time { return now }
	for range 2 {
		if err := a.Notify(context.Background(), Message{Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(45 * time.Minute) // Apple refuses tokens older than an hour
	if err := a.Notify(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	auth := func(i int) string { return f.calls[i].header.Get("authorization") }
	if auth(0) != auth(1) {
		t.Error("a new token for every push: Apple throttles that (TooManyProviderTokenUpdates)")
	}
	if auth(1) == auth(2) {
		t.Error("the token was not refreshed after 45 minutes")
	}
}

func TestAPNs_ForgetsDevicesAppleNoLongerKnows(t *testing.T) {
	f := newFakeAPNs(t)
	f.status[tokenA], f.reason[tokenA] = http.StatusGone, "Unregistered"
	f.status[tokenB], f.reason[tokenB] = http.StatusBadRequest, "BadDeviceToken"
	third := strings.Repeat("ab", 32)
	f.status[third], f.reason[third] = http.StatusForbidden, "InvalidProviderToken"
	a := newTestAPNs(t, f, testKey(t),
		Device{Token: tokenA, Environment: EnvSandbox}, Device{Token: tokenB, Environment: EnvSandbox}, Device{Token: third, Environment: EnvSandbox})
	err := a.Notify(context.Background(), Message{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "InvalidProviderToken") {
		t.Fatalf("got %v, want the configuration error reported", err)
	}
	if strings.Contains(err.Error(), "Unregistered") {
		t.Errorf("a stale device is routine, not an error: %v", err)
	}
	ds, _ := a.Devices.List()
	if len(ds) != 1 || ds[0].Token != third {
		t.Fatalf("devices left %+v: only the stale ones go; a key problem must not wipe devices", ds)
	}
}

func TestAPNs_NoDevicesSendsNothing(t *testing.T) {
	f := newFakeAPNs(t)
	a := newTestAPNs(t, f, testKey(t))
	if err := a.Notify(context.Background(), Message{Title: "t"}); err != nil || len(f.calls) != 0 {
		t.Fatalf("%v, %d calls", err, len(f.calls))
	}
}

func TestLoadAPNsKey(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "AuthKey_KEY123.p8")
	if err := os.WriteFile(good, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAPNsKey(good)
	if err != nil || !got.Equal(key) {
		t.Fatalf("LoadAPNsKey: %v", err)
	}
	notPEM := filepath.Join(dir, "x.p8")
	_ = os.WriteFile(notPEM, []byte("hello"), 0o600)
	for _, p := range []string{notPEM, filepath.Join(dir, "missing.p8")} {
		if _, err := LoadAPNsKey(p); err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("LoadAPNsKey(%s) = %v, want an error naming the file", p, err)
		}
	}
}

func TestLoadAPNsKey_RejectsOtherCurves(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	path := filepath.Join(t.TempDir(), "p384.p8")
	_ = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	if _, err := LoadAPNsKey(path); err == nil || !strings.Contains(err.Error(), "P-256") {
		t.Fatalf("got %v; a P-384 key would panic when signing, so it must be refused at startup", err)
	}
}

func TestAPNs_AStalledDeviceDoesNotHoldUpTheOthers(t *testing.T) {
	f := newFakeAPNs(t)
	stall := make(chan struct{})
	defer close(stall)
	reachedB := make(chan struct{}, 1)
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, tokenA):
			<-stall
		case strings.HasSuffix(r.URL.Path, tokenB):
			reachedB <- struct{}{}
		}
		inner.ServeHTTP(w, r)
	})
	a := newTestAPNs(t, f, testKey(t), Device{Token: tokenA, Environment: EnvSandbox}, Device{Token: tokenB, Environment: EnvSandbox})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = a.Notify(ctx, Message{Title: "t"}) }()
	select {
	case <-reachedB:
	case <-time.After(2 * time.Second):
		t.Fatal("the second phone waited for the stalled first one")
	}
}

func TestAPNs_SendsOnlyToDevicesOfLoginsStillAllowed(t *testing.T) {
	f := newFakeAPNs(t)
	a := newTestAPNs(t, f, testKey(t),
		Device{Token: tokenA, Environment: EnvSandbox, Login: "me@example.com"},
		Device{Token: tokenB, Environment: EnvSandbox, Login: "revoked@example.com"},
		Device{Token: strings.Repeat("cd", 32), Environment: EnvSandbox}) // registered by nobody known
	a.AllowedLogins = []string{"me@example.com"}
	if err := a.Notify(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !strings.HasSuffix(f.calls[0].path, tokenA) {
		t.Fatalf("calls %d (%v): a login removed from allowed_logins must stop getting alerts", len(f.calls), f.calls)
	}
}
