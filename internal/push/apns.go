package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Apple's servers per environment.
var apnsHosts = map[string]string{
	EnvSandbox:    "https://api.sandbox.push.apple.com",
	EnvProduction: "https://api.push.apple.com",
}

// Apple rejects provider tokens older than an hour and throttles fresh ones
// more often than every 20 minutes.
const apnsTokenAge = 40 * time.Minute

// APNs sends alerts straight to Apple's push service with a token signing
// key (.p8) from the Apple Developer account, to every registered device.
type APNs struct {
	Key     *ecdsa.PrivateKey
	KeyID   string // the key's id in the developer account
	TeamID  string
	Topic   string // the app's bundle id
	Devices *DeviceStore
	// AllowedLogins are the logins whose devices still get alerts: a phone
	// whose login was removed from the allowlist gets nothing.
	AllowedLogins []string
	Client        *http.Client // must speak HTTP/2; Go's default client does over TLS

	hosts map[string]string // environment -> base URL; nil: Apple's
	now   func() time.Time

	mu      sync.Mutex
	token   string
	tokenAt time.Time
}

// Notify sends m to every registered device. Devices Apple says are gone
// are forgotten; other failures are returned.
func (a *APNs) Notify(ctx context.Context, m Message) error {
	all, err := a.Devices.List()
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, l := range a.AllowedLogins {
		allowed[strings.ToLower(strings.TrimSpace(l))] = true
	}
	var devices []Device
	for _, d := range all {
		if allowed[strings.ToLower(strings.TrimSpace(d.Login))] {
			devices = append(devices, d)
		}
	}
	if len(devices) == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert":              map[string]string{"title": m.Title, "body": m.Body},
			"sound":              "default",
			"interruption-level": "time-sensitive",
			"category":           "AIMUX_APPROVAL",
			"thread-id":          m.SessionID,
		},
		"approvalId": m.ApprovalID,
		"sessionId":  m.SessionID,
	})
	if err != nil {
		return err
	}
	auth, err := a.providerToken()
	if err != nil {
		return err
	}
	// Every phone at once, a few at a time, so a stalled one does not use up
	// the others' time.
	errs := make([]error, len(devices))
	slots := make(chan struct{}, apnsParallel)
	var wg sync.WaitGroup
	for i, d := range devices {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			gone, err := a.send(ctx, d, auth, m, payload)
			if gone {
				err = a.Devices.Remove(d.Token)
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

const apnsParallel = 8

// send pushes to one device; gone reports that Apple no longer knows it.
func (a *APNs) send(ctx context.Context, d Device, auth string, m Message, payload []byte) (gone bool, err error) {
	hosts := a.hosts
	if hosts == nil {
		hosts = apnsHosts
	}
	url := hosts[d.Environment] + "/3/device/" + d.Token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("apns: %w", err)
	}
	req.Header.Set("authorization", "bearer "+auth)
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	if m.ApprovalID != "" {
		req.Header.Set("apns-collapse-id", m.ApprovalID)
	}
	// 0 asks Apple to try once and keep nothing; a later time keeps the
	// alert for a phone that is offline until then.
	exp := int64(0)
	if !m.Expires.IsZero() {
		exp = m.Expires.Unix()
	}
	req.Header.Set("apns-expiration", strconv.FormatInt(exp, 10))
	resp, err := client(a.Client).Do(req)
	if err != nil {
		return false, fmt.Errorf("apns: push to %s device %.8s…: %w", d.Environment, d.Token, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return false, nil
	}
	var why struct {
		Reason string `json:"reason"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	_ = json.Unmarshal(b, &why) // a missing reason still leaves the status
	if resp.StatusCode == http.StatusGone || why.Reason == "BadDeviceToken" || why.Reason == "Unregistered" {
		return true, nil
	}
	return false, fmt.Errorf("apns: push to %s device %.8s… refused: %s %s", d.Environment, d.Token, resp.Status, why.Reason)
}

// providerToken returns the ES256 JWT Apple authenticates pushes with,
// reusing it until it ages.
func (a *APNs) providerToken() (string, error) {
	now := time.Now
	if a.now != nil {
		now = a.now
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t := now()
	if a.token != "" && t.Sub(a.tokenAt) < apnsTokenAge {
		return a.token, nil
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v) // maps of strings and ints always marshal
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signed := enc(map[string]string{"alg": "ES256", "kid": a.KeyID}) + "." + enc(map[string]any{"iss": a.TeamID, "iat": t.Unix()})
	sum := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, a.Key, sum[:])
	if err != nil {
		return "", fmt.Errorf("apns: sign provider token: %w", err)
	}
	sig := make([]byte, 64) // JWS wants r and s as fixed 32-byte halves
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	a.token, a.tokenAt = signed+"."+base64.RawURLEncoding.EncodeToString(sig), t
	return a.token, nil
}

// LoadAPNsKey reads a token signing key (.p8) downloaded from the Apple
// Developer account.
func LoadAPNsKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the key path comes from the owner's own config
	if err != nil {
		return nil, fmt.Errorf("read APNs key: %w", err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("APNs key %s is not a PEM .p8 file", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("APNs key %s: %w", path, err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("APNs key %s is not a P-256 EC key, which Apple issues and ES256 needs", path)
	}
	return ec, nil
}
