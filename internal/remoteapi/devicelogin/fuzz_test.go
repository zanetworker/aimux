package devicelogin

import (
	"bytes"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"testing"
)

func TestParseP256_RefusesRSAAndEd25519(t *testing.T) {
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, pub := range map[string]any{"RSA": &rk.PublicKey, "Ed25519": ek} {
		der, err := x509.MarshalPKIXPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseP256(der); !errors.Is(err, ErrBadKey) {
			t.Errorf("%s key: %v, want ErrBadKey", name, err)
		}
	}
}

// sentinels are the fixed error texts; leaks looks only at what is left.
var sentinels = []error{ErrBadKey, ErrBadSignature, ErrClientData, ErrWrongType, ErrWrongChallenge,
	ErrWrongOrigin, ErrCrossOrigin, ErrAuthData, ErrWrongRPID, ErrUserNotPresent, ErrUserNotVerified}

// leaks reports whether err's text, without the fixed sentinel texts, holds
// a run of 8 or more input bytes, so an audited error cannot carry what was
// sent.
func leaks(err error, inputs ...[]byte) bool {
	if err == nil {
		return false
	}
	msg := []byte(err.Error())
	for _, s := range sentinels {
		msg = bytes.ReplaceAll(msg, []byte(s.Error()), nil)
	}
	for _, in := range inputs {
		for i := 0; i+8 <= len(in); i++ {
			if bytes.Contains(msg, in[i:i+8]) {
				return true
			}
		}
	}
	return false
}

func FuzzParseP256(f *testing.F) {
	k := newKey(&testing.T{}, elliptic.P256())
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	f.Add(der)
	f.Add([]byte{})
	f.Add([]byte("not a key at all, just text"))
	f.Add(append(append([]byte{}, der...), 0))
	f.Fuzz(func(t *testing.T, in []byte) {
		_, err := ParseP256(in)
		if leaks(err, in) {
			t.Fatalf("error %q repeats input bytes", err)
		}
	})
}

func FuzzVerifyWebAuthnGet(f *testing.F) {
	k := newKey(&testing.T{}, elliptic.P256())
	challenge := sha256.Sum256([]byte("fuzz challenge"))
	good := goodWebauthn("webauthn.get", challenge[:])
	cd, ad := good.clientData(&testing.T{}), good.authData()
	h := sha256.Sum256(cd)
	sig := sign(&testing.T{}, k, append(append([]byte{}, ad...), h[:]...))
	f.Add(cd, ad, sig)
	f.Add([]byte(`{"type":"webauthn.get","challenge":"%%%","origin":"x"}`), ad[:36], []byte{})
	f.Add([]byte(`[]`), []byte{}, []byte("junk"))
	f.Fuzz(func(t *testing.T, cd, ad, sig []byte) {
		err := VerifyWebAuthnGet(testOrigin, testHost, challenge[:], &k.PublicKey, cd, ad, sig)
		if leaks(err, cd, ad, sig) {
			t.Fatalf("error %q repeats input bytes", err)
		}
	})
}
