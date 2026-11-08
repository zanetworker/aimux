package devicelogin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	me           = "me@example.com"
	testHostPort = testHost + ":8443"
)

var b64 = base64.StdEncoding

type rig struct {
	t     *testing.T
	dir   string
	store *Store
	clock *clock
	srv   *Server
	audit *lockedBuffer
	h     http.Handler
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, dir: t.TempDir(), clock: newClock(), audit: &lockedBuffer{}}
	r.store = Open(r.dir)
	r.store.now = r.clock.now
	srv, err := NewServer(Options{
		Store: r.store,
		Audit: NewAudit(r.audit, r.clock.now, nil),
		Now:   r.clock.now,
		Login: func(req *http.Request) string { return strings.ToLower(req.Header.Get("Tailscale-User-Login")) },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.srv = srv
	mux := http.NewServeMux()
	srv.Register(mux)
	ok := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }
	for _, p := range []string{"GET /v1/health", "GET /v1/openapi.yaml", "GET /app/", "GET /v1/sessions", "POST /v1/sessions/{id}/messages", "GET /v1/sessions/{id}/events"} {
		mux.HandleFunc(p, ok)
	}
	r.h = srv.Middleware(mux)
	return r
}

type reply struct {
	code   int
	body   map[string]any
	cookie string
}

// do sends a request as me on the test host; creds are "Bearer <t>" or a
// cookie value, headers extra "Name: value" pairs.
func (r *rig) do(method, path string, body any, creds string, headers ...string) reply {
	r.t.Helper()
	var s string
	switch b := body.(type) {
	case nil:
	case string:
		s = b
	default:
		j, _ := json.Marshal(b)
		s = string(j)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(s))
	req.Host = testHostPort
	req.Header.Set("Tailscale-User-Login", me)
	switch {
	case strings.HasPrefix(creds, "Bearer "):
		req.Header.Set("Authorization", creds)
	case creds != "":
		req.AddCookie(&http.Cookie{Name: cookieName, Value: creds})
	}
	for _, h := range headers {
		k, v, _ := strings.Cut(h, ": ")
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	out := reply{code: w.Code, cookie: w.Header().Get("Set-Cookie")}
	_ = json.Unmarshal(w.Body.Bytes(), &out.body)
	return out
}

func (r *rig) pending(name string) string {
	r.t.Helper()
	code, hash := NewCode()
	if err := r.store.SetPending(name, hash, r.clock.now().Add(5*time.Minute)); err != nil {
		r.t.Fatal(err)
	}
	return code
}

type enclave struct {
	key   *ecdsa.PrivateKey
	id    string
	token string
}

func (r *rig) enclavePairBody(k *ecdsa.PrivateKey, code string) map[string]any {
	canon, _ := CanonicalCode(code)
	return map[string]any{"code": code, "kind": KindSecureEnclave, "publicKey": b64.EncodeToString(spki(r.t, k)),
		"signature": b64.EncodeToString(sign(r.t, k, PairMessage(testHost, canon)))}
}

func (r *rig) pairEnclave(name string) enclave {
	r.t.Helper()
	k := newKey(r.t, elliptic.P256())
	got := r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, r.pending(name)), "")
	if got.code != 200 {
		r.t.Fatalf("pair: %d %v", got.code, got.body)
	}
	tok, _ := got.body["token"].(string)
	id, _ := got.body["deviceId"].(string)
	if tok == "" || id == "" || got.cookie != "" {
		r.t.Fatalf("iPhone pairing answer %v, cookie %q", got.body, got.cookie)
	}
	return enclave{key: k, id: id, token: tok}
}

func (r *rig) loginEnclave(e enclave) reply {
	r.t.Helper()
	ch := r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": e.id}, "")
	c, _ := ch.body["challenge"].(string)
	return r.do("POST", "/v1/auth/login", map[string]any{"deviceId": e.id, "challenge": c,
		"signature": b64.EncodeToString(sign(r.t, e.key, LoginMessage(testHost, c)))}, "")
}

type passkey struct {
	key    *ecdsa.PrivateKey
	id     string // user handle = device id
	credID string
	cookie string
}

