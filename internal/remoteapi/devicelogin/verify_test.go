package devicelogin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
)

const (
	testHost   = "my-mac.tail1234.ts.net"
	testOrigin = "https://my-mac.tail1234.ts.net:8443"
)

func newKey(t *testing.T, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func spki(t *testing.T, k *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func sign(t *testing.T, k *ecdsa.PrivateKey, data []byte) []byte {
	t.Helper()
	h := sha256.Sum256(data)
	sig, err := ecdsa.SignASN1(rand.Reader, k, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestMessages_ExactBytes(t *testing.T) {
	if got, want := string(PairMessage(testHost, "ABCD1234")), "aimux-pair-v1\nmy-mac.tail1234.ts.net\nABCD1234"; got != want {
		t.Fatalf("PairMessage = %q, want %q", got, want)
	}
	if got, want := string(LoginMessage(testHost, "Y2hhbGxlbmdl")), "aimux-login-v1\nmy-mac.tail1234.ts.net\nY2hhbGxlbmdl"; got != want {
		t.Fatalf("LoginMessage = %q, want %q", got, want)
	}
}

func TestParseP256(t *testing.T) {
	k := newKey(t, elliptic.P256())
	pub, err := ParseP256(spki(t, k))
	if err != nil {
		t.Fatalf("P-256 key: %v", err)
	}
	if !pub.Equal(&k.PublicKey) {
		t.Fatal("parsed key differs from the generated one")
	}
	if _, err := ParseP256(spki(t, newKey(t, elliptic.P384()))); !errors.Is(err, ErrBadKey) {
		t.Fatalf("P-384 key: %v, want ErrBadKey", err)
	}
	for name, der := range map[string][]byte{"empty": nil, "garbage": []byte("not a key"), "trailing": append(spki(t, k), 0)} {
		if _, err := ParseP256(der); !errors.Is(err, ErrBadKey) {
			t.Fatalf("%s: %v, want ErrBadKey", name, err)
		}
	}
}

func TestVerifyEnclave(t *testing.T) {
	k := newKey(t, elliptic.P256())
	pub := spki(t, k)
	msg := LoginMessage(testHost, "c2VydmVyLWNoYWxsZW5nZQ")
	sig := sign(t, k, msg)

	if err := VerifyEnclave(pub, msg, sig); err != nil {
		t.Fatalf("good signature: %v", err)
	}

	// P-256 ECDSA has no low-S rule here (unlike Bitcoin): a high-S twin of a
	// valid signature is also valid, and accepting it is fine because the
	// signature is never used as an identifier.
	var rs struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(sig, &rs); err != nil {
		t.Fatal(err)
	}
	rs.S.Sub(elliptic.P256().Params().N, rs.S)
	highS, err := asn1.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEnclave(pub, msg, highS); err != nil {
		t.Fatalf("high-S twin: %v, want accepted", err)
	}

	pairSig := sign(t, k, PairMessage(testHost, "ABCD1234"))
	cases := []struct {
		name          string
		pub, msg, sig []byte
		want          error
	}{
		{"wrong message", pub, LoginMessage(testHost, "b3RoZXI"), sig, ErrBadSignature},
		{"wrong key", spki(t, newKey(t, elliptic.P256())), msg, sig, ErrBadSignature},
		{"P-384 key", spki(t, newKey(t, elliptic.P384())), msg, sig, ErrBadKey},
		{"garbage key", []byte{0x30, 0x01}, msg, sig, ErrBadKey},
		{"garbage signature", pub, msg, []byte("junk"), ErrBadSignature},
		{"empty signature", pub, msg, nil, ErrBadSignature},
		{"trailing byte after DER", pub, msg, append(append([]byte{}, sig...), 0), ErrBadSignature},
		{"pairing signature used as login", pub, LoginMessage(testHost, "ABCD1234"), pairSig, ErrBadSignature},
		{"other host", pub, LoginMessage("other.tail1234.ts.net", "c2VydmVyLWNoYWxsZW5nZQ"), sig, ErrBadSignature},
	}
	for _, c := range cases {
		if err := VerifyEnclave(c.pub, c.msg, c.sig); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

// webauthn builds what a browser would hand back, so each test can bend one
// field at a time.
type webauthn struct {
	typ, origin, rpID string
	challenge         []byte
	crossOrigin       *bool
	flags             byte
	extra             []byte // attested credential data on create
}

func goodWebauthn(typ string, challenge []byte) webauthn {
	w := webauthn{typ: typ, origin: testOrigin, rpID: testHost, challenge: challenge, flags: flagUP | flagUV}
	if typ == "webauthn.create" {
		w.flags |= flagAT
		w.extra = make([]byte, 60) // aaguid, length, id, key: not inspected
	}
	return w
}

func (w webauthn) clientData(t *testing.T) []byte {
	t.Helper()
	m := map[string]any{"type": w.typ, "challenge": base64.RawURLEncoding.EncodeToString(w.challenge), "origin": w.origin}
	if w.crossOrigin != nil {
		m["crossOrigin"] = *w.crossOrigin
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (w webauthn) authData() []byte {
	h := sha256.Sum256([]byte(w.rpID))
	b := append(h[:], w.flags, 0, 0, 0, 7) // counter 7: ignored
	return append(b, w.extra...)
}

func randomChallenge(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func oneByteOff(b []byte) []byte {
	c := append([]byte{}, b...)
	c[len(c)-1] ^= 1
	return c
}

// bends are the clientData and authenticatorData variations both create and
// get must refuse.
func bends(t *testing.T, good webauthn) []struct {
	name string
	w    webauthn
	want error
} {
	t.Helper()
	tr := true
	wrongType := map[string]string{"webauthn.get": "webauthn.create", "webauthn.create": "webauthn.get"}[good.typ]
	with := func(f func(*webauthn)) webauthn { w := good; f(&w); return w }
	return []struct {
		name string
		w    webauthn
		want error
	}{
		{"type " + wrongType, with(func(w *webauthn) { w.typ = wrongType }), ErrWrongType},
		{"other origin", with(func(w *webauthn) { w.origin = "https://evil.tail1234.ts.net:8443" }), ErrWrongOrigin},
		{"other port", with(func(w *webauthn) { w.origin = "https://my-mac.tail1234.ts.net:9443" }), ErrWrongOrigin},
		{"http origin", with(func(w *webauthn) { w.origin = "http://my-mac.tail1234.ts.net:8443" }), ErrWrongOrigin},
		{"crossOrigin true", with(func(w *webauthn) { w.crossOrigin = &tr }), ErrCrossOrigin},
		{"wrong challenge", with(func(w *webauthn) { w.challenge = randomChallenge(t) }), ErrWrongChallenge},
		{"challenge one byte off", with(func(w *webauthn) { w.challenge = oneByteOff(w.challenge) }), ErrWrongChallenge},
		{"empty challenge", with(func(w *webauthn) { w.challenge = nil }), ErrWrongChallenge},
		{"rpIdHash of another host", with(func(w *webauthn) { w.rpID = "evil.tail1234.ts.net" }), ErrWrongRPID},
		{"rpIdHash with port", with(func(w *webauthn) { w.rpID = testHost + ":8443" }), ErrWrongRPID},
		{"UP cleared", with(func(w *webauthn) { w.flags &^= flagUP }), ErrUserNotPresent},
		{"UV cleared", with(func(w *webauthn) { w.flags &^= flagUV }), ErrUserNotVerified},
	}
}

func TestVerifyWebAuthnGet(t *testing.T) {
	k := newKey(t, elliptic.P256())
	challenge := randomChallenge(t)
	assert := func(w webauthn) ([]byte, []byte, []byte) {
		cd, ad := w.clientData(t), w.authData()
		h := sha256.Sum256(cd)
		return cd, ad, sign(t, k, append(append([]byte{}, ad...), h[:]...))
	}

	good := goodWebauthn("webauthn.get", challenge)
	cd, ad, sig := assert(good)
	if err := VerifyWebAuthnGet(testOrigin, testHost, challenge, &k.PublicKey, cd, ad, sig); err != nil {
		t.Fatalf("well-formed assertion: %v", err)
	}
	fa := false
	good2 := good
	good2.crossOrigin = &fa
	cd2, ad2, sig2 := assert(good2)
	if err := VerifyWebAuthnGet(testOrigin, testHost, challenge, &k.PublicKey, cd2, ad2, sig2); err != nil {
		t.Fatalf("crossOrigin false: %v", err)
	}

	for _, c := range bends(t, good) {
		cd, ad, sig := assert(c.w) // validly signed, so only the check under test can refuse it
		if err := VerifyWebAuthnGet(testOrigin, testHost, challenge, &k.PublicKey, cd, ad, sig); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}

	noHash := sign(t, k, ad) // over authenticatorData alone
	other := newKey(t, elliptic.P256())
	shortAD := ad[:36]
	h := sha256.Sum256(cd)
	shortSig := sign(t, k, append(append([]byte{}, shortAD...), h[:]...))
	cases := []struct {
		name        string
		pub         *ecdsa.PublicKey
		cd, ad, sig []byte
		want        error
	}{
		{"signature without the clientData hash", &k.PublicKey, cd, ad, noHash, ErrBadSignature},
		{"other key", &other.PublicKey, cd, ad, sig, ErrBadSignature},
		{"nil key", nil, cd, ad, sig, ErrBadKey},
		{"trailing byte after DER", &k.PublicKey, cd, ad, append(append([]byte{}, sig...), 0), ErrBadSignature},
		{"clientData changed after signing", &k.PublicKey, append(append([]byte{}, cd[:len(cd)-1]...), ' ', '}'), ad, sig, ErrBadSignature},
		{"authenticatorData shorter than 37 bytes", &k.PublicKey, cd, shortAD, shortSig, ErrAuthData},
		{"malformed clientData", &k.PublicKey, []byte(`{"type":`), ad, sig, ErrClientData},
		{"clientData not an object", &k.PublicKey, []byte(`"webauthn.get"`), ad, sig, ErrClientData},
		{"challenge with padding", &k.PublicKey, []byte(`{"type":"webauthn.get","challenge":"` + base64.URLEncoding.EncodeToString(challenge) + `","origin":"` + testOrigin + `"}`), ad, sig, ErrWrongChallenge},
	}
	for _, c := range cases {
		if err := VerifyWebAuthnGet(testOrigin, testHost, challenge, c.pub, c.cd, c.ad, c.sig); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func TestVerifyWebAuthnCreate(t *testing.T) {
	code := sha256.Sum256([]byte("ABCD1234"))
	challenge := code[:]
	good := goodWebauthn("webauthn.create", challenge)
	if err := VerifyWebAuthnCreate(testOrigin, testHost, challenge, good.clientData(t), good.authData()); err != nil {
		t.Fatalf("well-formed creation: %v", err)
	}
	for _, c := range bends(t, good) {
		if err := VerifyWebAuthnCreate(testOrigin, testHost, challenge, c.w.clientData(t), c.w.authData()); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	noAT := good
	noAT.flags &^= flagAT
	if err := VerifyWebAuthnCreate(testOrigin, testHost, challenge, noAT.clientData(t), noAT.authData()); !errors.Is(err, ErrAuthData) {
		t.Errorf("AT cleared: %v, want ErrAuthData", err)
	}
	if err := VerifyWebAuthnCreate(testOrigin, testHost, challenge, good.clientData(t), good.authData()[:36]); !errors.Is(err, ErrAuthData) {
		t.Errorf("authenticatorData shorter than 37 bytes: %v, want ErrAuthData", err)
	}
	if err := VerifyWebAuthnCreate(testOrigin, testHost, challenge, []byte("{"), good.authData()); !errors.Is(err, ErrClientData) {
		t.Errorf("malformed clientData: %v, want ErrClientData", err)
	}
}

// Errors are audited, so they must never carry what was signed or sent.
func TestErrors_CarryNoSecrets(t *testing.T) {
	k := newKey(t, elliptic.P256())
	challenge := randomChallenge(t)
	enc := base64.RawURLEncoding.EncodeToString(challenge)
	bad := goodWebauthn("webauthn.get", oneByteOff(challenge))
	cd := bad.clientData(t)
	err := VerifyWebAuthnGet(testOrigin, testHost, challenge, &k.PublicKey, cd, bad.authData(), []byte("sig"))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, secret := range []string{enc, base64.RawURLEncoding.EncodeToString(bad.challenge), string(cd)} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaks %q", err, secret)
		}
	}
	err = VerifyEnclave(spki(t, k), LoginMessage(testHost, enc), []byte("junk"))
	if err == nil || strings.Contains(err.Error(), enc) {
		t.Fatalf("enclave error %v leaks the challenge", err)
	}
}

// Every way a check fails maps to one fixed audit reason, so the audit's
// result values are bounded and repeats collapse; wrapped variants of one
// sentinel (ErrAuthData with different byte counts) share it.
func TestReasonCode_Bounded(t *testing.T) {
	short := checkAuthData(make([]byte, 3), "h")
	shorter := checkAuthData(make([]byte, 1), "h")
	noAT := VerifyWebAuthnCreate("o", "h", []byte{1}, nil, nil) // fails earlier: client data
	cases := []struct {
		err  error
		want string
	}{
		{short, "bad_auth_data"},
		{shorter, "bad_auth_data"},
		{fmt.Errorf("%w: no attested credential data", ErrAuthData), "bad_auth_data"},
		{noAT, "bad_client_data"},
		{ErrBadSignature, "bad_signature"},
		{fmt.Errorf("%w: %w", ErrBadKey, errors.New("x509: anything")), "bad_key"},
		{ErrWrongType, "wrong_type"},
		{ErrWrongChallenge, "wrong_challenge"},
		{ErrWrongOrigin, "wrong_origin"},
		{ErrCrossOrigin, "cross_origin"},
		{ErrWrongRPID, "wrong_rp_id"},
		{ErrUserNotPresent, "user_not_present"},
		{ErrUserNotVerified, "user_not_verified"},
		{errors.New(`unknown device kind "<script>"`), "verify_failed"},
	}
	for _, c := range cases {
		if got := reasonCode(c.err); got != c.want {
			t.Errorf("reasonCode(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	if short.Error() == shorter.Error() {
		t.Fatal("test needs two different texts of ErrAuthData")
	}
}
