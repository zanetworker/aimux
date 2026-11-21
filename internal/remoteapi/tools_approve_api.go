package remoteapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/errcode"
	"github.com/zanetworker/aimux/internal/macconfirm"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// ToolsDiff is GET /v1/tools/{set}/diff: each tool of the set with its live
// definition and pin, what the client shows before it approves. No URL,
// endpoint or credential.
type ToolsDiff struct {
	Set      string     `json:"set"`
	Source   string     `json:"source"`
	Snapshot string     `json:"snapshot"`
	Tools    []ToolDiff `json:"tools"`
}

// ToolDiff is one tool of a diff. Pin is the live definition's hash, the
// value POST /v1/tools/approve takes; "" (and no Definition) when the tool
// is not listed live. Approved is the pin approved now, "" when none.
type ToolDiff struct {
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Pin        string          `json:"pin,omitempty"`
	Approved   string          `json:"approved,omitempty"`
	Definition json.RawMessage `json:"definition,omitempty"`
	// ReviewOnMac says why this tool cannot be approved from a client (its
	// source's address changed, or details the diff leaves out changed):
	// approve it with aimux tools diff and approve on the Mac.
	ReviewOnMac string `json:"reviewOnMac,omitempty"`
}

// ToolsApproveRequest is POST /v1/tools/approve's body: tools of one set,
// each with the pin of the live definition the client showed.
type ToolsApproveRequest struct {
	Set   string             `json:"set"`
	Tools []ToolApproveEntry `json:"tools"`
	// Snapshot, when given, is the diff's snapshot: a request made against
	// another one (the registry or pins changed since) is stale.
	Snapshot string `json:"snapshot,omitempty"`
}

// ToolApproveEntry is one tool to approve at Pin.
type ToolApproveEntry struct {
	Name string `json:"name"`
	Pin  string `json:"pin"`
}

// ToolsApproveResult is POST /v1/tools/approve's answer; Written is false
// when the pins already matched.
type ToolsApproveResult struct {
	Results []toolgateway.Approval `json:"results"`
	Written bool                   `json:"written"`
}

// RelistView is an approval's fresh listing of one source: the tool
// snapshot the gateway listed under and what it saw; Listed is false when
// the gateway has nothing for the source (not configured there, or its
// relist failed).
type RelistView struct {
	Version string
	Live    toolgateway.LiveSource
	Listed  bool
}

// relistTimeout bounds the fresh listing an approval waits for.
var relistTimeout = 5 * time.Second

// maxApproveTools bounds one approve request.
const maxApproveTools = 100

var (
	errApproveNotAllowed = errors.New("this client may not approve tools (tools_approve in clients.yaml)")
	errSetNotForClient   = errors.New("no profile this client may use requests that set")
	errApproveStepUp     = errors.New("approving tools needs a login in the last 60 seconds from a device kind this client allows (step_up_kinds in clients.yaml)")
)

// setForClient resolves name when a profile in client's scope requests it,
// answering the request (and false) otherwise.
func (a *API) setForClient(w http.ResponseWriter, r *http.Request, name string) (ToolsView, toolreg.Set, bool) {
	all, ok := a.loadProfiles(w)
	if !ok {
		return ToolsView{}, toolreg.Set{}, false
	}
	if a.Tools == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("the tool sets are not available"))
		return ToolsView{}, toolreg.Set{}, false
	}
	client := a.clientOf(r)
	if !a.requestsSet(client, all, name) {
		writeError(w, http.StatusForbidden, "forbidden_for_client", errSetNotForClient)
		return ToolsView{}, toolreg.Set{}, false
	}
	v := a.Tools()
	if err := v.Snapshot.Err(); err != nil {
		a.logf("tools: %v", err)
		writeError(w, http.StatusServiceUnavailable, "internal", errors.New(msgLoadFailed))
		return ToolsView{}, toolreg.Set{}, false
	}
	set, err := v.Snapshot.Registry().Resolve(name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", errors.New("not a set in the registry (a skipped set is listed with its reason in GET /v1/tools)"))
		return ToolsView{}, toolreg.Set{}, false
	}
	return v, set, true
}

