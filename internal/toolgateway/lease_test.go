package toolgateway

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/toolreg"
)

const leaseTok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func testLease(now time.Time) Lease {
	return Lease{
		Session: "sess-1",
		Token:   leaseTok,
		Allow:   map[string][]string{"jira": {"search"}, "wiki": {"wiki_search"}},
		Version: "v1",
		Pins: map[string]map[string]toolreg.Pin{
			"jira": {"search": {Hash: "sha256:a"}},
			"wiki": {"wiki_search": {Hash: "sha256:w"}},
		},
		Expires: now.Add(15 * time.Minute),
	}
}

func TestPut_LookupReturnsAllow(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	tk := Tokens{Now: fixedNow(now)}
	l := testLease(now)
	if err := tk.Put(l); err != nil {
		t.Fatal(err)
	}
	sess, allow, ok := tk.Lookup(leaseTok)
	if !ok || sess != "sess-1" || !reflect.DeepEqual(allow, l.Allow) {
		t.Fatalf("Lookup = %q %v %v", sess, allow, ok)
	}
	// The caller's lease and the looked-up allow are copies.
	l.Allow["slack"] = []string{"x"}
	if _, again, _ := tk.Lookup(leaseTok); again["slack"] != nil {
		t.Fatal("lease widened through the caller's map")
	}
}

func TestLookup_ExpiredLeaseIsAbsent(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := now
	tk := Tokens{Now: func() time.Time { return clock }}
	if err := tk.Put(testLease(now)); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(15*time.Minute + time.Second)
	if _, _, ok := tk.Lookup(leaseTok); ok {
		t.Fatal("expired lease still accepted")
	}
	if err := tk.Renew("sess-1", clock.Add(time.Hour)); !errors.Is(err, ErrUnknownLease) {
		t.Fatalf("renew of an expired lease = %v, want ErrUnknownLease", err)
	}
}

func TestRenew_UnknownLease(t *testing.T) {
	var tk Tokens
	if err := tk.Renew("nope", time.Now().Add(time.Hour)); !errors.Is(err, ErrUnknownLease) {
		t.Fatalf("Renew = %v, want ErrUnknownLease", err)
	}
}

func TestRenew_ExtendsExpiry(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := now
	tk := Tokens{Now: func() time.Time { return clock }}
	if err := tk.Put(testLease(now)); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(10 * time.Minute)
	if err := tk.Renew("sess-1", clock.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(20 * time.Minute) // past the first expiry, before the renewed one
	if _, _, ok := tk.Lookup(leaseTok); !ok {
		t.Fatal("renewed lease expired at its old time")
	}
}

func TestPut_ReplacesSameSession(t *testing.T) {
	now := time.Now()
	var tk Tokens
	if err := tk.Put(testLease(now)); err != nil {
		t.Fatal(err)
	}
	l2 := testLease(now)
	l2.Token = "ffff" + leaseTok[4:]
	if err := tk.Put(l2); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := tk.Lookup(leaseTok); ok {
		t.Fatal("old token of the session still accepted")
	}
	if _, _, ok := tk.Lookup(l2.Token); !ok {
		t.Fatal("new token refused")
	}
}

func TestLookup_DropsToolWhosePinChanged(t *testing.T) {
	now := time.Now()
	cur := toolreg.NewSnapshot(toolreg.Registry{}, toolreg.PinTable{
		"jira": {"search": {Hash: "sha256:b"}},
		"wiki": {"wiki_search": {Hash: "sha256:w"}},
	}, nil)
	tk := Tokens{Current: func() toolreg.Snapshot { return cur }}
	if err := tk.Put(testLease(now)); err != nil {
		t.Fatal(err)
	}
	_, allow, ok := tk.Lookup(leaseTok)
	if !ok {
		t.Fatal("lease refused")
	}
	if len(allow["jira"]) != 0 || !reflect.DeepEqual(allow["wiki"], []string{"wiki_search"}) {
		t.Fatalf("allow = %v, want jira emptied (pin changed), wiki kept", allow)
	}
}

func TestPut_RejectsPastExpiry(t *testing.T) {
	now := time.Now()
	tk := Tokens{Now: fixedNow(now)}
	l := testLease(now)
	l.Expires = now.Add(-time.Second)
	if err := tk.Put(l); err == nil {
		t.Fatal("past expiry accepted")
	}
	for _, bad := range []Lease{{Token: leaseTok, Expires: now.Add(time.Hour)}, {Session: "s", Expires: now.Add(time.Hour)}} {
		if err := tk.Put(bad); err == nil {
			t.Fatalf("lease for session %q accepted without a session or token", bad.Session)
		}
	}
}

func TestIssue_StillWorks(t *testing.T) {
	tk := Tokens{Now: fixedNow(time.Now().Add(1000 * time.Hour))}
	tok, err := tk.Issue("sess-9", map[string][]string{"jira": {"a"}}, toolreg.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := tk.Lookup(tok); !ok {
		t.Fatal("an issued token must not expire")
	}
}

func TestRenew_RejectsZeroOrPastDeadline(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := now
	tk := Tokens{Now: func() time.Time { return clock }}
	if err := tk.Put(testLease(now)); err != nil {
		t.Fatal(err)
	}
	for _, until := range []time.Time{{}, now.Add(-time.Minute)} {
		if err := tk.Renew("sess-1", until); err == nil {
			t.Fatalf("Renew(%v) accepted", until)
		}
	}
	clock = now.Add(16 * time.Minute)
	if _, _, ok := tk.Lookup(leaseTok); ok {
		t.Fatal("a refused renew must not stop the lease from expiring")
	}
}

func TestLookup_SweepsOtherExpiredLeases(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := now
	tk := Tokens{Now: func() time.Time { return clock }}
	if err := tk.Put(testLease(now)); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(time.Hour)
	_, _, _ = tk.Lookup("not-a-token")
	tk.mu.Lock()
	n := len(tk.sessions)
	tk.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d expired lease(s) kept after a lookup", n)
	}
}

func TestLen_DropsExpiredLeases(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	clock := now
	tk := Tokens{Now: func() time.Time { return clock }}
	if err := tk.Put(testLease(now)); err != nil {
		t.Fatal(err)
	}
	if tk.Len() != 1 {
		t.Fatal("live lease not counted")
	}
	clock = now.Add(time.Hour)
	if n := tk.Len(); n != 0 {
		t.Fatalf("Len = %d after expiry", n)
	}
	tk.mu.Lock()
	kept := len(tk.sessions)
	tk.mu.Unlock()
	if kept != 0 {
		t.Fatal("Len kept the expired lease")
	}
}