func randB64URL(t *testing.T, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (r *rig) webPairBody(k *ecdsa.PrivateKey, code, id, credID string) map[string]any {
	canon, _ := CanonicalCode(code)
	h := sha256.Sum256([]byte(canon))
	w := goodWebauthn("webauthn.create", h[:])
	return map[string]any{"code": code, "kind": KindWebAuthn, "publicKey": b64.EncodeToString(spki(r.t, k)),
		"credentialId": credID, "userHandle": id,
		"clientDataJSON": b64.EncodeToString(w.clientData(r.t)), "authenticatorData": b64.EncodeToString(w.authData())}
}

func sessionCookie(t *testing.T, setCookie string) string {
	t.Helper()
	const want = "; Path=/; Secure; HttpOnly; SameSite=Strict"
	v, ok := strings.CutPrefix(setCookie, cookieName+"=")
	if !ok || !strings.HasSuffix(v, want) {
		t.Fatalf("Set-Cookie %q, want %s=<token>%s", setCookie, cookieName, want)
	}
	return strings.TrimSuffix(v, want)
}

func (r *rig) pairWeb(name string) passkey {
	r.t.Helper()
	p := passkey{key: newKey(r.t, elliptic.P256()), id: randB64URL(r.t, 16), credID: randB64URL(r.t, 20)}
	got := r.do("POST", "/v1/auth/pair", r.webPairBody(p.key, r.pending(name), p.id, p.credID), "")
	if got.code != 200 {
		r.t.Fatalf("web pair: %d %v", got.code, got.body)
	}
	if _, has := got.body["token"]; has {
		r.t.Fatalf("web pairing answer carries a token: %v", got.body)
	}
	if got.body["deviceId"] != p.id {
		r.t.Fatalf("deviceId %v, want the user handle %s", got.body["deviceId"], p.id)
	}
	p.cookie = sessionCookie(r.t, got.cookie)
	return p
}

func (r *rig) webLoginBody(p passkey, challenge string, withID bool) map[string]any {
	raw, _ := base64.RawURLEncoding.DecodeString(challenge)
	w := goodWebauthn("webauthn.get", raw)
	cd, ad := w.clientData(r.t), w.authData()
	h := sha256.Sum256(cd)
	body := map[string]any{"challenge": challenge, "credentialId": p.credID, "userHandle": p.id,
		"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad),
		"signature": b64.EncodeToString(sign(r.t, p.key, append(append([]byte{}, ad...), h[:]...)))}
	if withID {
		body["deviceId"] = p.id
	}
	return body
}

func TestHost_Canonical(t *testing.T) {
	for host, want := range map[string][2]string{
		"MY-MAC.tail1234.ts.net:8443": {"my-mac.tail1234.ts.net", "https://my-mac.tail1234.ts.net:8443"},
		"my-mac.tail1234.ts.net:443":  {"my-mac.tail1234.ts.net", "https://my-mac.tail1234.ts.net"},
		"my-mac.tail1234.ts.net":      {"my-mac.tail1234.ts.net", "https://my-mac.tail1234.ts.net"},
	} {
		name, origin := canonicalHost(host)
		if name != want[0] || origin != want[1] {
			t.Errorf("canonicalHost(%q) = %q, %q; want %q, %q", host, name, origin, want[0], want[1])
		}
	}
}

func TestPair_EnclaveLogsIn(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	if got := r.do("GET", "/v1/sessions", nil, "Bearer "+e.token); got.code != 200 {
		t.Fatalf("session from pairing refused: %d %v", got.code, got.body)
	}
	ds, _ := r.store.Devices()
	if len(ds) != 1 || ds[0].Name != "iphone" || ds[0].ID != e.id || ds[0].Login != me || ds[0].Kind != KindSecureEnclave {
		t.Fatalf("devices %+v", ds)
	}
	if b, err := base64.RawURLEncoding.Strict().DecodeString(e.id); err != nil || len(b) != 16 {
		t.Fatalf("device id %q is not 16 bytes of base64url", e.id)
	}
}

func TestPair_WebSetsCookie(t *testing.T) {
	r := newRig(t)
	p := r.pairWeb("safari")
	if got := r.do("GET", "/v1/sessions", nil, p.cookie); got.code != 200 {
		t.Fatalf("cookie from pairing refused: %d %v", got.code, got.body)
	}
	ds, _ := r.store.Devices()
	if len(ds) != 1 || ds[0].CredentialID != p.credID || ds[0].Kind != KindWebAuthn {
		t.Fatalf("devices %+v", ds)
	}
}

func TestPair_Refusals(t *testing.T) {
	r := newRig(t)
	k := newKey(t, elliptic.P256())

	noProof := r.enclavePairBody(k, r.pending("a"))
	delete(noProof, "signature")
	if got := r.do("POST", "/v1/auth/pair", noProof, ""); got.code != 401 || got.body["code"] != "pairing_failed" {
		t.Fatalf("no proof: %d %v", got.code, got.body)
	}
	otherKey := r.enclavePairBody(k, r.pending("a"))
	otherKey["publicKey"] = b64.EncodeToString(spki(t, newKey(t, elliptic.P256())))
	if got := r.do("POST", "/v1/auth/pair", otherKey, ""); got.code != 401 {
		t.Fatalf("proof by another key: %d %v", got.code, got.body)
	}
	// A webauthn.get client data sent to pairing is refused.
	code := r.pending("w")
	web := r.webPairBody(k, code, randB64URL(t, 16), randB64URL(t, 20))
	canon, _ := CanonicalCode(code)
	h := sha256.Sum256([]byte(canon))
	get := goodWebauthn("webauthn.get", h[:])
	web["clientDataJSON"] = b64.EncodeToString(get.clientData(t))
	if got := r.do("POST", "/v1/auth/pair", web, ""); got.code != 401 {
		t.Fatalf("get ceremony at pairing: %d %v", got.code, got.body)
	}
	shortID := r.webPairBody(k, r.pending("w"), randB64URL(t, 8), randB64URL(t, 20))
	if got := r.do("POST", "/v1/auth/pair", shortID, ""); got.code != 400 {
		t.Fatalf("8-byte user handle: %d %v", got.code, got.body)
	}

	// The proof failures above did not use up the code.
	code = r.pending("b")
	first := r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, code), "")
	if first.code != 200 {
		t.Fatalf("good pairing: %d %v", first.code, first.body)
	}
	if got := r.do("POST", "/v1/auth/pair", r.enclavePairBody(newKey(t, elliptic.P256()), code), ""); got.code != 401 {
		t.Fatalf("code used twice: %d %v", got.code, got.body)
	}
	if got := r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, "ABCD-123"), ""); got.code != 401 || got.body["code"] != "pairing_failed" {
		t.Fatalf("malformed code: %d %v", got.code, got.body)
	}
	if got := r.do("POST", "/v1/auth/pair", `{"code":"ABCD-1234","kind":"sms"}`, ""); got.code != 400 {
		t.Fatalf("unknown kind: %d %v", got.code, got.body)
	}
	if got := r.do("POST", "/v1/auth/pair", `not json`, ""); got.code != 400 || got.body["code"] != "bad_request" {
		t.Fatalf("not json: %d %v", got.code, got.body)
	}
}