// ApprovableSets is sets, in order, kept to those client may diff and
// approve under its own scope (tools_approve and requestsSet, the diff
// endpoint's checks); none when tools_approve is off:
// a run profile's roles do not widen it. Nil when the profiles cannot be
// read.
func (a *API) ApprovableSets(client string, sets []string) []string {
	if !a.Scopes.Clients[client].ToolsApprove || a.Profiles == nil { // toolsDiff's own gate
		return nil
	}
	all, err := a.Profiles()
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range sets {
		if a.requestsSet(client, all, s) {
			out = append(out, s)
		}
	}
	return out
}

// requestsSet reports whether a profile in client's scope requests set.
func (a *API) requestsSet(client string, all map[string]profile.Manifest, set string) bool {
	for _, m := range all {
		if a.profileAllowed(client, m) && slices.Contains(m.Tools, set) {
			return true
		}
	}
	return false
}

// livePin is the pin of tool's live definition under v, "" when the
// gateway does not list it.
func livePin(v ToolsView, source, tool string) (string, json.RawMessage) {
	raw, ok := v.Live[source].Tools[tool]
	if !ok {
		return "", nil
	}
	pin, err := toolgateway.PinTool(raw)
	if err != nil {
		return "", nil
	}
	def, _ := toolgateway.Canonical(clientDefinition(raw)) // PinTool succeeded on raw
	return pin, def
}

// clientDefinition is raw without aimux's own annotations (x-aimux-*: an
// http-openapi tool's base URL and path), which a client must not see; the
// pin still covers them.
func clientDefinition(raw json.RawMessage) json.RawMessage {
	var def map[string]json.RawMessage
	if json.Unmarshal(raw, &def) != nil {
		return raw
	}
	var ann map[string]json.RawMessage
	if json.Unmarshal(def["annotations"], &ann) != nil {
		return raw
	}
	for k := range ann {
		if strings.HasPrefix(k, "x-aimux-") {
			delete(ann, k)
		}
	}
	b, err := json.Marshal(ann)
	if err != nil {
		return raw
	}
	def["annotations"] = b
	out, err := json.Marshal(def)
	if err != nil {
		return raw
	}
	return out
}

