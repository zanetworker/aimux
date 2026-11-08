package remoteapi

import (
	"context"
	"errors"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/sessionstate"
	"github.com/zanetworker/aimux/internal/waiting"
)

var activityFixture = filepath.Join("..", "..", "api", "testdata", "clients", "activity", "cases.json")

// activityCase is one case of api/testdata/clients/activity/cases.json.
type activityCase struct {
	Name    string  `json:"name"`
	Session Session `json:"session"`
	Events  []Event `json:"events"`
	Expect  string  `json:"expect"`
}

func loadActivityCases(t *testing.T) []activityCase {
	t.Helper()
	raw, err := os.ReadFile(activityFixture) // #nosec G304 -- fixed fixture path
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []activityCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("cases.json: %v", err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("cases.json has no cases")
	}
	return f.Cases
}

// fixtureApprovalPending reports an approval event without a later resolution:
// what the Broker's AwaitingApproval says during that turn.
func fixtureApprovalPending(events []Event) bool {
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
	open := map[string]bool{}
	for _, e := range events {
		var d struct {
			ApprovalID string `json:"approvalId"`
		}
		_ = json.Unmarshal(e.Data, &d)
		switch e.Kind {
		case KindApproval:
			open[d.ApprovalID] = true
		case KindApprovalResolved:
			delete(open, d.ApprovalID)
		}
	}
	return len(open) > 0
}

// serverActivities lists every case's session through the API, with the
// state service fed the facts each session's wire fields stand for: a
// terminal holder for open_in_terminal (registry busy when working), an
// aimux turn for busy, its pending approval from the events, and the
// waiting marker.
func serverActivities(t *testing.T, cases []activityCase) map[string]string {
	t.Helper()
	holders := map[string]harness.Holder{}
	busy, asking := idSet{}, idSet{}
	marks := map[string]waiting.Waiting{}
	var list []Session
	names := map[string]string{}
	for _, c := range cases {
		s := c.Session
		names[s.ID] = c.Name
		switch s.State {
		case StateOpenInTerminal:
			status := "idle"
			if s.Working {
				status = "busy"
			}
			holders[s.ID] = harness.Holder{PID: len(holders) + 1, SessionID: s.ID, Status: status}
		case StateBusy:
			busy[s.ID] = true
			asking[s.ID] = fixtureApprovalPending(c.Events)
		}
		if s.Waiting != nil {
			marks[s.ID] = *s.Waiting
		}
		s.Working, s.Waiting = false, nil // the service fills them
		list = append(list, s)
	}
	api := &API{
		Sessions: func(context.Context, int, bool) ([]Session, error) { return list, nil },
		States: &sessionstate.Service{
			Holders:   func() map[string]harness.Holder { return holders },
			Busy:      busy,
			Approvals: asking,
			Markers:   func() map[string]waiting.Waiting { return marks },
		},
	}
	out := map[string]string{}
	for _, s := range listJSON(t, api) {
		a, _ := s["activity"].(string)
		out[names[s["id"].(string)]] = a
	}
	return out
}

// TestActivity_SharedFixture: the server's activity for every case of the
// shared fixture is the expected one.
func TestActivity_SharedFixture(t *testing.T) {
	cases := loadActivityCases(t)
	got := serverActivities(t, cases)
	for _, c := range cases {
		if got[c.Name] != c.Expect {
			t.Errorf("%s: server activity %q, want %q", c.Name, got[c.Name], c.Expect)
		}
	}
}

// TestActivity_ParityWithWebClient: what the phone web client derives
// (now.mjs deriveActivity, search.mjs isWorking/isOpen/isWaitingForPermission)
// matches the server's activity on the shared fixture.
func TestActivity_ParityWithWebClient(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	cases := loadActivityCases(t)
	// Output, not CombinedOutput: a warning on stderr must not break the JSON.
	raw, err := exec.Command(node, filepath.Join("testdata", "activity_parity.mjs"), activityFixture).Output() // #nosec G204 -- fixed test files
	if err != nil {
		var stderr []byte
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		t.Fatalf("node activity_parity.mjs: %v\nstderr:\n%s", err, stderr)
	}
	var client map[string]string
	if err := json.Unmarshal(raw, &client); err != nil {
		t.Fatalf("node output %q: %v", raw, err)
	}
	server := serverActivities(t, cases)
	for _, c := range cases {
		if client[c.Name] != server[c.Name] {
			t.Errorf("%s: web client %q, server %q", c.Name, client[c.Name], server[c.Name])
		}
	}
}