func TestPair_BurnedAfterFiveWrongCodes(t *testing.T) {
	r := newRig(t)
	k := newKey(t, elliptic.P256())
	code := r.pending("a")
	for i := range 5 {
		wrong, _ := NewCode()
		if wrong == code {
			continue
		}
		if got := r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, wrong), ""); got.code != 401 {
			t.Fatalf("wrong code %d: %d %v", i, got.code, got.body)
		}
	}
	if got := r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, code), ""); got.code != 401 {
		t.Fatalf("burned code still pairs: %d %v", got.code, got.body)
	}
	// A malformed code is refused but not counted.
	code = r.pending("a")
	for range 6 {
		r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, "!!!!-!!!!"), "")
	}
	if got := r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, code), ""); got.code != 200 {
		t.Fatalf("malformed codes burned the code: %d %v", got.code, got.body)
	}
}

func TestPair_DuplicateName(t *testing.T) {
	r := newRig(t)
	code := r.pending("iphone")
	if err := r.store.Add(Device{ID: "x", Name: "iphone", Kind: KindSecureEnclave}); err != nil { // paired meanwhile
		t.Fatal(err)
	}
	got := r.do("POST", "/v1/auth/pair", r.enclavePairBody(newKey(t, elliptic.P256()), code), "")
	if got.code != 409 || got.body["code"] != "pairing_conflict" {
		t.Fatalf("duplicate name: %d %v", got.code, got.body)
	}
}

func TestPair_Expired(t *testing.T) {
	r := newRig(t)
	code := r.pending("a")
	r.clock.add(5 * time.Minute)
	if got := r.do("POST", "/v1/auth/pair", r.enclavePairBody(newKey(t, elliptic.P256()), code), ""); got.code != 401 {
		t.Fatalf("expired code: %d %v", got.code, got.body)
	}
}

func TestLogin_Enclave(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	got := r.loginEnclave(e)
	tok, _ := got.body["token"].(string)
	if got.code != 200 || tok == "" || tok == e.token || got.cookie != "" {
		t.Fatalf("login: %d %v cookie %q", got.code, got.body, got.cookie)
	}
	if got := r.do("GET", "/v1/sessions", nil, "Bearer "+tok); got.code != 200 {
		t.Fatalf("new session refused: %d", got.code)
	}
}