// toolsDiff answers GET /v1/tools/{set}/diff, read-only: it needs the
// client's tools_approve scope but no step-up, since it changes nothing;
// POST /v1/tools/approve asks for the step-up.
func (a *API) toolsDiff(w http.ResponseWriter, r *http.Request) {
	// Definitions are shown only to a client that may approve them.
	if !a.Scopes.Clients[a.clientOf(r)].ToolsApprove {
		writeError(w, http.StatusForbidden, "forbidden_for_client", errApproveNotAllowed)
		return
	}
	name := r.PathValue("set")
	v, set, ok := a.setForClient(w, r, name)
	if !ok {
		return
	}
	snap := v.Snapshot
	status := toolStatuses(v, set.Source)
	pins := a.approvedPins()
	out := ToolsDiff{Set: name, Source: set.Source, Snapshot: snap.Version(), Tools: []ToolDiff{}}
	for _, tool := range slices.Sorted(slices.Values(set.Tools)) {
		d := ToolDiff{Name: tool, Status: status[tool]}
		if _, listed := v.Live[set.Source]; !listed && d.Status == toolgateway.StatusUnavailable {
			d.Status = toolUnknown
		}
		d.Pin, d.Definition = livePin(v, set.Source, tool)
		if rec, ok := snap.Pin(set.Source, tool); ok {
			d.Approved = rec.Hash
		}
		d.ReviewOnMac = localReview(v, pins, set.Source, tool, d.Status)
		out.Tools = append(out.Tools, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// approveTools answers POST /v1/tools/approve: the client's scope must
// allow it (tools_approve) and the request carry a fresh step-up of one of
// its step_up_kinds; then each tool's live pin must still be the one the
// client showed, or nothing is approved (409 pin_stale). It writes the
// pins as aimux tools approve does. A sandbox lease token is no login, so
// it never passes the step-up.
func (a *API) approveTools(w http.ResponseWriter, r *http.Request) {
	client := a.clientOf(r)
	c := a.Scopes.Clients[client]
	if !c.ToolsApprove {
		writeError(w, http.StatusForbidden, "forbidden_for_client", errApproveNotAllowed)
		return
	}
	// A device step-up is checked now; a mac-confirm one is asked once the
	// request is known good, just before the pins are written.
	stepUp, deviceOK := a.stepUpKind(r, c.StepUpKinds)
	macConfirm := !deviceOK && slices.Contains(c.StepUpKinds, StepUpMacConfirm) && a.MacConfirm != nil
	if !deviceOK && !macConfirm {
		writeJSON(w, http.StatusForbidden, ErrorBody{Error: errApproveStepUp.Error(), Code: "step_up_required", MaxAge: int(stepUpMaxAge / time.Second)})
		return
	}
	var req ToolsApproveRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body: %w", err))
		return
	}
	if dec.More() || dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("body: one JSON object only"))
		return
	}
	if len(req.Tools) == 0 || len(req.Tools) > maxApproveTools {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("tools: give 1 to %d tools", maxApproveTools))
		return
	}
	if a.Pins == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("approving tools is not available"))
		return
	}
	v, set, ok := a.setForClient(w, r, req.Set)
	if !ok {
		return
	}
	// A malformed request is 400 before the gateway is asked anything.
	for _, t := range req.Tools {
		switch {
		case t.Name == "" || t.Pin == "":
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("each tool needs a name and the pin GET /v1/tools/{set}/diff showed"))
			return
		case !slices.Contains(set.Tools, t.Name):
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("set %s does not list tool %q", req.Set, t.Name))
			return
		}
	}
	// A request made against another snapshot is stale whatever the
	// gateway says: tell the client before asking it.
	if req.Snapshot != "" && req.Snapshot != v.Snapshot.Version() {
		writeError(w, http.StatusConflict, "pin_stale", errors.New("the tool configuration changed since the diff; get the diff again"))
		return
	}
	// Check the pins against what the server lists now, not the gateway's
	// last listing: a tool that changed since the diff is then stale.
	if v, ok = a.freshListing(w, r, set, v); !ok {
		return
	}

	if req.Snapshot != "" && req.Snapshot != v.Snapshot.Version() {
		writeError(w, http.StatusConflict, "pin_stale", errors.New("the tool configuration changed since the diff; get the diff again"))
		return
	}
	pins := a.approvedPins()
	status := toolStatuses(v, set.Source)
	targets := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		switch {
		case t.Name == "" || t.Pin == "":
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("each tool needs a name and the pin GET /v1/tools/{set}/diff showed"))
			return
		case !slices.Contains(set.Tools, t.Name):
			writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("set %s does not list tool %q", req.Set, t.Name))
			return
		}
		if live, _ := livePin(v, set.Source, t.Name); live != t.Pin {
			writeError(w, http.StatusConflict, "pin_stale", fmt.Errorf("%s: the live definition is not the one you were shown (it changed, or is not listed now); get the diff again", t.Name))
			return
		}
		// The file read now must agree with the snapshot the status came
		// from; an approval in between makes the request stale.
		snapPin, inSnap := v.Snapshot.Pin(set.Source, t.Name)
		filePin, inFile := pins[set.Source][t.Name]
		if inSnap != inFile || snapPin.Hash != filePin.Hash {
			writeError(w, http.StatusConflict, "pin_stale", errors.New("another approval changed these tools meanwhile; get the diff again"))
			return
		}
		if why := localReview(v, pins, set.Source, t.Name, status[t.Name]); why != "" {
			writeError(w, http.StatusConflict, "review_on_mac", fmt.Errorf("%s: %s; approve it on the Mac (aimux tools diff, then aimux tools approve)", t.Name, why))
			return
		}
		targets = append(targets, req.Set+"/"+t.Name)
	}
	if macConfirm {
		text := confirmText(client, req.Set, set.Source, req.Tools, v, pins)
		if !macconfirm.Fits(text) {
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("too many tools to show in one confirmation on the Mac; approve fewer at a time"))
			return
		}
		// One dialog at a time on the Mac: a second request while one
		// waits would stack prompts.
		if !a.confirming.CompareAndSwap(false, true) {
			writeError(w, http.StatusServiceUnavailable, "confirm_pending", errors.New("an approval is already waiting for an answer on the Mac; answer it, then try again"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), (macconfirm.GiveUpAfter+10)*time.Second)
		ans, err := a.MacConfirm(ctx, macConfirmTitle, text)
		cancel()
		a.confirming.Store(false)
		if ans != macconfirm.Approved {
			if err != nil {
				a.logf("tools approve: mac-confirm for client %s: %v", client, err)
			}
			writeJSON(w, http.StatusForbidden, ErrorBody{Error: macConfirmRefusal(ans), Code: "step_up_required"})
			return
		}
		// The person approved what aimux showed; anything that changed while
		// the dialog was open makes that stale.
		if a.Tools().Snapshot.Version() != v.Snapshot.Version() {
			writeError(w, http.StatusConflict, "pin_stale", errors.New("the tool configuration changed while the approval was open on the Mac; get the diff again"))
			return
		}
		// The backend may have changed a tool while the dialog was open:
		// list it afresh again; the pins shown must still be what it lists.
		if v, ok = a.freshListing(w, r, set, v); !ok {
			return
		}
		for _, t := range req.Tools {
			if live, _ := livePin(v, set.Source, t.Name); live != t.Pin {
				writeError(w, http.StatusConflict, "pin_stale", fmt.Errorf("%s changed on its server while the approval was open on the Mac; nothing was approved, get the diff again", t.Name))
				return
			}
		}
		// Profiles are not in the snapshot: check, last before writing, that
		// the client may still use the set (its scope, the profiles that
		// request it).
		if all, err := a.Profiles(); err != nil || !a.requestsSet(client, all, req.Set) {
			writeError(w, http.StatusForbidden, "forbidden_for_client", errSetNotForClient)
			return
		}
		stepUp = StepUpMacConfirm
	}
	res, written, err := a.approvePins(r.Context(), v, targets, pins)
	if errors.Is(err, toolgateway.ErrPinsBusy) {
		writeError(w, http.StatusServiceUnavailable, "pins_busy", err)
		return
	}
	if errors.Is(err, errPinsMoved) {
		writeError(w, http.StatusConflict, "pin_stale", errors.New("another approval changed these tools meanwhile; get the diff again"))
		return
	}
	if err != nil {
		a.logf("tools approve: %v", err)
		writeError(w, http.StatusInternalServerError, "internal", errors.New("the pins could not be written; see the aimux remote serve log"))
		return
	}
	ta := toolsAudit{set: req.Set, stepUp: stepUp}
	for _, t := range req.Tools { // each pin was checked against the live definition
		ta.tools = append(ta.tools, devicelogin.ApprovedTool{Tool: t.Name, Pin: t.Pin})
	}
	noteTools(w, r, ta)
	a.logf("tools approve: client %s approved %v (written %v)", client, targets, written)
	writeJSON(w, http.StatusOK, ToolsApproveResult{Results: res, Written: written})
}

