package remoteapi

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/macconfirm"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// macRig is Wingman allowed to approve with the mac-confirm step-up, a
// fake dialog answering answer, and its audit lines in audit.
type macRig struct {
	*profileRig
	shown  []string // the dialog texts, in order
	titles []string
	audit  *syncBuffer
}

func newMacRig(t *testing.T, answer macconfirm.Answer) *macRig {
	t.Helper()
	r, store := approveRig(t, "wingman", true, []string{StepUpMacConfirm})
	// b was approved with its definition, so the diff can name what changed.
	def, err := toolgateway.Canonical(toolDef("b", "string"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(f toolgateway.PinFile) (toolgateway.PinFile, bool, error) {
		rec := f["jira"]["b"]
		rec.Definition = def
		f["jira"]["b"] = rec
		return f, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	m := &macRig{profileRig: r, audit: &syncBuffer{}}
	r.api.Audit = devicelogin.NewAudit(m.audit, time.Now, nil)
	r.api.MacConfirm = func(_ context.Context, title, text string) (macconfirm.Answer, error) {
		m.titles, m.shown = append(m.titles, title), append(m.shown, text)
		return answer, nil
	}
	return m
}

const approveB = `{"set":"jira:readonly","tools":[{"name":"b","pin":"%s"}]}`

func (m *macRig) approveB(t *testing.T) (int, map[string]any) {
	t.Helper()
	resp, out := m.do("POST", "/v1/tools/approve", strings.Replace(approveB, "%s", liveB(t), 1))
	return resp.StatusCode, out
}

// Approve in the Mac dialog is the step-up: the pins are written, and the
// dialog showed what aimux itself found changed, not text from the client.
func TestToolsApprove_MacConfirmApproves(t *testing.T) {
	m := newMacRig(t, macconfirm.Approved)
	st, out := m.approveB(t)
	if st != 200 || out["written"] != true {
		t.Fatalf("%d %v", st, out)
	}
	if len(m.shown) != 1 || m.titles[0] != "aimux: approve tools" {
		t.Fatalf("dialogs %q titles %q", m.shown, m.titles)
	}
	text := m.shown[0]
	for _, want := range []string{"jira:readonly", "b:", "changed", `inputSchema.properties.q.type: "string" -> "integer"`} {
		if !strings.Contains(text, want) {
			t.Errorf("dialog text lacks %q:\n%s", want, text)
		}
	}
	var line map[string]any
	for _, l := range strings.Split(strings.TrimSpace(m.audit.String()), "\n") {
		if strings.Contains(l, `"tools_approve"`) {
			_ = json.Unmarshal([]byte(l), &line)
		}
	}
	if line["step_up"] != StepUpMacConfirm || line["client"] != "wingman" {
		t.Fatalf("audit line %v, want step_up mac-confirm for wingman", line)
	}
}

// Cancel, no answer in time, or no one to ask: 403 step_up_required with a
// message saying which, and nothing written.
func TestToolsApprove_MacConfirmRefusals(t *testing.T) {
	for answer, want := range map[macconfirm.Answer]string{
		macconfirm.Cancelled:   "cancelled",
		macconfirm.TimedOut:    "60 seconds",
		macconfirm.Unavailable: "could not ask",
	} {
		m := newMacRig(t, answer)
		before, _ := m.api.Pins.LoadFile()
		st, out := m.approveB(t)
		if msg, _ := out["error"].(string); st != 403 || out["code"] != "step_up_required" || !strings.Contains(msg, want) {
			t.Errorf("answer %v: %d %v, want 403 step_up_required saying %q", answer, st, out, want)
		}
		if after, _ := m.api.Pins.LoadFile(); after["jira"]["b"].Hash != before["jira"]["b"].Hash {
			t.Errorf("answer %v: pins written", answer)
		}
	}
}

// No dialog for a request that fails before it (stale pin, bad request), and
// none without a dialog on this server: then the kind is unusable.
func TestToolsApprove_MacConfirmOnlyForAGoodRequest(t *testing.T) {
	m := newMacRig(t, macconfirm.Approved)
	if resp, _ := m.do("POST", "/v1/tools/approve", `{"set":"jira:readonly","tools":[{"name":"b","pin":"sha256:stale"}]}`); resp.StatusCode != 409 || len(m.shown) != 0 {
		t.Fatalf("stale pin: %d, dialogs %d", resp.StatusCode, len(m.shown))
	}
	m.api.MacConfirm = nil
	if st, out := m.approveB(t); st != 403 || out["code"] != "step_up_required" {
		t.Fatalf("no dialog here: %d %v", st, out)
	}
}

// A client whose kinds do not include mac-confirm never gets a dialog.
func TestToolsApprove_NoMacConfirmKindNoDialog(t *testing.T) {
	r, _ := approveRig(t, "wingman", true, []string{"secure-enclave"})
	shown := 0
	r.api.MacConfirm = func(context.Context, string, string) (macconfirm.Answer, error) {
		shown++
		return macconfirm.Approved, nil
	}
	resp, out := r.do("POST", "/v1/tools/approve", strings.Replace(approveB, "%s", liveB(t), 1))
	if resp.StatusCode != 403 || out["code"] != "step_up_required" || shown != 0 {
		t.Fatalf("%d %v, dialogs %d", resp.StatusCode, out, shown)
	}
}

// A batch whose dialog would be cut is refused before any dialog: the
// person must see every tool they approve.
func TestToolsApprove_MacConfirmRefusesWhatDoesNotFit(t *testing.T) {
	m := newMacRig(t, macconfirm.Approved)
	long := strings.Repeat("y", macconfirm.MaxText)
	text := confirmText(long, "jira:readonly", "jira", []ToolApproveEntry{{Name: "b", Pin: liveB(t)}}, m.api.Tools(), nil)
	if macconfirm.Fits(text) {
		t.Fatal("the test text fits; it proves nothing")
	}
	// A client by that (long) name, with Wingman's scope: its dialog is the
	// one that would not fit.
	m.api.Scopes.Clients[long] = m.api.Scopes.Clients["wingman"]
	setClientOf(m.api, func(*http.Request) string { return long })
	st, out := m.approveB(t)
	if st != 400 || out["code"] != "bad_request" || len(m.shown) != 0 {
		t.Fatalf("%d %v, dialogs %d: want 400 and no dialog", st, out, len(m.shown))
	}
}

// The tool configuration changing while the dialog is open makes the
// approval stale after Approve: nothing is written for the old one.
func TestToolsApprove_MacConfirmRechecksAfterApprove(t *testing.T) {
	m := newMacRig(t, macconfirm.Approved)
	view := m.api.Tools()
	m.api.MacConfirm = func(context.Context, string, string) (macconfirm.Answer, error) {
		changed := view
		changed.Snapshot = toolreg.NewSnapshot(view.Snapshot.Registry(), toolreg.PinTable{}, nil)
		m.api.Tools = func() ToolsView { return changed }
		return macconfirm.Approved, nil
	}
	before, _ := m.api.Pins.LoadFile()
	if st, out := m.approveB(t); st != 409 || out["code"] != "pin_stale" {
		t.Fatalf("%d %v, want 409 pin_stale", st, out)
	}
	if after, _ := m.api.Pins.LoadFile(); after["jira"]["b"].Hash != before["jira"]["b"].Hash {
		t.Fatal("pins written after the configuration changed")
	}
}

// One dialog at a time on the Mac: a second approval while one waits gets
// a retryable 503, no second dialog.
func TestToolsApprove_MacConfirmOneAtATime(t *testing.T) {
	m := newMacRig(t, macconfirm.Approved)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	m.api.MacConfirm = func(ctx context.Context, _, _ string) (macconfirm.Answer, error) {
		entered <- struct{}{}
		<-release
		return macconfirm.Cancelled, nil
	}
	done := make(chan int, 1)
	go func() { st, _ := m.approveB(t); done <- st }()
	<-entered
	st, out := m.approveB(t)
	close(release)
	if st != 503 || out["code"] != "confirm_pending" {
		t.Fatalf("second approval: %d %v, want 503 confirm_pending", st, out)
	}
	if first := <-done; first != 403 {
		t.Fatalf("first approval: %d, want 403 after Cancel", first)
	}
}

// The client's access to the set is checked again after Approve: a profile
// change while the dialog was open that takes the set away refuses it.
func TestToolsApprove_MacConfirmRechecksAccess(t *testing.T) {
	m := newMacRig(t, macconfirm.Approved)
	m.api.MacConfirm = func(context.Context, string, string) (macconfirm.Answer, error) {
		m.api.Profiles = func() (map[string]profile.Manifest, error) { return map[string]profile.Manifest{}, nil }
		return macconfirm.Approved, nil
	}
	before, _ := m.api.Pins.LoadFile()
	if st, out := m.approveB(t); st != 403 || out["code"] != "forbidden_for_client" {
		t.Fatalf("%d %v, want 403 forbidden_for_client", st, out)
	}
	if after, _ := m.api.Pins.LoadFile(); after["jira"]["b"].Hash != before["jira"]["b"].Hash {
		t.Fatal("pins written after the access went away")
	}
}

// A set requested under an alias (wiki:readonly resolves to notes:readonly)
// still shows what changed: changes are matched by source and tool, the
// dialog keeps the name the client used.
func TestConfirmText_AliasedSet(t *testing.T) {
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{"notes": {Type: toolreg.TypeLocalNotes, Root: "/notes"}},
		Sets:    map[string]toolreg.Set{"notes:readonly": {Source: "notes", Tools: []string{"wiki_search"}}}}
	if s, err := reg.Resolve("wiki:readonly"); err != nil || s.Source != "notes" {
		t.Fatalf("wiki:readonly resolves to %+v, %v", s, err)
	}
	view := ToolsView{Snapshot: toolreg.NewSnapshot(reg, toolreg.PinTable{}, nil),
		Live: map[string]toolgateway.LiveSource{"notes": {Tools: map[string]json.RawMessage{"wiki_search": toolDef("wiki_search", "string")}}}}
	text := confirmText("wingman", "wiki:readonly", "notes", []ToolApproveEntry{{Name: "wiki_search"}}, view, toolgateway.PinFile{})
	if !strings.Contains(text, "wiki:readonly") || !strings.Contains(text, "wiki_search: new") {
		t.Fatalf("dialog text:\n%s", text)
	}
}

// A request cancelled after Approve, while waiting to write, writes nothing.
func TestApprovePins_CancelledRequestDoesNotCommit(t *testing.T) {
	m := newMacRig(t, macconfirm.Approved)
	v := m.api.Tools()
	checked, _ := m.api.Pins.LoadFile()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := m.api.approvePins(ctx, v, []string{"jira:readonly/c"}, checked); err == nil {
		t.Fatal("a cancelled request wrote pins")
	}
	if after, _ := m.api.Pins.LoadFile(); after["jira"]["c"].Hash != "" {
		t.Fatalf("pin written: %+v", after["jira"]["c"])
	}
}

// After Approve the source is listed afresh again: a tool the backend
// changed while the dialog was open is stale, and nothing is written.
func TestToolsApprove_MacConfirmRelistsAfterApprove(t *testing.T) {
	m := newMacRig(t, macconfirm.Approved)
	view := m.api.Tools()
	calls := 0
	m.api.Relist = func(context.Context, string, string) (RelistView, error) {
		calls++
		live := view.Live["jira"]
		if calls > 1 { // after the dialog: the backend changed b
			tools := maps.Clone(live.Tools)
			tools["b"] = toolDef("b", "boolean")
			live = toolgateway.LiveSource{Tools: tools}
		}
		return RelistView{Live: live, Listed: true}, nil
	}
	before, _ := m.api.Pins.LoadFile()
	if st, out := m.approveB(t); st != 409 || out["code"] != "pin_stale" {
		t.Fatalf("%d %v, want 409 pin_stale", st, out)
	}
	if calls != 2 || len(m.shown) != 1 {
		t.Fatalf("relists %d, dialogs %d: want a relist before and after the dialog", calls, len(m.shown))
	}
	if after, _ := m.api.Pins.LoadFile(); after["jira"]["b"].Hash != before["jira"]["b"].Hash {
		t.Fatal("pins written for a tool that changed during the dialog")
	}
	// Unchanged across the dialog: approved.
	calls = -100
	if st, out := m.approveB(t); st != 200 {
		t.Fatalf("unchanged: %d %v", st, out)
	}
}

// The dialog shows each changed field with its value before and after,
// long values cut with an ellipsis, every field listed (a batch that does
// not fit is refused, never shortened by dropping fields).
func TestChangeSummary_BeforeAndAfter(t *testing.T) {
	long := strings.Repeat("a", 200)
	ch := toolgateway.Change{Kind: toolgateway.KindChanged, Fields: []toolgateway.FieldChange{
		{Path: "inputSchema.properties.limit.maximum", Before: "50", After: "1000"},
		{Path: "description", Before: `"` + long + `"`, After: `"short"`},
		{Path: "inputSchema.properties.q", After: `{"type":"string"}`},
		{Path: "annotations.readOnlyHint", Before: "true"},
		{Path: "a.b", Before: "1", After: "2"},
		{Path: "annotations.x-aimux-base", Before: `"https://a"`, After: `"https://b"`},
	}}
	got := changeSummary(ch)
	for _, want := range []string{
		"inputSchema.properties.limit.maximum: 50 -> 1000",
		`description: "` + strings.Repeat("a", 58) + "… -> \"short\"",
		`inputSchema.properties.q: (none) -> {"type":"string"}`,
		"annotations.readOnlyHint: true -> (none)",
		"a.b: 1 -> 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "x-aimux") || strings.Contains(got, "more") {
		t.Errorf("summary shows aimux's own annotations or hides fields:\n%s", got)
	}
}
