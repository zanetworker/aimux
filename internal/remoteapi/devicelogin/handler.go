package devicelogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// Challenge rules: valid challengeTTL, single use, at most maxChallenges
// outstanding per device (the oldest is dropped, never refused).
const (
	challengeTTL  = 60 * time.Second
	maxChallenges = 5
	seenEvery     = time.Minute // how often last seen is written per device
	maxAuthBody   = 64 << 10
	deviceIDBytes = 16
)

// cookieName is the web client's session cookie. __Host- makes the browser
// refuse it unless Secure, Path=/ and no Domain.
const cookieName = "__Host-aimux_session"

const (
	setCookieFormat = cookieName + "=%s; Path=/; Secure; HttpOnly; SameSite=Strict"
	clearCookie     = cookieName + "=; Path=/; Max-Age=0; Secure; HttpOnly; SameSite=Strict"
)

// InteractionHeader marks a request as something the person did (opening a
// chat, pull to refresh): it moves the idle clock like a POST does.
const InteractionHeader = "Aimux-Interaction"

// Options are a Server's dependencies.
type Options struct {
	Store *Store
	Audit *Audit // may be nil
	// Login returns the caller's Tailscale login, already checked by the
	// outer tailnet middleware.
	Login func(*http.Request) string
	Now   func() time.Time // nil: time.Now
	Log   io.Writer        // device file errors; may be nil
}

// Server is device login for the remote API: the /v1/auth endpoints and the
// middleware that requires a live session on every other route.
type Server struct {
	store    *Store
	audit    *Audit
	login    func(*http.Request) string
	now      func() time.Time
	log      io.Writer
	sessions *Sessions

	// refreshMu serializes reading the devices file into devices with
	// pairing, so a read taken before a pairing's Add is never swapped in
	// after it.
	refreshMu sync.Mutex
	readErr   string // the last failed read's error, "" after a good one; under refreshMu

	mu         sync.Mutex
	devices    map[string]Device      // by id, as last read from the store
	challenges map[string][]challenge // by device id; "" for logins that name no device
	seen       map[string]time.Time   // last Store.Seen write per device

	// Test hooks: afterRead runs in reload between reading the devices and
	// using them, beforeAdd in pair once it holds refreshMu, before
	// Store.TakePending.
	afterRead, beforeAdd func()
}

type challenge struct {
	value   string
	expires time.Time
}

// NewServer reads the paired devices and returns a Server with no sessions.
func NewServer(o Options) (*Server, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Server{store: o.Store, audit: o.Audit, login: o.Login, now: o.Now, log: o.Log,
		sessions: NewSessions(o.Now), challenges: map[string][]challenge{}, seen: map[string]time.Time{}}
	ds, err := o.Store.Devices()
	if err != nil {
		return nil, err
	}
	s.devices = byID(ds)
	return s, nil
}

func byID(ds []Device) map[string]Device {
	m := make(map[string]Device, len(ds))
	for _, d := range ds {
		m[d.ID] = d
	}
	return m
}

// Register adds the /v1/auth endpoints to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/pair", s.pair)
	mux.HandleFunc("POST /v1/auth/challenge", s.challenge)
	mux.HandleFunc("POST /v1/auth/login", s.loginHandler)
	mux.HandleFunc("POST /v1/auth/logout", s.logout)
}

type sessionKey struct{}

type authed struct {
	Session
	device string // its name, for the audit log
}

// SessionFrom returns the session the middleware admitted the request with.
func SessionFrom(ctx context.Context) (Session, bool) {
	a, ok := ctx.Value(sessionKey{}).(authed)
	return a.Session, ok
}

// openRoute reports whether a request may go without a session: what the
// web client needs to load and log in. An unclean path is never open, so
// /app/../v1/sessions cannot slip through.
func openRoute(r *http.Request) bool {
	p := r.URL.Path
	clean := path.Clean(p)
	if strings.HasSuffix(p, "/") && clean != "/" {
		clean += "/"
	}
	if clean != p {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return p == "/v1/health" || p == "/v1/openapi.yaml" || p == "/app" || strings.HasPrefix(p, "/app/")
	case http.MethodPost:
		return p == "/v1/auth/pair" || p == "/v1/auth/challenge" || p == "/v1/auth/login"
	}
	return false
}