// freshListing has the gateway apply v's tool snapshot and list set's
// source afresh, and returns v with that relist's own listing of it; it
// answers the request (and false) when the listing cannot be had or the
// configuration moved. Without Relist, v is returned as is.
func (a *API) freshListing(w http.ResponseWriter, r *http.Request, set toolreg.Set, v ToolsView) (ToolsView, bool) {
	if a.Relist == nil {
		return v, true
	}
	ctx, cancel := context.WithTimeout(r.Context(), relistTimeout)
	res, err := a.Relist(ctx, set.Source, v.Snapshot.Version())
	cancel()
	before := v.Snapshot.Version()
	switch {
	case errors.Is(err, toolgateway.ErrGatewayNotRunning):
		writeError(w, http.StatusServiceUnavailable, errcode.ToolGatewayNotRunning,
			errors.New(errcode.Message(errcode.ToolGatewayNotRunning)+", then approve again"))
		return v, false
	case errors.Is(err, toolgateway.ErrRelistUnsupported):
		writeError(w, http.StatusServiceUnavailable, "gateway_backend_down",
			errors.New("the tool gateway worker runs an older aimux than remote serve: run aimux mcp proxy gateway install, then approve again"))
		return v, false
	case err == nil && a.Tools().Snapshot.Version() != before:
		writeError(w, http.StatusConflict, "pin_stale", errors.New("the tool configuration changed while checking; get the diff again"))
		return v, false
	case err == nil && !res.Listed:
		err = fmt.Errorf("the relist did not list %s", set.Source)
	case err == nil && res.Version != "" && res.Version != before:
		// The worker listed under another configuration than this
		// approval checks against (it reloads within a minute).
		err = fmt.Errorf("the tool gateway listed %s under tool snapshot %s, not %s", set.Source, res.Version, before)
	case err == nil && res.Live.Err != nil:
		err = res.Live.Err // the backend is down: unknown, not changed
	}
	if err != nil {
		a.logf("tools approve: relist of %s: %v", set.Source, err)
		// A known limit: the budget is 5 s for the whole listing, which a
		// source with many pages of tools can outlast.
		writeError(w, http.StatusServiceUnavailable, "gateway_backend_down",
			fmt.Errorf("%s did not list its tools within %v (slow or down); nothing was approved, try again", set.Source, relistTimeout))
		return v, false
	}
	// Check against what this relist saw, not a listing read later.
	v.Live = maps.Clone(v.Live)
	v.Live[set.Source] = res.Live
	return v, true
}

