package devicelogin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Why a signature check failed. Each is a fixed string so callers can audit
// the reason; none ever carries a challenge, signature, key or client data.
var (
	ErrBadKey          = errors.New("public key is not an ECDSA P-256 key in SPKI DER")
	ErrBadSignature    = errors.New("signature does not verify")
	ErrClientData      = errors.New("clientDataJSON is not a JSON object")
	ErrWrongType       = errors.New("clientDataJSON type is not the expected ceremony")
	ErrWrongChallenge  = errors.New("clientDataJSON challenge does not match")
	ErrWrongOrigin     = errors.New("clientDataJSON origin does not match")
	ErrCrossOrigin     = errors.New("clientDataJSON says crossOrigin")
	ErrAuthData        = errors.New("authenticatorData is malformed")
	ErrWrongRPID       = errors.New("authenticatorData rpIdHash does not match")
	ErrUserNotPresent  = errors.New("authenticatorData lacks the user-present flag")
	ErrUserNotVerified = errors.New("authenticatorData lacks the user-verified flag")
)

// authenticatorData flag bits (WebAuthn §6.1).
const (
	flagUP = 1 << 0 // user present
	flagUV = 1 << 2 // user verified (Face ID, Touch ID)
	flagAT = 1 << 6 // attested credential data follows
)

// minAuthData is rpIdHash (32) + flags (1) + signCount (4).
const minAuthData = 37

// PairMessage is what the iPhone signs to pair: host is the host name without
// port, code the 8 canonical characters without the dash.
func PairMessage(host, code string) []byte {
	return []byte("aimux-pair-v1\n" + host + "\n" + code)
}

// LoginMessage is what the iPhone signs to log in: host is the host name
// without port, challenge the base64url string the server issued.
func LoginMessage(host, challenge string) []byte {
	return []byte("aimux-login-v1\n" + host + "\n" + challenge)
}

// ParseP256 parses an SPKI DER public key and refuses anything but P-256.
func ParseP256(spkiDER []byte) (*ecdsa.PublicKey, error) {
	key, err := x509.ParsePKIXPublicKey(spkiDER)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadKey, err)
	}
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, ErrBadKey
	}
	return pub, nil
}

// VerifyEnclave checks an ECDSA P-256 SHA-256 signature (ASN.1 DER) by the
// SPKI DER key over message, as made by the iPhone's Secure Enclave key.
func VerifyEnclave(publicKeyDER, message, sigDER []byte) error {
	pub, err := ParseP256(publicKeyDER)
	if err != nil {
		return err
	}
	return verifyP256(pub, message, sigDER)
}

// verifyP256 hashes data with SHA-256 and checks sigDER. ecdsa.VerifyASN1
// refuses DER with trailing bytes. It accepts high-S signatures, which is
// fine: P-256 ECDSA here has no malleability rule and a signature is never
// used as an identifier.
func verifyP256(pub *ecdsa.PublicKey, data, sigDER []byte) error {
	if pub == nil {
		return ErrBadKey
	}
	h := sha256.Sum256(data)
	if !ecdsa.VerifyASN1(pub, h[:], sigDER) {
		return ErrBadSignature
	}
	return nil
}

// VerifyWebAuthnCreate checks the clientDataJSON and authenticatorData of a
// passkey creation (attestation none, so there is no signature): type
// webauthn.create, the challenge, origin, rpIdHash, UP, UV and attested
// credential data present. The public key itself comes from the browser's
// getPublicKey() and is checked with ParseP256.
// Nothing here is signed (attestation none), so a caller could forge all of
// it: the pairing challenge, the hash of a code only the owner saw, is the gate.
func VerifyWebAuthnCreate(origin, rpID string, wantChallenge []byte, clientDataJSON, authenticatorData []byte) error {
	if err := checkClientData(clientDataJSON, "webauthn.create", origin, wantChallenge); err != nil {
		return err
	}
	if err := checkAuthData(authenticatorData, rpID); err != nil {
		return err
	}
	if authenticatorData[32]&flagAT == 0 {
		return fmt.Errorf("%w: no attested credential data", ErrAuthData)
	}
	return nil
}

// VerifyWebAuthnGet checks a passkey assertion: clientDataJSON (type
// webauthn.get, challenge, origin), authenticatorData (rpIdHash, UP, UV) and
// the signature over authenticatorData || SHA-256(clientDataJSON). The sign
// counter is ignored (synced passkeys report 0).
func VerifyWebAuthnGet(origin, rpID string, wantChallenge []byte, pub *ecdsa.PublicKey, clientDataJSON, authenticatorData, sigDER []byte) error {
	if err := checkClientData(clientDataJSON, "webauthn.get", origin, wantChallenge); err != nil {
		return err
	}
	if err := checkAuthData(authenticatorData, rpID); err != nil {
		return err
	}
	cdHash := sha256.Sum256(clientDataJSON)
	signed := make([]byte, 0, len(authenticatorData)+len(cdHash))
	signed = append(append(signed, authenticatorData...), cdHash[:]...)
	return verifyP256(pub, signed, sigDER)
}

func checkClientData(clientDataJSON []byte, wantType, wantOrigin string, wantChallenge []byte) error {
	var cd struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}
	if err := json.Unmarshal(clientDataJSON, &cd); err != nil {
		return ErrClientData // the JSON error may quote the input
	}
	if cd.Type != wantType {
		return ErrWrongType
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(cd.Challenge)
	if err != nil || len(wantChallenge) == 0 || subtle.ConstantTimeCompare(got, wantChallenge) != 1 {
		return ErrWrongChallenge
	}
	if cd.Origin != wantOrigin {
		return ErrWrongOrigin
	}
	if cd.CrossOrigin {
		return ErrCrossOrigin
	}
	return nil
}

func checkAuthData(ad []byte, rpID string) error {
	if len(ad) < minAuthData {
		return fmt.Errorf("%w: %d bytes, want at least %d", ErrAuthData, len(ad), minAuthData)
	}
	want := sha256.Sum256([]byte(rpID))
	if subtle.ConstantTimeCompare(ad[:32], want[:]) != 1 {
		return ErrWrongRPID
	}
	if ad[32]&flagUP == 0 {
		return ErrUserNotPresent
	}
	if ad[32]&flagUV == 0 {
		return ErrUserNotVerified
	}
	return nil
}

// reasons maps each check failure to its fixed audit reason code.
var reasons = []struct {
	err  error
	code string
}{
	{ErrBadKey, "bad_key"},
	{ErrBadSignature, "bad_signature"},
	{ErrClientData, "bad_client_data"},
	{ErrWrongType, "wrong_type"},
	{ErrWrongChallenge, "wrong_challenge"},
	{ErrWrongOrigin, "wrong_origin"},
	{ErrCrossOrigin, "cross_origin"},
	{ErrAuthData, "bad_auth_data"},
	{ErrWrongRPID, "wrong_rp_id"},
	{ErrUserNotPresent, "user_not_present"},
	{ErrUserNotVerified, "user_not_verified"},
}

// reasonCode is the audit reason for a failed check: a fixed code per
// sentinel error (wrapped ones included), "verify_failed" for anything else.
// Never the error's text, so audit results stay a small fixed set.
func reasonCode(err error) string {
	for _, r := range reasons {
		if errors.Is(err, r.err) {
			return r.code
		}
	}
	return "verify_failed"
}
