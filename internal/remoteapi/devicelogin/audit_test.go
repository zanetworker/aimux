package devicelogin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestAudit_OneJSONLinePerEntry(t *testing.T) {
	var buf lockedBuffer
	c := newClock()
	a := NewAudit(&buf, c.now, nil)
	a.Write(AuditEntry{Login: "me@example.com", Device: "iphone", Action: ActionSend, Session: "s1", Result: "ok"})
	a.Write(AuditEntry{Login: "me@example.com", Action: ActionPairFailed, Result: "bad_code"})
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), buf.String())
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"time": c.now().Format(time.RFC3339Nano), "login": "me@example.com", "device": "iphone", "action": "send", "session": "s1", "result": "ok"}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("%s = %v, want %v", k, first[k], v)
		}
	}
	if len(first) != len(want) {
		t.Errorf("unexpected fields in %v", first)
	}
	if strings.Contains(lines[1], `"session"`) {
		t.Errorf("empty session should be left out: %s", lines[1])
	}
}

func TestAudit_ConcurrentLinesStayWhole(t *testing.T) {
	var buf lockedBuffer
	a := NewAudit(&buf, time.Now, nil)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() { defer wg.Done(); a.Write(AuditEntry{Action: ActionLogin, Result: "ok"}) }()
	}
	wg.Wait()
	sc := bufio.NewScanner(strings.NewReader(buf.String()))
	n := 0
	for sc.Scan() {
		if !json.Valid(sc.Bytes()) {
			t.Fatalf("broken line %q", sc.Text())
		}
		n++
	}
	if n != 50 {
		t.Fatalf("%d lines, want 50", n)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestAudit_WriteFailureIsReported(t *testing.T) {
	var errs lockedBuffer
	NewAudit(failWriter{}, time.Now, &errs).Write(AuditEntry{Action: ActionLogin, Result: "ok"})
	if !strings.Contains(errs.String(), "audit log") || !strings.Contains(errs.String(), "disk full") {
		t.Fatalf("failure not reported: %q", errs.String())
	}
}

func TestAudit_NilIsANoOp(t *testing.T) {
	var a *Audit
	a.Write(AuditEntry{Action: ActionLogin}) // must not panic
}

func TestAudit_CollapsesRepeatedFailures(t *testing.T) {
	var buf lockedBuffer
	c := newClock()
	a := NewAudit(&buf, c.now, nil)
	fail := AuditEntry{Login: me, Action: ActionLoginFailed, Result: "bad_signature"}
	for range 3 {
		a.Write(fail)
		c.add(10 * time.Second)
	}
	a.Write(AuditEntry{Login: me, Action: ActionLoginFailed, Result: "bad_challenge"})                 // another result: its own line
	a.Write(AuditEntry{Login: me, Device: "ipad", Action: ActionLoginFailed, Result: "bad_signature"}) // another device: its own line
	a.Write(AuditEntry{Login: me, Action: ActionLogin, Result: "ok"})
	a.Write(AuditEntry{Login: me, Action: ActionLogin, Result: "ok"}) // successes are never collapsed
	c.add(time.Minute)
	a.Write(fail)
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var e struct {
			Result string `json:"result"`
			Count  int    `json:"count"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Result+"/"+strconv.Itoa(e.Count))
	}
	want := "bad_signature/0 bad_challenge/0 bad_signature/0 ok/0 ok/0 bad_signature/3"
	if strings.Join(got, " ") != want {
		t.Fatalf("lines %v, want %s\n%s", got, want, buf.String())
	}
}