// toolStatuses is each tool of source's review status under v.
func toolStatuses(v ToolsView, source string) map[string]string {
	snap := v.Snapshot
	status := map[string]string{}
	for _, st := range toolgateway.Review(snap.Registry(), toolgateway.PinsOf(snap), v.Live) {
		if st.Source == source {
			status[st.Tool] = st.Status
		}
	}
	return status
}

// approvedPins is the pins file with its definitions, empty when it cannot
// be read (a changed hidden detail is then judged against no definition).
func (a *API) approvedPins() toolgateway.PinFile {
	if a.Pins == nil {
		return nil
	}
	f, err := a.Pins.LoadFile()
	if err != nil {
		return nil
	}
	return f
}

// localReview is why tool cannot be approved from a client, "" when it
// can: its source's address changed since it was approved (the pin does
// not cover the endpoint), or aimux's own annotations, which the diff
// leaves out, differ from the approved definition's (an http-openapi
// tool's base URL or path), or there is no approved definition to compare
// them with.
func localReview(v ToolsView, pins toolgateway.PinFile, source, tool, status string) string {
	if status == toolgateway.StatusSourceChanged {
		return "its source changed since it was approved (its address, or a servers.yaml server's command, executable, cwd or secret names)"
	}
	raw, ok := v.Live[source].Tools[tool]
	if !ok {
		return ""
	}
	live := aimuxAnnotations(raw)
	rec, pinned := pins[source][tool]
	var approved map[string]string
	if pinned && rec.Definition != nil {
		approved = aimuxAnnotations(rec.Definition)
	}
	switch {
	case len(live) == 0 && len(approved) == 0:
		return "" // nothing hidden, now or before
	case pinned && rec.Definition != nil && reflect.DeepEqual(approved, live):
		return ""
	case !pinned:
		return "its request details (base URL or path) are not shown here"
	}
	return "its request details (base URL or path) changed, and they are not shown here"
}

// aimuxAnnotations is def's x-aimux-* annotations.
func aimuxAnnotations(def json.RawMessage) map[string]string {
	var d struct {
		Annotations map[string]json.RawMessage `json:"annotations"`
	}
	if json.Unmarshal(def, &d) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range d.Annotations {
		if strings.HasPrefix(k, "x-aimux-") {
			out[k] = string(v)
		}
	}
	return out
}

// toolsAudit is what an approval adds to its audit line.
type toolsAudit struct {
	set    string
	tools  []devicelogin.ApprovedTool
	stepUp string // the step-up kind that allowed it
}

// StepUpMacConfirm is the step-up kind of a confirm dialog on the Mac.
const StepUpMacConfirm = clientscope.StepUpMacConfirm

// macConfirmTitle is the confirm dialog's title.
const macConfirmTitle = "aimux: approve tools"

// macConfirmRefusal is the 403 message for a dialog that did not approve.
func macConfirmRefusal(ans macconfirm.Answer) string {
	switch ans {
	case macconfirm.Cancelled:
		return "the approval was cancelled on the Mac; nothing was approved"
	case macconfirm.TimedOut:
		return "no one answered the approval on the Mac within 60 seconds; nothing was approved"
	}
	return "aimux could not ask on the Mac (no one logged in to its desktop, or the dialog could not be shown); nothing was approved"
}