func TestLogin_WebWithAndWithoutDeviceID(t *testing.T) {
	r := newRig(t)
	p := r.pairWeb("safari")
	for _, withID := range []bool{true, false} {
		req := map[string]any{}
		if withID {
			req["deviceId"] = p.id
		}
		ch := r.do("POST", "/v1/auth/challenge", req, "")
		c, _ := ch.body["challenge"].(string)
		got := r.do("POST", "/v1/auth/login", r.webLoginBody(p, c, withID), "")
		if got.code != 200 {
			t.Fatalf("withID=%v: %d %v", withID, got.code, got.body)
		}
		if _, has := got.body["token"]; has {
			t.Fatalf("web login answer carries a token: %v", got.body)
		}
		if r.do("GET", "/v1/sessions", nil, sessionCookie(t, got.cookie)).code != 200 {
			t.Fatalf("withID=%v: cookie refused", withID)
		}
	}
	// Without deviceId, the user handle must match the credential's device.
	ch := r.do("POST", "/v1/auth/challenge", map[string]any{}, "")
	c, _ := ch.body["challenge"].(string)
	body := r.webLoginBody(p, c, false)
	body["userHandle"] = randB64URL(t, 16)
	if got := r.do("POST", "/v1/auth/login", body, ""); got.code != 404 || got.body["code"] != "unknown_device" {
		t.Fatalf("wrong user handle: %d %v", got.code, got.body)
	}
}

func TestLogin_ChallengeOnceAndFor60Seconds(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	ch := r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": e.id}, "")
	c, _ := ch.body["challenge"].(string)
	if raw, err := base64.RawURLEncoding.Strict().DecodeString(c); err != nil || len(raw) != 32 {
		t.Fatalf("challenge %q is not 32 bytes of base64url", c)
	}
	body := map[string]any{"deviceId": e.id, "challenge": c, "signature": b64.EncodeToString(sign(t, e.key, LoginMessage(testHost, c)))}
	if got := r.do("POST", "/v1/auth/login", body, ""); got.code != 200 {
		t.Fatalf("first use: %d %v", got.code, got.body)
	}
	if got := r.do("POST", "/v1/auth/login", body, ""); got.code != 401 || got.body["code"] != "login_failed" {
		t.Fatalf("replayed challenge: %d %v", got.code, got.body)
	}

	ch = r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": e.id}, "")
	c, _ = ch.body["challenge"].(string)
	body = map[string]any{"deviceId": e.id, "challenge": c, "signature": b64.EncodeToString(sign(t, e.key, LoginMessage(testHost, c)))}
	r.clock.add(60 * time.Second)
	if got := r.do("POST", "/v1/auth/login", body, ""); got.code != 401 {
		t.Fatalf("challenge after 60 s: %d %v", got.code, got.body)
	}
}

func TestLogin_SixthChallengeDropsTheOldest(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	var cs []string
	for range 6 {
		ch := r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": e.id}, "")
		if ch.code != 200 {
			t.Fatalf("challenge refused: %d", ch.code)
		}
		cs = append(cs, ch.body["challenge"].(string))
	}
	use := func(c string) int {
		return r.do("POST", "/v1/auth/login", map[string]any{"deviceId": e.id, "challenge": c,
			"signature": b64.EncodeToString(sign(t, e.key, LoginMessage(testHost, c)))}, "").code
	}
	if use(cs[0]) != 401 {
		t.Fatal("the oldest challenge survived a 6th")
	}
	for _, c := range cs[1:] {
		if use(c) != 200 {
			t.Fatal("a newer challenge was dropped")
		}
	}
}

func TestLogin_Refusals(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	if got := r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": "nope"}, ""); got.code != 404 || got.body["code"] != "unknown_device" {
		t.Fatalf("challenge for an unknown device: %d %v", got.code, got.body)
	}
	ch := r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": e.id}, "")
	c, _ := ch.body["challenge"].(string)
	// The pairing signature replayed as a login is refused.
	canonPairSig := b64.EncodeToString(sign(t, e.key, PairMessage(testHost, c)))
	if got := r.do("POST", "/v1/auth/login", map[string]any{"deviceId": e.id, "challenge": c, "signature": canonPairSig}, ""); got.code != 401 || got.body["code"] != "login_failed" {
		t.Fatalf("pair-message signature: %d %v", got.code, got.body)
	}
	// Another Tailscale login cannot log in as this device.
	ch = r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": e.id}, "")
	c, _ = ch.body["challenge"].(string)
	good := map[string]any{"deviceId": e.id, "challenge": c, "signature": b64.EncodeToString(sign(t, e.key, LoginMessage(testHost, c)))}
	if got := r.do("POST", "/v1/auth/login", good, "", "Tailscale-User-Login: other@example.com"); got.code != 401 {
		t.Fatalf("other login: %d %v", got.code, got.body)
	}
	if got := r.do("POST", "/v1/auth/login", `{"deviceId":`, ""); got.code != 400 {
		t.Fatalf("broken body: %d", got.code)
	}
}