// interaction reports whether a request counts as use for the idle limit:
// any POST, PATCH or DELETE (each is something the person did), and a
// request marked by the client. An event stream never does;
// a one-page snapshot (?wait=0, the same rule the events handler uses) is an
// ordinary read.
func interaction(r *http.Request) bool {
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events") && r.URL.Query().Get("wait") != "0" {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	}
	return r.Header.Get(InteractionHeader) == "1"
}

// tokenFrom reads the session token: the iPhone's Bearer header, else the
// web client's cookie.
func tokenFrom(r *http.Request) string {
	if scheme, t, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(t)
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

var errLoginRequired = errors.New("log in with a paired device")

// Middleware admits a request to next only on an open route or with a live
// session of a still-paired device for the caller's Tailscale login; else
// 401 login_required.
func (s *Server) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.refresh()
		if openRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := s.sessions.Check(tokenFrom(r), s.login(r), interaction(r))
		var d Device
		if ok {
			s.mu.Lock()
			d, ok = s.devices[sess.DeviceID]
			s.mu.Unlock()
			if !ok {
				s.sessions.EndDevice(sess.DeviceID)
			}
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "login_required", errLoginRequired)
			return
		}
		s.markSeen(d.ID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, authed{sess, d.Name})))
	})
}

// Watch runs Tick every second until ctx ends, so streams end within a
// second or two of a revoke or a session passing its limit.
func (s *Server) Watch(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// Tick notices revoked devices, ends sessions past their limits and drops
// expired challenges.
func (s *Server) Tick() {
	s.refresh()
	s.sessions.Sweep()
	now := s.now()
	s.mu.Lock()
	for id, cs := range s.challenges {
		live := cs[:0]
		for _, c := range cs {
			if now.Before(c.expires) {
				live = append(live, c)
			}
		}
		if len(live) == 0 {
			delete(s.challenges, id)
		} else {
			s.challenges[id] = live
		}
	}
	s.mu.Unlock()
}

// refresh re-reads the devices when the file changed (Store.Changed looks
// at most once a second).
func (s *Server) refresh() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.store.Changed() {
		s.reloadLocked()
	}
}

// reloadLocked reads the devices and ends the sessions of those that are
// gone. Revokes are audited by `aimux remote revoke`, not here. An
// unreadable file fails closed (no device admitted) and is read again on a
// later refresh. Holds refreshMu.
func (s *Server) reloadLocked() {
	ds, err := s.store.Devices()
	if s.afterRead != nil {
		s.afterRead()
	}
	if err != nil {
		// Retried every second: logged on the first failure and when the
		// error changes, not on every retry.
		if msg := err.Error(); msg != s.readErr && s.log != nil {
			_, _ = fmt.Fprintf(s.log, "device login: %s; admitting no device until it is readable\n", msg)
		}
		s.readErr = err.Error()
		ds = nil
		s.store.Recheck()
	} else {
		s.readErr = ""
	}
	now := byID(ds)
	s.mu.Lock()
	old := s.devices
	s.devices = now
	for id := range old {
		if _, kept := now[id]; !kept {
			delete(s.challenges, id)
			delete(s.seen, id)
		}
	}
	s.mu.Unlock()
	for id := range old {
		if _, kept := now[id]; !kept {
			s.sessions.EndDevice(id)
		}
	}
}

func (s *Server) markSeen(id string) {
	now := s.now()
	s.mu.Lock()
	due := now.Sub(s.seen[id]) >= seenEvery
	if due {
		s.seen[id] = now
	}
	s.mu.Unlock()
	if due {
		s.store.Seen(id, now)
	}
}

// Audit is the server's audit log, for lines written outside a request
// (the chat queue's sends and pauses). Nil for a nil server.
func (s *Server) Audit() *Audit {
	if s == nil {
		return nil
	}
	return s.audit
}

