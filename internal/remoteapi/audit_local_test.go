package remoteapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

// auditLines decodes the audit log.
func auditLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestAudit_LocalRequestWritesLineWithoutText(t *testing.T) {
	r := newProfileRig(t)
	buf := &syncBuffer{}
	r.api.Audit = devicelogin.NewAudit(buf, time.Now, nil)
	r.serve()
	const secret = "the launch codes are 0000"
	const page = "PAGE-BODY-SECRET"
	const url = "https://intranet.example.com/hr/salaries?token=abc123"
	if resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","url":"`+url+`","text":"`+secret+`","pageText":"`+page+`"}`); resp.StatusCode != 202 {
		t.Fatalf("ask: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	r.do("POST", "/v1/sessions", `{"profile":"coding","text":"`+secret+`"}`) // refused: out of scope
	r.do("GET", "/v1/profiles", "")
	r.do("PATCH", "/v1/sessions/p-1/meta", `{"hidden":true}`)
	// A device request writes nothing here (device login audits those).
	r.as("device").do("GET", "/v1/profiles", "")

	raw := buf.String()
	for _, leak := range []string{secret, page, "/hr/salaries", "token=abc123"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("audit log holds %q:\n%s", leak, raw)
		}
	}
	lines := auditLines(t, raw)
	if len(lines) != 4 {
		t.Fatalf("%d audit lines, want 4:\n%s", len(lines), raw)
	}
	first := lines[0]
	if first["client"] != "wingman" || first["action"] != devicelogin.ActionNewSession || first["profile"] != "page" ||
		first["digest"] != testProfiles()["page"].Digest() || first["urlHost"] != "intranet.example.com" || first["result"] != "ok" {
		t.Fatalf("new session line %v", first)
	}
	if l := lines[1]; l["action"] != devicelogin.ActionNewSession || l["profile"] != "coding" || l["result"] != "http_403" {
		t.Fatalf("refusal line %v", l)
	}
	if l := lines[2]; l["action"] != "GET /v1/profiles" || l["result"] != "ok" {
		t.Fatalf("list line %v", l)
	}
	if l := lines[3]; l["action"] != devicelogin.ActionHide || l["session"] != "p-1" {
		t.Fatalf("hide line %v", l)
	}
}
