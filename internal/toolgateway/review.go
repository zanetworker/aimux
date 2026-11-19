package toolgateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// Tool statuses, as `aimux tools status` reports them.
const (
	StatusActive        = "active"          // pinned, and the live tool matches
	StatusUnpinned      = "unpinned"        // listed live, never approved
	StatusChanged       = "changed"         // pinned, and the live tool differs or is gone
	StatusUnavailable   = "unavailable"     // the backend is down, or lists no such tool
	StatusNotInRegistry = "not_in_registry" // pinned, but no set lists it
	StatusSourceChanged = "source_changed"  // pinned from another endpoint than the registry now names
	StatusNewOnServer   = "new_on_server"   // listed live by a registry source, in no set and not pinned
)

// Change kinds, as `aimux tools diff` reports them.
const (
	KindAdded   = "added"   // listed live, not pinned
	KindRemoved = "removed" // pinned, no longer listed
	KindChanged = "changed" // pinned and listed, with another definition
)

// Approval actions, as `aimux tools approve` reports them.
const (
	ActionApproved  = "approved"  // the live definition is now pinned
	ActionUpgraded  = "upgraded"  // a v1 pin that matches now stores its definition
	ActionRemoved   = "removed"   // the pin of a tool no longer listed is dropped
	ActionUnchanged = "unchanged" // already approved as it is live
	ActionSkipped   = "skipped"   // could not be approved (see Reason)
)

// NoteNoDefinition is the diff note for a changed v1 pin.
const NoteNoDefinition = "no reviewed definition stored (pinned before v2): showing the live definition only"

// Disclaimer is printed by every approval.
const Disclaimer = "A pin detects schema changes; it does not prove a tool is read-only."

// LiveSource is one source's live tool listing: tools by name, or Err when
// it could not be listed.
type LiveSource struct {
	Tools map[string]json.RawMessage
	Err   error
}

// LiveFromCatalog turns a catalog and its per-backend errors into live
// listings. A tool that does not decode or has no name is left out.
func LiveFromCatalog(cat *mcpproxy.Catalog, down map[string]error) map[string]LiveSource {
	out := map[string]LiveSource{}
	for s, e := range cat.Servers {
		tools := map[string]json.RawMessage{}
		for _, raw := range e.Tools {
			var n struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &n) == nil && n.Name != "" {
				tools[n.Name] = raw
			}
		}
		out[s] = LiveSource{Tools: tools}
	}
	for s, err := range down {
		out[s] = LiveSource{Err: err}
	}
	return out
}

// ToolStatus is one tool's review state. Set is empty for not_in_registry.
type ToolStatus struct {
	Set        string     `json:"set"`
	Source     string     `json:"source"`
	Tool       string     `json:"tool"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	Authorship string     `json:"authorship,omitempty"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
}

// NeedsReview is whether approving would change the tool's pin.
func (s ToolStatus) NeedsReview() bool {
	return s.Status == StatusUnpinned || s.Status == StatusChanged || s.Status == StatusSourceChanged
}

// view is one set tool's inputs: its pin, its live definition, and the
// status they give.
type view struct {
	set, source, tool string
	pin               PinRecord
	pinned            bool
	live              json.RawMessage // nil: not listed, or the source is down
	liveHash          string
	status, reason    string
	unavailable       bool // the tool could not be checked live (status unavailable before a source change)
}

// lookup is one set tool's view. A pin approved from another endpoint than
// reg now names is source_changed whatever the live state, since the pin
// says nothing about the new endpoint.
func lookup(reg toolreg.Registry, pins PinFile, live map[string]LiveSource, set, src, tool string) view {
	v := lookupLive(pins, live, set, src, tool)
	v.unavailable = v.status == StatusUnavailable
	if v.pinned && reg.SourceChanged(src, v.pin.Source) {
		v.status = StatusSourceChanged
		v.reason = fmt.Sprintf("approved from %s; tool-sources.yaml now points %s at %q", v.pin.Source, src, reg.Sources[src].Endpoint())
	}
	return v
}