// Record writes an audit line for an action on a request the middleware
// admitted; session is the aimux session acted on, if any.
func (s *Server) Record(r *http.Request, action, session, result string) {
	a, _ := r.Context().Value(sessionKey{}).(authed)
	s.audit.Write(AuditEntry{Login: s.login(r), Device: a.device, Action: action, Session: session, Result: result})
}

// canonicalHost returns the request's host name (lowercase, no port, no
// trailing dot), which is the WebAuthn rp id and what the iPhone signs, and
// the origin a browser page on it has: https, with the port unless 443.
// The Host is one the tailnet middleware already trusts.
func canonicalHost(host string) (name, origin string) {
	h, port := strings.ToLower(host), ""
	if hh, p, err := net.SplitHostPort(h); err == nil {
		h, port = hh, p
	}
	name = strings.TrimSuffix(h, ".")
	origin = "https://" + name
	if port != "" && port != "443" {
		origin += ":" + port
	}
	return name, origin
}

// authAnswer is what pairing and login return. The token is in the body
// for the iPhone only; the web client gets it as an HttpOnly cookie.
type authAnswer struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Token    string `json:"token,omitempty"`
}

// startSession logs d in and answers the request with the new session.
func (s *Server) startSession(w http.ResponseWriter, d Device, login string) {
	token := s.sessions.Create(d.ID, login)
	s.markSeen(d.ID)
	ans := authAnswer{DeviceID: d.ID, Name: d.Name}
	if d.Kind == KindWebAuthn {
		w.Header().Set("Set-Cookie", fmt.Sprintf(setCookieFormat, token))
	} else {
		ans.Token = token
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, ans)
}

var (
	errPairing     = errors.New("pairing failed: check the code, or run aimux remote pair again")
	errLoginFailed = errors.New("login failed")
	errNoDevice    = errors.New("this device is not paired; pair it again with aimux remote pair")
)