// confirmText is what the Mac's dialog says: the client, the set, and for
// each tool a one-line summary of what changed, all from aimux's own diff
// of the live definitions it checked (never text from the client).
func confirmText(client, set, source string, tools []ToolApproveEntry, v ToolsView, pins toolgateway.PinFile) string {
	// By source and tool: a set asked for under an alias (wiki:readonly)
	// is diffed under its real name (notes:readonly).
	changes := map[string]toolgateway.Change{}
	for _, ch := range toolgateway.Diff(v.Snapshot.Registry(), pins, v.Live) {
		if ch.Source == source {
			changes[ch.Tool] = ch
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s asks to approve tools of %s (source %s):\n\n", client, set, source)
	for _, t := range tools {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, changeSummary(changes[t.Name]))
	}
	b.WriteString("\nApprove only if you asked for this from that client.")
	return b.String()
}

// changeSummary is what approving changes for a tool: "changed", then
// each changed field on its own line with its value before and after (cut
// to about 60 characters). Every field is listed: a batch too long for the
// dialog is refused rather than shown in part.
func changeSummary(ch toolgateway.Change) string {
	switch ch.Kind {
	case "":
		return "unchanged"
	case toolgateway.KindAdded:
		return "new, not approved before"
	}
	var lines []string
	for _, f := range ch.Fields {
		if strings.Contains(f.Path, "x-aimux-") { // aimux's own annotations stay on the Mac's review
			continue
		}
		lines = append(lines, "    "+f.Path+": "+shortValue(f.Before)+" -> "+shortValue(f.After))
	}
	switch {
	case len(lines) == 0 && ch.Note != "":
		return "changed (" + ch.Note + ")"
	case len(lines) == 0:
		return "changed"
	}
	return "changed\n" + strings.Join(lines, "\n")
}

// maxShownValue is how much of a field value the dialog shows.
const maxShownValue = 60

// shortValue is v for the dialog: "(none)" when absent, else cut to
// maxShownValue characters with an ellipsis.
func shortValue(v string) string {
	if v == "" {
		return "(none)"
	}
	if r := []rune(v); len(r) > maxShownValue {
		return string(r[:maxShownValue-1]) + "…"
	}
	return v
}

// noteTools puts ta on the request's audit line: the local note, or the
// device's recorder.
func noteTools(w http.ResponseWriter, r *http.Request, ta toolsAudit) {
	if n := noteOf(r); n != nil {
		n.tools = ta
		return
	}
	if rec, ok := w.(*statusRecorder); ok {
		rec.tools = ta
	}
}

// errPinsMoved is an approval racing another: a target's record in the
// pins file is not the one the request was checked against.
var errPinsMoved = errors.New("the pins changed since the request was checked")

// approvePins approves targets against the live definitions the pins were
// checked on, from the pins file as it is now, under the pins lock
// aimux tools approve takes too, saving it when a record changed. A target
// whose record changed since checked (another approval meanwhile) fails
// the whole request with errPinsMoved.
func (a *API) approvePins(ctx context.Context, v ToolsView, targets []string, checked toolgateway.PinFile) ([]toolgateway.Approval, bool, error) {
	reg := v.Snapshot.Registry()
	var res []toolgateway.Approval
	written, err := a.Pins.Update(func(pins toolgateway.PinFile) (toolgateway.PinFile, bool, error) {
		// A request cancelled while it waited (after Approve on the Mac,
		// for the lock) commits nothing: the client has let go.
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		for _, tg := range targets {
			set, tool, _ := strings.Cut(tg, "/")
			s, _ := reg.Resolve(set) // resolved by the handler
			if !reflect.DeepEqual(pins[s.Source][tool], checked[s.Source][tool]) {
				return nil, false, errPinsMoved
			}
		}
		out, r, err := toolgateway.Approve(reg, pins, maps.Clone(v.Live), targets, a.now())
		res = r
		return out, err == nil && !reflect.DeepEqual(out, pins), err
	})
	return res, written, err
}