// protected are routes the rig serves that need a session.
var protected = [][2]string{{"GET", "/v1/sessions"}, {"POST", "/v1/sessions/s1/messages"}, {"POST", "/v1/auth/logout"}, {"GET", "/v1/nowhere"}, {"GET", "/app/../v1/sessions"}}

func requireLoginEverywhere(t *testing.T, r *rig, creds string, why string) {
	t.Helper()
	for _, p := range protected {
		got := r.do(p[0], p[1], "{}", creds)
		if got.code != 401 || got.body["code"] != "login_required" {
			t.Errorf("%s: %s %s = %d %v, want 401 login_required", why, p[0], p[1], got.code, got.body)
		}
	}
}

func TestMiddleware_LoginRequired(t *testing.T) {
	r := newRig(t)
	requireLoginEverywhere(t, r, "", "no session")
	requireLoginEverywhere(t, r, "Bearer forged", "forged token")
	e := r.pairEnclave("iphone")
	for _, p := range protected[:2] {
		got := r.do(p[0], p[1], "{}", "Bearer "+e.token, "Tailscale-User-Login: other@example.com")
		if got.code != 401 || got.body["code"] != "login_required" {
			t.Errorf("other login %s %s: %d", p[0], p[1], got.code)
		}
	}
}

func TestMiddleware_OpenRoutes(t *testing.T) {
	r := newRig(t)
	for _, p := range [][2]string{{"GET", "/v1/health"}, {"GET", "/v1/openapi.yaml"}, {"GET", "/app/"}, {"HEAD", "/v1/health"}} {
		if got := r.do(p[0], p[1], nil, ""); got.code != 200 {
			t.Errorf("%s %s = %d, want open", p[0], p[1], got.code)
		}
	}
	for _, p := range []string{"/v1/auth/pair", "/v1/auth/challenge", "/v1/auth/login"} {
		if got := r.do("POST", p, "{}", ""); got.code == 401 && got.body["code"] == "login_required" {
			t.Errorf("POST %s needs a session", p)
		}
	}
	if got := r.do("POST", "/v1/health", "{}", ""); got.code != 401 {
		t.Errorf("POST /v1/health = %d: only GET is open", got.code)
	}
}

func TestMiddleware_IdleAndInteraction(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	auth := "Bearer " + e.token
	r.clock.add(50 * time.Minute)
	if r.do("GET", "/v1/sessions", nil, auth, "Aimux-Interaction: 1").code != 200 { // moves the clock
		t.Fatal("refused within the hour")
	}
	r.clock.add(50 * time.Minute)
	if r.do("GET", "/v1/sessions", nil, auth).code != 200 { // a poll: does not move it
		t.Fatal("interaction did not move the idle clock")
	}
	if r.do("GET", "/v1/sessions/s1/events", nil, auth, "Aimux-Interaction: 1").code != 200 { // a stream never counts
		t.Fatal("stream refused")
	}
	r.clock.add(9 * time.Minute)
	if r.do("POST", "/v1/sessions/s1/messages", "{}", auth).code != 200 { // 59 min: a POST moves it
		t.Fatal("POST refused within the hour")
	}
	r.clock.add(59 * time.Minute)
	if r.do("GET", "/v1/sessions", nil, auth).code != 200 {
		t.Fatal("POST did not move the idle clock")
	}
	r.clock.add(time.Minute)
	requireLoginEverywhere(t, r, auth, "idle an hour")
}

func TestMiddleware_PollsAloneExpire(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	for range 6 {
		r.clock.add(10 * time.Minute)
		r.do("GET", "/v1/sessions", nil, "Bearer "+e.token)
	}
	requireLoginEverywhere(t, r, "Bearer "+e.token, "only polls for an hour")
}

func TestMiddleware_AbsoluteLimit(t *testing.T) {
	r := newRig(t)
	p := r.pairWeb("safari")
	for range 14 {
		r.clock.add(50 * time.Minute)
		if r.do("POST", "/v1/sessions/s1/messages", "{}", p.cookie).code != 200 {
			t.Fatal("refused while in use")
		}
	}
	r.clock.add(20 * time.Minute)
	requireLoginEverywhere(t, r, p.cookie, "12 hours")
}