type pairRequest struct {
	Code              string `json:"code"`
	Kind              string `json:"kind"`
	PublicKey         string `json:"publicKey"`
	Signature         string `json:"signature,omitempty"`
	CredentialID      string `json:"credentialId,omitempty"`
	UserHandle        string `json:"userHandle,omitempty"`
	ClientDataJSON    string `json:"clientDataJSON,omitempty"`
	AuthenticatorData string `json:"authenticatorData,omitempty"`
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	login := s.login(r)
	failed := func(status int, code, reason string, err error) {
		s.audit.Write(AuditEntry{Login: login, Action: ActionPairFailed, Result: reason})
		writeError(w, status, code, err)
	}
	var req pairRequest
	if err := decode(w, r, &req); err != nil {
		failed(http.StatusBadRequest, "bad_request", "bad_request", errors.New("body must be JSON with code, kind and publicKey, at most 64 KiB"))
		return
	}
	code, err := CanonicalCode(req.Code)
	if err != nil { // refused, not counted: a typo is not a guess
		failed(http.StatusUnauthorized, "pairing_failed", "bad_code", errPairing)
		return
	}
	pubDER, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err == nil {
		_, err = ParseP256(pubDER) // P-256 only, which is alg -7 for a passkey
	}
	if err != nil {
		failed(http.StatusBadRequest, "bad_request", "bad_key", ErrBadKey)
		return
	}
	host, origin := canonicalHost(r.Host)
	codeHash := sha256.Sum256([]byte(code))
	d := Device{Kind: req.Kind, PublicKey: base64.StdEncoding.EncodeToString(pubDER), Login: login}
	// The proof is checked before the code is tried, so a bad proof does
	// not use up the owner's code.
	switch req.Kind {
	case KindSecureEnclave:
		sig, _ := base64.StdEncoding.DecodeString(req.Signature)
		if err := VerifyEnclave(pubDER, PairMessage(host, code), sig); err != nil {
			failed(http.StatusUnauthorized, "pairing_failed", "bad_proof", errPairing)
			return
		}
		d.ID = newDeviceID()
	case KindWebAuthn:
		// The passkey was made with user.id = the device id before this
		// request, so the browser chose it; the store refuses one in use.
		id, err := base64.RawURLEncoding.Strict().DecodeString(req.UserHandle)
		if err != nil || len(id) != deviceIDBytes || !validCredentialID(req.CredentialID) {
			failed(http.StatusBadRequest, "bad_request", "bad_request", errors.New("userHandle must be 16 bytes and credentialId 1 to 1023 bytes, both base64url"))
			return
		}
		cd, err1 := base64.StdEncoding.DecodeString(req.ClientDataJSON)
		ad, err2 := base64.StdEncoding.DecodeString(req.AuthenticatorData)
		if err1 != nil || err2 != nil || VerifyWebAuthnCreate(origin, host, codeHash[:], cd, ad) != nil {
			failed(http.StatusUnauthorized, "pairing_failed", "bad_proof", errPairing)
			return
		}
		d.ID, d.CredentialID = req.UserHandle, req.CredentialID
	default:
		failed(http.StatusBadRequest, "bad_request", "bad_request", fmt.Errorf("kind must be %s or %s", KindSecureEnclave, KindWebAuthn))
		return
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.beforeAdd != nil {
		s.beforeAdd()
	}
	name, ok, err := s.store.TakePending(codeHash, s.now())
	if err != nil {
		s.internal(w, "pairing", err)
		return
	}
	if !ok {
		failed(http.StatusUnauthorized, "pairing_failed", "wrong_code", errPairing)
		return
	}
	d.Name, d.Created = name, s.now().UTC()
	if err := s.store.Add(d); err != nil {
		failed(http.StatusConflict, "pairing_conflict", "conflict", errors.New("a device with this name or id is already paired; revoke it or pair under another name"))
		return
	}
	s.reloadLocked() // the file as it is now, with this device and any revoke since
	// A passkey keeps its user handle, so a revoked id can be paired again:
	// sessions from before the revoke must not come back with it.
	s.sessions.EndDevice(d.ID)
	s.audit.Write(AuditEntry{Login: login, Device: d.Name, Action: ActionPair, Result: "ok"})
	s.startSession(w, d, login)
}

func validCredentialID(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) > 0 && len(b) < 1024
}

func newDeviceID() string {
	b := make([]byte, deviceIDBytes)
	_, _ = rand.Read(b) // never fails (crypto/rand, Go 1.24+)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) challenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"deviceId"`
	}
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("body must be JSON, {} or with a deviceId"))
		return
	}
	s.refresh()
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails (crypto/rand, Go 1.24+)
	c := challenge{value: base64.RawURLEncoding.EncodeToString(b), expires: s.now().Add(challengeTTL)}
	s.mu.Lock()
	_, known := s.devices[req.DeviceID]
	if known || req.DeviceID == "" {
		cs := append(s.challenges[req.DeviceID], c)
		if len(cs) > maxChallenges {
			cs = cs[len(cs)-maxChallenges:]
		}
		s.challenges[req.DeviceID] = cs
	}
	s.mu.Unlock()
	if !known && req.DeviceID != "" {
		writeError(w, http.StatusNotFound, "unknown_device", errNoDevice)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"challenge": c.value})
}

// takeChallenge uses up value if it was issued under bucket, and reports
// whether it was still valid.
func (s *Server) takeChallenge(bucket, value string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.challenges[bucket]
	for i, c := range cs {
		if subtle.ConstantTimeCompare([]byte(c.value), []byte(value)) == 1 {
			s.challenges[bucket] = append(cs[:i:i], cs[i+1:]...)
			return now.Before(c.expires)
		}
	}
	return false
}

