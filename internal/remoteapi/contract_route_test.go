package remoteapi

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/decide"
)

// TestContract_Route drives POST /v1/route and `routed` on POST
// /v1/sessions through the contract (TestContract leaves them to this test).
func TestContract_Route(t *testing.T) {
	api, as := contractProfileAPI(t, profileFixture{})
	all, _ := api.Profiles()
	for _, name := range []string{"page", "research", "work-readonly"} {
		m := all[name]
		m.Name, m.Runs, m.Available = name, "acp", true
		all[name] = m
	}
	api.ModeRouter = &decide.Decider{Name: "mode-router", Backends: []decide.Backend{webDecision()}, Threshold: 0.25,
		Timeout: time.Second, Log: &decide.Log{Path: filepath.Join(t.TempDir(), "decisions.jsonl")}, Usage: func(cost.Usage) error { return nil }}
	c := newContract(t, api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	as("wingman")

	st, body := c.call("POST", "/v1/route", `{"text":"latest Go release?","facts":{"sitePrivate":false,"pageReadable":true,"usePage":false}}`, testLogin)
	if st != 200 || body["source"] != "model" || body["profile"] != "research" {
		t.Fatalf("decided: %d %v", st, body)
	}
	id := body["decisionId"].(string)
	st, body = c.call("POST", "/v1/route", `{"text":"@page what is this?","facts":{"pageReadable":false}}`, testLogin)
	if st != 200 || body["source"] != "override_blocked" || body["reason"] == nil {
		t.Fatalf("blocked: %d %v", st, body)
	}
	st, body = c.call("POST", "/v1/route", `{"text":""}`, testLogin)
	if st != 400 || body["code"] != "bad_request" {
		t.Fatalf("empty: %d %v", st, body)
	}
	st, body = c.call("POST", "/v1/sessions", `{"profile":"research","text":"latest Go release?","url":"https://example.com/r","routed":{"decisionId":"`+id+`","mode":"web","source":"model"}}`, testLogin)
	if st != 202 {
		t.Fatalf("routed session: %d %v", st, body)
	}
	for range routesPerMinute {
		c.call("POST", "/v1/route", `{"text":"latest x?","facts":{}}`, testLogin)
	}
	st, body = c.call("POST", "/v1/route", `{"text":"latest x?","facts":{}}`, testLogin)
	if st != 429 || body["code"] != "client_limit" {
		t.Fatalf("limit: %d %v", st, body)
	}
	api.ModeRouter = nil
	as("cli")
	st, body = c.call("POST", "/v1/route", `{"text":"what now?","facts":{"pageReadable":true}}`, testLogin)
	if st != 200 || body["source"] != "unavailable" || body["decided"] != false {
		t.Fatalf("no decider: %d %v", st, body)
	}

	for path, item := range c.doc.Paths.Map() {
		if !strings.HasPrefix(path, "/v1/route") {
			continue
		}
		for method, op := range item.Operations() {
			if !c.ops[op.OperationID] {
				t.Errorf("never exercised: %s %s", method, path)
			}
		}
	}
	if len(c.ops) == 0 || !c.ops["route"] {
		t.Fatal("POST /v1/route is not in the contract")
	}
}