func TestMiddleware_RevokeByAnotherStore(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	keep := r.pairEnclave("ipad")
	done := r.srv.sessions.Done(e.token)
	if _, err := Open(r.dir).Remove("iphone"); err != nil { // the CLI, another process
		t.Fatal(err)
	}
	r.clock.add(2 * time.Second)
	requireLoginEverywhere(t, r, "Bearer "+e.token, "revoked")
	if !closed(done) {
		t.Fatal("revoke left the session's streams open")
	}
	if r.do("GET", "/v1/sessions", nil, "Bearer "+keep.token).code != 200 {
		t.Fatal("revoking one device ended another")
	}
	if got := r.loginEnclave(e); got.code != 404 {
		t.Fatalf("revoked device logged in: %d %v", got.code, got.body)
	}
}

func TestTick_NoticesRevokeAndExpiry(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	idle := r.pairEnclave("ipad")
	revoked, expired := r.srv.sessions.Done(e.token), r.srv.sessions.Done(idle.token)
	r.clock.add(30 * time.Minute)
	r.do("POST", "/v1/sessions/s1/messages", "{}", "Bearer "+e.token)
	if _, err := Open(r.dir).Remove("iphone"); err != nil {
		t.Fatal(err)
	}
	r.clock.add(30 * time.Minute)
	r.srv.Tick()
	if !closed(revoked) || !closed(expired) {
		t.Fatalf("Tick: revoked closed %v, idle closed %v", closed(revoked), closed(expired))
	}
}

func TestLogout(t *testing.T) {
	r := newRig(t)
	p := r.pairWeb("safari")
	done := r.srv.sessions.Done(p.cookie)
	got := r.do("POST", "/v1/auth/logout", "{}", p.cookie)
	if got.code != 200 || !strings.Contains(got.cookie, "Max-Age=0") {
		t.Fatalf("logout: %d %v cookie %q", got.code, got.body, got.cookie)
	}
	if !closed(done) {
		t.Fatal("logout left streams open")
	}
	requireLoginEverywhere(t, r, p.cookie, "logged out")
}

func TestSeen_AtMostOncePerMinute(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	r.do("GET", "/v1/sessions", nil, "Bearer "+e.token)
	first := r.store.LastSeen()[e.id]
	r.clock.add(30 * time.Second)
	r.do("GET", "/v1/sessions", nil, "Bearer "+e.token)
	if got := r.store.LastSeen()[e.id]; !got.Equal(first) {
		t.Fatalf("seen written twice in a minute: %v then %v", first, got)
	}
	r.clock.add(31 * time.Second)
	r.do("GET", "/v1/sessions", nil, "Bearer "+e.token)
	if got := r.store.LastSeen()[e.id]; !got.Equal(r.clock.now()) {
		t.Fatalf("seen %v after a minute, want %v", got, r.clock.now())
	}
}

func TestAudit_AuthLinesCarryNoSecrets(t *testing.T) {
	r := newRig(t)
	k := newKey(t, elliptic.P256())
	code := r.pending("iphone")
	wrong := "ZZZZ-ZZZZ"
	r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, wrong), "")
	pair := r.do("POST", "/v1/auth/pair", r.enclavePairBody(k, code), "")
	token, _ := pair.body["token"].(string)
	id, _ := pair.body["deviceId"].(string)
	e := enclave{key: k, id: id, token: token}
	ch := r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": id}, "")
	c, _ := ch.body["challenge"].(string)
	r.do("POST", "/v1/auth/login", map[string]any{"deviceId": id, "challenge": c, "signature": b64.EncodeToString([]byte("junk"))}, "")
	login := r.loginEnclave(e)
	tok2, _ := login.body["token"].(string)
	r.do("POST", "/v1/auth/logout", "{}", "Bearer "+tok2)
	if _, err := Open(r.dir).Remove("iphone"); err != nil {
		t.Fatal(err)
	}
	r.clock.add(2 * time.Second)
	r.srv.Tick()

	log := r.audit.String()
	var actions []string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if e.Login != me {
			t.Errorf("line %q lacks the login", line)
		}
		actions = append(actions, e.Action+":"+e.Device)
	}
	// revoke is written by `aimux remote revoke`, never by the server.
	want := []string{"pair_failed:", "pair:iphone", "login_failed:iphone", "login:iphone", "logout:iphone"}
	if strings.Join(actions, " ") != strings.Join(want, " ") {
		t.Fatalf("actions %v, want %v", actions, want)
	}
	canon, _ := CanonicalCode(code)
	pub := b64.EncodeToString(spki(t, k))
	for _, secret := range []string{code, canon, wrong, "ZZZZZZZZ", token, tok2, c, pub, pub[:40]} {
		if strings.Contains(log, secret) {
			t.Errorf("audit log holds %q:\n%s", secret, log)
		}
	}
}