func lookupLive(pins PinFile, live map[string]LiveSource, set, src, tool string) view {
	v := view{set: set, source: src, tool: tool}
	v.pin, v.pinned = pins[src][tool]
	l, listed := live[src]
	switch {
	case !listed:
		v.status, v.reason = StatusUnavailable, "backend not listed"
		return v
	case l.Err != nil:
		v.status, v.reason = StatusUnavailable, "backend down: "+clip(l.Err.Error(), 200)
		return v
	}
	raw, ok := l.Tools[tool]
	if ok {
		h, err := PinTool(raw)
		if err != nil {
			v.status, v.reason = StatusUnavailable, "live definition is malformed: "+err.Error()
			return v
		}
		v.live, v.liveHash = raw, h
	}
	switch {
	case !ok && v.pinned:
		v.status, v.reason = StatusChanged, "no longer listed by the backend"
	case !ok:
		v.status, v.reason = StatusUnavailable, "not listed by the backend"
	case !v.pinned:
		v.status, v.reason = StatusUnpinned, "never approved"
	case v.pin.Hash != v.liveHash:
		v.status, v.reason = StatusChanged, "definition differs from the approved one"
	default:
		v.status = StatusActive
	}
	return v
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// setViews is every tool of every registry set, by set name then tool name.
func setViews(reg toolreg.Registry, pins PinFile, live map[string]LiveSource) []view {
	var out []view
	for _, set := range slices.Sorted(maps.Keys(reg.Sets)) {
		s := reg.Sets[set]
		for _, t := range slices.Sorted(slices.Values(s.Tools)) {
			out = append(out, lookup(reg, pins, live, set, s.Source, t))
		}
	}
	return out
}

func approvedAt(v view) *time.Time {
	if !v.pinned || v.pin.ApprovedAt.IsZero() {
		return nil
	}
	t := v.pin.ApprovedAt
	return &t
}

// Review is the status of every tool in reg's sets, sorted by set and tool,
// followed by each pinned tool no set lists (not_in_registry), sorted by
// source and tool.
func Review(reg toolreg.Registry, pins PinFile, live map[string]LiveSource) []ToolStatus {
	var out []ToolStatus
	listed := map[string]bool{}
	for _, v := range setViews(reg, pins, live) {
		listed[v.source+"/"+v.tool] = true
		out = append(out, ToolStatus{Set: v.set, Source: v.source, Tool: v.tool, Status: v.status, Reason: v.reason, Authorship: reg.Sources[v.source].Class(), ApprovedAt: approvedAt(v)})
	}
	for _, src := range slices.Sorted(maps.Keys(pins)) {
		for _, t := range slices.Sorted(maps.Keys(pins[src])) {
			if listed[src+"/"+t] {
				continue
			}
			v := view{pin: pins[src][t], pinned: true}
			reason, class := "no set in tool-sources.yaml lists it", ""
			if s, ok := reg.Sources[src]; !ok {
				reason = "source " + src + " is not in tool-sources.yaml; the pin is kept but unused"
			} else {
				class = s.Class()
			}
			out = append(out, ToolStatus{Source: src, Tool: t, Status: StatusNotInRegistry, Reason: reason, Authorship: class, ApprovedAt: approvedAt(v)})
		}
	}
	// Tools a registry source offers that no set lists and no pin covers:
	// shown so a tool added on the server is not invisible; nothing grants
	// them.
	for _, src := range slices.Sorted(maps.Keys(live)) {
		s, ok := reg.Sources[src]
		if !ok || live[src].Err != nil {
			continue
		}
		for _, t := range slices.Sorted(maps.Keys(live[src].Tools)) {
			if _, pinned := pins[src][t]; pinned || inAnySet(reg, src, t) {
				continue
			}
			out = append(out, ToolStatus{Source: src, Tool: t, Status: StatusNewOnServer, Reason: "offered by " + src + ", not in any set", Authorship: s.Class()})
		}
	}
	return out
}

// inAnySet reports whether a set of source src lists tool.
func inAnySet(reg toolreg.Registry, src, tool string) bool {
	for _, set := range reg.Sets {
		if set.Source == src && slices.Contains(set.Tools, tool) {
			return true
		}
	}
	return false
}

// FieldChange is one difference between two definitions. Path is dotted
// (arrays as [i]); Before and After are compact JSON, empty when the field
// is absent on that side.
type FieldChange struct {
	Path   string `json:"path"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Change is one tool whose approval would change its pin. Before is the
// reviewed canonical definition (nil when added, or for a v1 pin, which
// stores none: Note then says so); After is the live canonical definition
// (nil when removed). Fields is set when both are known.
type Change struct {
	Set    string          `json:"set"`
	Source string          `json:"source"`
	Tool   string          `json:"tool"`
	Kind   string          `json:"kind"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
	Fields []FieldChange   `json:"fields,omitempty"`
	Note   string          `json:"note,omitempty"`
	// Hash is the live definition's pin (empty when removed): what
	// approving would pin, and what approve --expect checks.
	Hash string `json:"hash,omitempty"`
}

// Diff is each set tool that is unpinned, changed or source_changed, in
// Review's order; a source_changed tool is a changed one whose Note names
// both endpoints. Tools whose backend is down are not compared.
func Diff(reg toolreg.Registry, pins PinFile, live map[string]LiveSource) []Change {
	var out []Change
	for _, v := range setViews(reg, pins, live) {
		if v.unavailable || (v.status != StatusUnpinned && v.status != StatusChanged && v.status != StatusSourceChanged) {
			continue
		}
		c := Change{Set: v.set, Source: v.source, Tool: v.tool, Hash: v.liveHash}
		if v.live != nil {
			c.After, _ = Canonical(v.live) // PinTool succeeded on it in lookup
		}
		if v.pinned {
			c.Before = v.pin.Definition
		}
		switch {
		case !v.pinned:
			c.Kind = KindAdded
		case v.live == nil:
			c.Kind = KindRemoved
		default:
			c.Kind = KindChanged
		}
		if v.pinned && c.Before == nil {
			c.Note = NoteNoDefinition
		}
		if v.status == StatusSourceChanged {
			c.Note = "source changed: " + v.reason
		}
		if c.Before != nil && c.After != nil {
			fields, err := FieldDiff(c.Before, c.After)
			if err != nil {
				c.Note = "stored definition does not parse: " + err.Error()
			}
			c.Fields = fields
		}
		out = append(out, c)
	}
	return out
}

// FieldDiff lists the differences between two JSON definitions, sorted by
// path: objects are compared key by key, arrays index by index, anything
// else as a whole.
func FieldDiff(before, after json.RawMessage) ([]FieldChange, error) {
	a, err := decodeNumbers(before)
	if err != nil {
		return nil, fmt.Errorf("before: %w", err)
	}
	b, err := decodeNumbers(after)
	if err != nil {
		return nil, fmt.Errorf("after: %w", err)
	}
	var out []FieldChange
	walkDiff("", a, b, true, true, &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func decodeNumbers(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

func compact(v any) string {
	b, _ := json.Marshal(v) // decoded JSON always re-encodes
	return string(b)
}

// join appends key to path. A key that is not a plain name is quoted
// (["a.b"], control characters escaped), so it can neither look like
// nesting nor start a line of its own when printed.
func join(path, key string) string {
	if !plainKey.MatchString(key) {
		return path + "[" + strconv.Quote(key) + "]"
	}
	if path == "" {
		return key
	}
	return path + "." + key
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func walkDiff(path string, a, b any, hasA, hasB bool, out *[]FieldChange) {
	switch {
	case !hasA:
		*out = append(*out, FieldChange{Path: path, After: compact(b)})
		return
	case !hasB:
		*out = append(*out, FieldChange{Path: path, Before: compact(a)})
		return
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			av, ina := am[k]
			bv, inb := bm[k]
			walkDiff(join(path, k), av, bv, ina, inb, out)
		}
		return
	}
	aa, aok := a.([]any)
	ba, bok := b.([]any)
	if aok && bok {
		for i := 0; i < max(len(aa), len(ba)); i++ {
			var av, bv any
			if i < len(aa) {
				av = aa[i]
			}
			if i < len(ba) {
				bv = ba[i]
			}
			walkDiff(fmt.Sprintf("%s[%d]", path, i), av, bv, i < len(aa), i < len(ba), out)
		}
		return
	}
	if ca, cb := compact(a), compact(b); ca != cb {
		*out = append(*out, FieldChange{Path: path, Before: ca, After: cb})
	}
}

// Approval is what Approve did with one tool.
type Approval struct {
	Set    string `json:"set"`
	Source string `json:"source"`
	Tool   string `json:"tool"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	// Hash is the pin of the definition approved or upgraded: what it now
	// serves. Empty for the other actions.
	Hash string `json:"hash,omitempty"`
}

// ShortHash is a pin's first 12 hex digits, how the CLI names a definition.
func ShortHash(pin string) string {
	h := strings.TrimPrefix(pin, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// Approve records the live definitions of the targeted tools as reviewed and
// returns the new pins; pins is not changed. A target is a set name (all its
// tools) or set/tool; an unknown set, or a tool the set does not list, is an
// error and nothing is approved. A tool already approved as it is live keeps
// its record (no new approved_at); a v1 pin that matches gains its
// definition; a pinned tool the backend no longer lists loses its pin; a
// tool whose backend is down or that is not listed is skipped. Each approved
// record carries now and the source's endpoint.
func Approve(reg toolreg.Registry, pins PinFile, live map[string]LiveSource, targets []string, now time.Time) (PinFile, []Approval, error) {
	type target struct{ set, tool string }
	var want []target
	seen := map[target]bool{}
	for _, tg := range targets {
		set, tool, hasTool := strings.Cut(tg, "/")
		if set == "" || (hasTool && tool == "") {
			return nil, nil, fmt.Errorf("target %q: want <set> or <set>/<tool>", tg)
		}
		s, err := reg.Resolve(set)
		if err != nil {
			return nil, nil, fmt.Errorf("target %q: %w", tg, err)
		}
		tools := slices.Sorted(slices.Values(s.Tools))
		if hasTool {
			if !slices.Contains(tools, tool) {
				return nil, nil, fmt.Errorf("target %q: set %s does not list tool %q", tg, set, tool)
			}
			tools = []string{tool}
		}
		for _, t := range tools {
			if k := (target{set, t}); !seen[k] {
				seen[k] = true
				want = append(want, k)
			}
		}
	}
	out := pins.clone()
	var res []Approval
	for _, tg := range want {
		s, _ := reg.Resolve(tg.set) // resolved above
		// Each tool is judged against out, so a tool named twice through two
		// sets is approved once and then reported unchanged.
		v := lookup(reg, out, live, tg.set, s.Source, tg.tool)
		a := Approval{Set: tg.set, Source: s.Source, Tool: tg.tool}
		switch {
		case v.unavailable:
			a.Action, a.Reason = ActionSkipped, v.reason
		case v.live == nil: // pinned, no longer listed
			delete(out[s.Source], tg.tool)
			if len(out[s.Source]) == 0 {
				delete(out, s.Source)
			}
			a.Action = ActionRemoved
		case v.status == StatusActive && v.pin.Definition != nil:
			a.Action = ActionUnchanged
		default:
			def, _ := Canonical(v.live) // PinTool succeeded on it in lookup
			if out[s.Source] == nil {
				out[s.Source] = map[string]PinRecord{}
			}
			out[s.Source][tg.tool] = PinRecord{Hash: v.liveHash, Definition: def, ApprovedAt: now.UTC(), Source: reg.Sources[s.Source].Endpoint()}
			a.Action, a.Hash = ActionApproved, v.liveHash
			if v.status == StatusActive {
				a.Action = ActionUpgraded
			}
		}
		res = append(res, a)
	}
	return out, res, nil
}

// heldStatuses are the statuses a turn's notice reports: tools the owner
// can make usable by reviewing them.
var heldStatuses = map[string]string{StatusChanged: "changed", StatusUnpinned: "unpinned", StatusSourceChanged: "source changed", StatusNewOnServer: "new"}

// HeldBack is the tools of sets' sources that a turn cannot use until the
// owner reviews them (changed, unpinned, source_changed, new_on_server),
// in Review's order. Unavailable tools are left out: nothing to review.
func HeldBack(reg toolreg.Registry, pins PinFile, live map[string]LiveSource, sets []string) []ToolStatus {
	sources := map[string]bool{}
	tools := map[string]bool{} // source/tool of the requested sets
	for _, name := range sets {
		if s, err := reg.Resolve(name); err == nil {
			sources[s.Source] = true
			for _, t := range s.Tools {
				tools[s.Source+"/"+t] = true
			}
		}
	}
	var out []ToolStatus
	seen := map[string]bool{} // a tool in two sets is one tool
	for _, st := range Review(reg, pins, live) {
		key := st.Source + "/" + st.Tool
		if _, held := heldStatuses[st.Status]; !held || seen[key] {
			continue
		}
		// A set tool counts only for the requested sets; a tool no set lists
		// counts for any requested set of its source (adding it is how it
		// becomes usable there).
		if (st.Status == StatusNewOnServer && sources[st.Source]) || tools[key] {
			seen[key] = true
			out = append(out, st)
		}
	}
	return out
}

// HeldSets is the names in requested (as the profile asks for them, an
// alias such as wiki:readonly included) of the sets that list a held tool,
// sorted and once each; nil when none. A client opens the diff of each; a
// tool no set lists (new_on_server) names no set.
func HeldSets(reg toolreg.Registry, held []ToolStatus, requested []string) []string {
	var out []string
	for _, name := range requested {
		s, err := reg.Resolve(name)
		if err != nil || slices.Contains(out, name) {
			continue
		}
		if slices.ContainsFunc(held, func(st ToolStatus) bool {
			return st.Source == s.Source && slices.Contains(s.Tools, st.Tool)
		}) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// LeftOut is the servers of grant (server to granted tools) on which every
// granted tool is in held: the gateway serves the turn none of them, so the
// turn's MCP config leaves the server out. Sorted.
func LeftOut(held []ToolStatus, grant map[string][]string) []string {
	isHeld := map[string]bool{}
	for _, st := range held {
		isHeld[st.Source+"/"+st.Tool] = true
	}
	var out []string
	for _, srv := range slices.Sorted(maps.Keys(grant)) {
		tools := grant[srv]
		if len(tools) > 0 && !slices.ContainsFunc(tools, func(t string) bool { return !isHeld[srv+"/"+t] }) {
			out = append(out, srv)
		}
	}
	return out
}

// HeldBackNotice is the one line a turn shows for held: "" when nothing is
// held. A source in leftOut is named first, as left out of the turn
// ("slack: 5 tools changed, held for review, left out of this turn").
func HeldBackNotice(held []ToolStatus, leftOut []string) string {
	if len(held) == 0 {
		return ""
	}
	var order []string
	bySource := map[string][]ToolStatus{}
	for _, st := range held {
		if _, seen := bySource[st.Source]; !seen {
			order = append(order, st.Source)
		}
		bySource[st.Source] = append(bySource[st.Source], st)
	}
	var lines, parts []string
	n := 0
	for _, src := range order {
		if slices.Contains(leftOut, src) {
			lines = append(lines, src+": "+countStatuses(bySource[src])+", held for review, left out of this turn")
			continue
		}
		names := make([]string, 0, len(bySource[src]))
		for _, st := range bySource[src] {
			names = append(names, st.Tool+" "+heldStatuses[st.Status])
		}
		parts = append(parts, src+": "+strings.Join(names, ", "))
		n += len(names)
	}
	if n > 0 {
		lines = append(lines, fmt.Sprintf("%d %s held for review (%s)", n, toolNoun(n), strings.Join(parts, "; ")))
	}
	return strings.Join(lines, "; ") + ": run aimux tools status"
}

// countStatuses is held tools counted by status: "5 tools changed", or
// "2 tools (1 changed, 1 source changed)" when they differ.
func countStatuses(held []ToolStatus) string {
	var order []string
	count := map[string]int{}
	for _, st := range held {
		w := heldStatuses[st.Status]
		if count[w] == 0 {
			order = append(order, w)
		}
		count[w]++
	}
	if len(order) == 1 {
		return fmt.Sprintf("%d %s %s", len(held), toolNoun(len(held)), order[0])
	}
	parts := make([]string, 0, len(order))
	for _, w := range order {
		parts = append(parts, fmt.Sprintf("%d %s", count[w], w))
	}
	return fmt.Sprintf("%d %s (%s)", len(held), toolNoun(len(held)), strings.Join(parts, ", "))
}

func toolNoun(n int) string {
	if n == 1 {
		return "tool"
	}
	return "tools"
}
