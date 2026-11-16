package remoteapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

// stepUpRig is a profileRig serving device requests with device login on.
type stepUpRig struct {
	*profileRig
	login *loginRig
}

func newStepUpRig(t *testing.T, legacy bool) *stepUpRig {
	t.Helper()
	pr := newProfileRig(t).as("device")
	lr := &loginRig{t: t, dir: t.TempDir(), audit: &syncBuffer{}, clock: &testClock{t: time.Now()}}
	dl, err := devicelogin.NewServer(devicelogin.Options{
		Store: devicelogin.Open(lr.dir), Now: lr.clock.now,
		Audit: devicelogin.NewAudit(lr.audit, lr.clock.now, nil),
		Login: func(req *http.Request) string { return LoginFrom(req.Context()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	lr.dl = dl
	pr.api.DeviceLogin, pr.api.StepUpLegacy = dl, legacy
	lr.srv = httptest.NewServer(pr.api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(lr.srv.Close)
	pr.srv = lr.srv
	return &stepUpRig{profileRig: pr, login: lr}
}

// newSession posts body with tok and checks the answer.
func (r *stepUpRig) newSession(tok, body string, status int, code string) map[string]any {
	r.t.Helper()
	st, out := r.login.do("POST", "/v1/sessions", body, tok)
	if st != status || (code != "" && out["code"] != code) {
		r.t.Fatalf("%s: %d %v, want %d %s", body, st, out, status, code)
	}
	return out
}

func (r *stepUpRig) nothingStarted() {
	r.t.Helper()
	if r.started() != 0 || r.f.sends.Load() != 0 {
		r.t.Fatalf("started %d profile sessions, %d turns; want none", r.started(), r.f.sends.Load())
	}
}

const codingAsk = `{"profile":"coding","text":"refactor it"}`

func TestStepUp_OldLoginRefused403(t *testing.T) {
	r := newStepUpRig(t, false)
	tok := r.login.pair("iphone")
	r.login.clock.add(61 * time.Second)
	out := r.newSession(tok, codingAsk, 403, "step_up_required")
	if out["maxAge"] != float64(60) {
		t.Fatalf("refusal %v, want maxAge 60", out)
	}
	r.nothingStarted()
	// Read-only profiles need no step-up.
	r.newSession(tok, `{"profile":"page","text":"hi"}`, 202, "")
}

// Without device login there is no login age to check: host profiles are
// refused (fail closed).
func TestStepUp_NoDeviceLoginRefusesHostProfiles(t *testing.T) {
	r := newProfileRig(t).as("device").serve()
	out := r.expectRefused(codingAsk, 403, "step_up_required")
	if out["maxAge"] != float64(60) {
		t.Fatalf("refusal %v", out)
	}
}

func TestStepUp_FreshSecureEnclaveLoginAllowed(t *testing.T) {
	r := newStepUpRig(t, false)
	tok := r.login.pair("iphone")
	r.login.clock.add(59 * time.Second)
	r.newSession(tok, codingAsk, 202, "")
	if r.started() != 1 {
		t.Fatalf("started %d", r.started())
	}
}

func TestStepUp_PasskeyDeviceRefusedForCoding(t *testing.T) {
	r := newStepUpRig(t, false)
	tok := r.login.pair("laptop")
	// The same device, now a passkey (as `aimux remote pair` from a browser
	// records it).
	store := devicelogin.Open(r.login.dir)
	d, err := store.Remove("laptop")
	if err != nil {
		t.Fatal(err)
	}
	d.Kind = devicelogin.KindWebAuthn
	if err := store.Add(d); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second) // the server rereads the file at most once a second
	for {
		r.login.dl.Tick()
		st, out := r.login.do("POST", "/v1/sessions", codingAsk, tok)
		if st == 403 && out["code"] == "step_up_required" {
			break
		}
		if st != 202 && st != 409 || time.Now().After(deadline) {
			t.Fatalf("passkey device: %d %v", st, out)
		}
		time.Sleep(100 * time.Millisecond)
		r.f.sends.Store(0)
		r.mu.Lock()
		r.starts = nil
		r.mu.Unlock()
	}
}

func TestStepUp_LegacyRequestUnchangedByDefault(t *testing.T) {
	legacy := `{"cwd":"/w/app","text":"plan it"}`
	r := newStepUpRig(t, false)
	tok := r.login.pair("iphone")
	r.login.clock.add(10 * time.Minute)
	r.newSession(tok, legacy, 202, "")

	strict := newStepUpRig(t, true) // remote_access.step_up_legacy: true
	tok = strict.login.pair("iphone")
	strict.login.clock.add(61 * time.Second)
	strict.newSession(tok, legacy, 403, "step_up_required")
	strict.nothingStarted()
	fresh := strict.login.pair("ipad") // a login just now
	strict.newSession(fresh, legacy, 202, "")
}