// A refresh that read the devices before a pairing's Add must not swap that
// stale list in after it: the new device would vanish and its session end.
// Unserialized, the hooks force exactly that order: the tick reads while
// the pairing waits before Add, and swaps only after the pairing is done.
func TestRefresh_SerializedWithPair(t *testing.T) {
	r := newRig(t)
	atAdd, goAdd, pairDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.srv.beforeAdd = func() {
		if err := Open(r.dir).Add(Device{ID: "other", Name: "other", Kind: KindSecureEnclave}); err != nil {
			t.Error(err) // a change for the tick to read
		}
		r.clock.add(2 * time.Second)
		close(atAdd)
		<-goAdd
	}
	r.srv.afterRead = func() {
		select {
		case <-goAdd: // read after the pairing went on: nothing stale to hold
		default:
			<-pairDone // a read taken while the pairing waits before Add
		}
	}
	paired := make(chan enclave, 1)
	go func() { paired <- r.pairEnclave("iphone"); close(pairDone) }()
	<-atAdd
	ticked := make(chan struct{})
	go func() { r.srv.Tick(); close(ticked) }()
	time.Sleep(50 * time.Millisecond) // unserialized, the tick has read the stale list by now
	close(goAdd)
	var e enclave
	for _, wait := range []func() bool{
		func() bool { <-ticked; return true },
		func() bool { e = <-paired; return true },
	} {
		done := make(chan bool, 1)
		go func() { done <- wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the tick and the pairing deadlocked")
		}
	}
	if got := r.do("GET", "/v1/sessions", nil, "Bearer "+e.token); got.code != 200 {
		t.Fatalf("a refresh racing the pairing ended its session: %d %v", got.code, got.body)
	}
}

// A device id revoked and paired again (a passkey keeps its user handle)
// starts clean: sessions from before the revoke do not come back.
func TestPair_RepairedIDEndsOldSessions(t *testing.T) {
	r := newRig(t)
	p := r.pairWeb("safari")
	if _, err := Open(r.dir).Remove("safari"); err != nil { // not yet noticed by the server
		t.Fatal(err)
	}
	got := r.do("POST", "/v1/auth/pair", r.webPairBody(p.key, r.pending("safari"), p.id, p.credID), "")
	if got.code != 200 {
		t.Fatalf("pair again: %d %v", got.code, got.body)
	}
	if r.do("GET", "/v1/sessions", nil, p.cookie).code != 401 {
		t.Fatal("a session from before the revoke works again")
	}
	if r.do("GET", "/v1/sessions", nil, sessionCookie(t, got.cookie)).code != 200 {
		t.Fatal("the new session is refused")
	}
}

// A failed read of the devices file is retried on a later tick even though
// the file did not change (for example a permission fixed back).
func TestRefresh_RetriesAfterFailedRead(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	file := r.dir + "/" + devicesFile
	r.clock.add(2 * time.Second)
	r.srv.Tick() // takes in the pairing's own write
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err) // a change, so the next tick reads (and fails)
	}
	r.clock.add(2 * time.Second)
	r.srv.Tick()
	if r.do("GET", "/v1/sessions", nil, "Bearer "+e.token).code != 401 {
		t.Fatal("an unreadable devices file must admit nobody")
	}
	if err := os.Chmod(file, 0o600); err != nil { // same mtime and size
		t.Fatal(err)
	}
	r.clock.add(2 * time.Second)
	r.srv.Tick()
	if got := r.loginEnclave(e); got.code != 200 {
		t.Fatalf("the device stays locked out after the file is readable again: %d %v", got.code, got.body)
	}
}

func TestSessionState(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	req := func(token, login string) *http.Request {
		q := httptest.NewRequest("GET", "/v1/health", nil)
		q.Header.Set("Tailscale-User-Login", login)
		q.Header.Set(InteractionHeader, "1")
		if token != "" {
			q.Header.Set("Authorization", "Bearer "+token)
		}
		return q
	}
	if got := r.srv.SessionState(req("", me)); got != "none" {
		t.Fatalf("no token: %q", got)
	}
	if got := r.srv.SessionState(req(e.token, "other@example.com")); got != "none" {
		t.Fatalf("other login: %q", got)
	}
	r.clock.add(59 * time.Minute)
	if got := r.srv.SessionState(req(e.token, me)); got != "live" {
		t.Fatalf("live session: %q", got)
	}
	r.clock.add(time.Minute) // the check above did not count as use
	if got := r.srv.SessionState(req(e.token, me)); got != "none" {
		t.Fatalf("idle an hour: %q", got)
	}
}