func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID          string `json:"deviceId"`
		Challenge         string `json:"challenge"`
		Signature         string `json:"signature"`
		CredentialID      string `json:"credentialId"`
		UserHandle        string `json:"userHandle"`
		ClientDataJSON    string `json:"clientDataJSON"`
		AuthenticatorData string `json:"authenticatorData"`
	}
	login := s.login(r)
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("body must be JSON with challenge and signature, at most 64 KiB"))
		return
	}
	s.refresh()
	d, found := s.findDevice(req.DeviceID, req.CredentialID, req.UserHandle)
	failed := func(status int, code, reason string, err error) {
		s.audit.Write(AuditEntry{Login: login, Device: d.Name, Action: ActionLoginFailed, Result: reason})
		writeError(w, status, code, err)
	}
	if !found {
		failed(http.StatusNotFound, "unknown_device", "unknown_device", errNoDevice)
		return
	}
	if d.Login != login {
		failed(http.StatusUnauthorized, "login_failed", "other_login", errLoginFailed)
		return
	}
	if !s.takeChallenge(req.DeviceID, req.Challenge) {
		failed(http.StatusUnauthorized, "login_failed", "bad_challenge", errLoginFailed)
		return
	}
	host, origin := canonicalHost(r.Host)
	pubDER, _ := base64.StdEncoding.DecodeString(d.PublicKey)
	sig, _ := base64.StdEncoding.DecodeString(req.Signature)
	var err error
	switch d.Kind {
	case KindSecureEnclave:
		err = VerifyEnclave(pubDER, LoginMessage(host, req.Challenge), sig)
	case KindWebAuthn:
		raw, _ := base64.RawURLEncoding.DecodeString(req.Challenge)
		cd, _ := base64.StdEncoding.DecodeString(req.ClientDataJSON)
		ad, _ := base64.StdEncoding.DecodeString(req.AuthenticatorData)
		pub, perr := ParseP256(pubDER)
		if err = perr; err == nil {
			err = VerifyWebAuthnGet(origin, host, raw, pub, cd, ad, sig)
		}
	default:
		err = fmt.Errorf("unknown device kind %q", d.Kind)
	}
	if err != nil {
		failed(http.StatusUnauthorized, "login_failed", reasonCode(err), errLoginFailed)
		return
	}
	s.audit.Write(AuditEntry{Login: login, Device: d.Name, Action: ActionLogin, Result: "ok"})
	s.startSession(w, d, login)
}

// findDevice finds the device by id, or a passkey by its credential id and
// the user handle it returned (a login after lost browser storage). With an
// id, a credential id or user handle that is sent must match it too.
func (s *Server) findDevice(id, credentialID, userHandle string) (Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		d, ok := s.devices[id]
		if ok && d.Kind == KindWebAuthn && ((credentialID != "" && credentialID != d.CredentialID) || (userHandle != "" && userHandle != d.ID)) {
			return Device{}, false
		}
		return d, ok
	}
	if credentialID == "" {
		return Device{}, false
	}
	d, ok := s.devices[userHandle]
	if !ok || d.Kind != KindWebAuthn || d.CredentialID != credentialID {
		return Device{}, false
	}
	return d, true
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.sessions.End(tokenFrom(r))
	s.Record(r, ActionLogout, "", "ok")
	if _, err := r.Cookie(cookieName); err == nil {
		w.Header().Set("Set-Cookie", clearCookie)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) internal(w http.ResponseWriter, what string, err error) {
	if s.log != nil {
		_, _ = fmt.Fprintf(s.log, "device login: %s: %v\n", what, err)
	}
	writeError(w, http.StatusInternalServerError, "internal", errors.New(what+" failed on the server; see its log"))
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers in the remote API's error shape. err is always a fixed
// text here, never request input.
func writeError(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": code})
}

// SessionState is "live" when the request carries a live session of a
// still-paired device for its Tailscale login, else "none". It never counts
// as use and names no device, so health can tell a lost login from a lost
// network.
func (s *Server) SessionState(r *http.Request) string {
	s.refresh()
	sess, ok := s.sessions.Check(tokenFrom(r), s.login(r), false)
	if ok {
		s.mu.Lock()
		_, ok = s.devices[sess.DeviceID]
		s.mu.Unlock()
	}
	if ok {
		return "live"
	}
	return "none"
}