func TestLogin_ChallengeOfAnotherDevice(t *testing.T) {
	r := newRig(t)
	a := r.pairEnclave("iphone")
	b := r.pairEnclave("ipad")
	ch := r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": a.id}, "")
	c, _ := ch.body["challenge"].(string)
	got := r.do("POST", "/v1/auth/login", map[string]any{"deviceId": b.id, "challenge": c,
		"signature": b64.EncodeToString(sign(t, b.key, LoginMessage(testHost, c)))}, "")
	if got.code != 401 || got.body["code"] != "login_failed" {
		t.Fatalf("A's challenge used by B: %d %v", got.code, got.body)
	}
	if !strings.Contains(r.audit.String(), `"result":"bad_challenge"`) {
		t.Fatalf("not audited as bad_challenge:\n%s", r.audit.String())
	}
}

func TestBearer_CaseInsensitive(t *testing.T) {
	r := newRig(t)
	e := r.pairEnclave("iphone")
	for _, scheme := range []string{"bearer", "BEARER", "Bearer"} {
		if got := r.do("GET", "/v1/sessions", nil, "", "Authorization: "+scheme+" "+e.token); got.code != 200 {
			t.Errorf("scheme %q: %d", scheme, got.code)
		}
	}
	if got := r.do("GET", "/v1/sessions", nil, "", "Authorization: Basic "+e.token); got.code != 401 {
		t.Errorf("Basic scheme accepted: %d", got.code)
	}
}

// A login refused by the signature check is audited with a fixed reason
// code, never the error's text, so two malformed authenticatorData of
// different lengths are one result and collapse into one line.
func TestLogin_FailureReasonIsAFixedCode(t *testing.T) {
	r := newRig(t)
	p := r.pairWeb("safari")
	for _, n := range []int{3, 5} {
		ch := r.do("POST", "/v1/auth/challenge", map[string]any{"deviceId": p.id}, "")
		body := r.webLoginBody(p, ch.body["challenge"].(string), true)
		body["authenticatorData"] = b64.EncodeToString(make([]byte, n))
		if got := r.do("POST", "/v1/auth/login", body, ""); got.code != 401 {
			t.Fatalf("short authenticatorData: %d %v", got.code, got.body)
		}
	}
	log := r.audit.String()
	if n := strings.Count(log, `"action":"login_failed"`); n != 1 {
		t.Fatalf("%d login_failed lines, want 1 (the second collapsed):\n%s", n, log)
	}
	if !strings.Contains(log, `"result":"bad_auth_data"`) || strings.Contains(log, "bytes") {
		t.Fatalf("result is not the fixed code:\n%s", log)
	}
}

// An unreadable devices file is retried every second; the log says so on
// the first failure and when the error changes, not on every retry, and
// again after the file was readable in between.
func TestRefresh_LogsAReadFailureOnce(t *testing.T) {
	r := newRig(t)
	logBuf := &lockedBuffer{}
	r.srv.log = logBuf
	r.pairEnclave("iphone")
	file := r.dir + "/" + devicesFile
	tick := func(n int) {
		for range n {
			r.clock.add(2 * time.Second)
			r.srv.Tick()
		}
	}
	tick(1) // takes in the pairing's own write
	lines := func() int { return strings.Count(logBuf.String(), "device login:") }
	if err := os.WriteFile(file, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	tick(5)
	if n := lines(); n != 1 {
		t.Fatalf("%d log lines after 5 failed reads, want 1:\n%s", n, logBuf.String())
	}
	if err := os.Chmod(file, 0); err != nil { // another error
		t.Fatal(err)
	}
	tick(3)
	if n := lines(); n != 2 {
		t.Fatalf("%d log lines after the error changed, want 2:\n%s", n, logBuf.String())
	}
	if err := os.WriteFile(file+".tmp", []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file+".tmp", file); err != nil { // readable again
		t.Fatal(err)
	}
	tick(1)
	if err := os.WriteFile(file, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	tick(2)
	if n := lines(); n != 3 {
		t.Fatalf("%d log lines after a recovery and a new failure, want 3:\n%s", n, logBuf.String())
	}
}
