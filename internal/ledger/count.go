package ledger

import (
	"cmp"
	"slices"
)

// writerToolGateway is the one writer whose lines may lack a session: an
// admin-token caller has no lease, and its calls and verdicts still count.
const writerToolGateway = "tool-gateway"

// Counted is the result of applying the counting rule to a set of lines.
type Counted struct {
	Lines     []Line // the lines that count, in time order
	Unmatched []Line // secondary lines with no call id, or lines with no session
	// Orphans are secondary lines a harness reported (LearnedReported) that name a session and have no record
	// of their call in it. They are evidence, set apart, and never counted as calls.
	Orphans []Line
	Gaps    map[string][]Gap // per writer, sequence numbers missing between the lowest and highest seen
}

// Count applies the rule that gives every action exactly one line that
// counts. The result does not depend on the order of lines. Precedence when
// rules overlap: duplicate hook events are removed first, then a secondary
// tool call that a primary line covers is dropped (even with no session),
// then lines missing a call id or a session are set apart as unmatched. A
// secondary line that is the only record of its call is counted even with
// no session, and two primary lines for one call id count once. A session
// or agent line is a fact, not a call: an equal one written again is held to
// one. A secondary line that names its session is matched within it, and
// lends its record what the record lacks.
func Count(lines []Line) Counted {
	sorted := slices.Clone(lines)
	for i := range sorted {
		// One instant has one spelling, so equal lines compare equal whatever zone they arrived in.
		sorted[i].At = sorted[i].At.UTC()
	}
	slices.SortFunc(sorted, compareLines)

	out := Counted{Gaps: gaps(sorted)}

	// Rule 1 first, so a discarded delivery cannot influence anything after it. A harness's ids are only
	// unique inside its session, so an event is identified by writer, session and event id.
	type eventKey struct{ writer, session, event string }
	seenEvents := map[eventKey]bool{}
	kept := sorted[:0:0]
	for _, l := range sorted {
		if l.EventID != "" {
			k := eventKey{l.Writer, l.Session, l.EventID}
			if seenEvents[k] {
				continue
			}
			seenEvents[k] = true
		}
		kept = append(kept, l)
	}

	// A secondary line knows no session, so it can only be matched to a primary one by call id alone.
	primaryCalls := map[string]bool{}
	for _, l := range kept {
		// Only a primary line that will itself be counted may stand in for a secondary one.
		countable := l.Session != "" || l.Writer == writerToolGateway
		if l.Kind == KindToolCall && !l.Secondary && l.Call != "" && countable {
			primaryCalls[l.Call] = true
		}
	}

	type callKey struct{ session, call string }
	// A secondary line that names its session is matched within that session only: a harness's ids mean
	// nothing outside it. What it knows that the record lacks is lent to the record afterwards (rule 2).
	primaryInSession := map[callKey]bool{}
	for _, l := range kept {
		if l.Kind == KindToolCall && !l.Secondary && l.Call != "" && l.Session != "" {
			primaryInSession[callKey{l.Session, l.Call}] = true
		}
	}
	lent := map[callKey][]Line{}
	// A fact is one writer's word about one session or agent; the last one kept for each is what a new line is
	// compared with.
	type factOf struct {
		kind                   Kind
		session, agent, writer string
	}
	type fact struct{ harness, version, place, sandbox, placeRequest, startedBy, parent, agentType, learned string }
	lastFact := map[factOf]fact{}
	countedCalls := map[callKey]bool{}
	for _, l := range kept {
		if l.Kind == KindSession || l.Kind == KindAgent {
			if l.Session == "" {
				out.Unmatched = append(out.Unmatched, l) // a fact that cannot be tied to a session
				continue
			}
			// Rule 8: a fact written again unchanged (a writer that restarted) is held to one, the earliest. One
			// that differs from the last is kept, also when it returns to an earlier value.
			k := factOf{l.Kind, l.Session, l.Agent, l.Writer}
			f := fact{l.Harness, l.HarnessVersion, l.Place, l.Sandbox, l.PlaceRequest, l.StartedBy, l.AgentParent, l.AgentType, l.Learned}
			if last, seen := lastFact[k]; !seen || last != f {
				lastFact[k] = f
				out.Lines = append(out.Lines, l)
			}
			continue
		}
		if l.Kind == KindToolCall && l.Secondary {
			if l.Call == "" {
				out.Unmatched = append(out.Unmatched, l) // rule 3
				continue
			}
			if k := (callKey{l.Session, l.Call}); l.Session != "" && primaryInSession[k] {
				lent[k] = append(lent[k], l) // rule 2, within its session
				continue
			}
			if l.Session != "" && l.Learned == LearnedReported {
				// rule 9: what a harness reported about a call nothing recorded. A line on the call's own path
				// (the MCP proxy's) is a witness and falls to rule 4; a report is not.
				out.Orphans = append(out.Orphans, l)
				continue
			}
			if primaryCalls[l.Call] {
				continue // rule 2: the primary line is the record
			}
			// rule 4: the only witness of this call. It is counted even without a session, which the MCP
			// proxy never has; the call happened, and its attribution is unknown.
			if k := (callKey{l.Session, l.Call}); !countedCalls[k] {
				countedCalls[k] = true
				out.Lines = append(out.Lines, l)
			}
			continue
		}
		if l.Session == "" && l.Writer != writerToolGateway {
			out.Unmatched = append(out.Unmatched, l) // rule 5
			continue
		}
		if l.Kind == KindToolCall && l.Call != "" {
			k := callKey{l.Session, l.Call}
			if countedCalls[k] {
				continue // rule 7: two primary writers saw one invocation; the earliest is the record
			}
			countedCalls[k] = true
		}
		out.Lines = append(out.Lines, l)
	}
	// What a secondary line knows and its record lacks is lent to the record: a duration, a class of failure, the
	// harness's turn id. What the record already says is never replaced.
	for i := range out.Lines {
		l := &out.Lines[i]
		if l.Kind != KindToolCall || l.Secondary {
			continue
		}
		for _, more := range lent[callKey{l.Session, l.Call}] {
			if l.DurationMs == 0 {
				l.DurationMs = more.DurationMs
			}
			if l.ErrorType == "" {
				l.ErrorType = more.ErrorType
			}
			if l.PromptID == "" {
				l.PromptID = more.PromptID
			}
		}
	}
	return out
}

// compareLines orders lines by time, then by every other field, so that
// sorting gives the same order whatever order the lines arrived in.
func compareLines(a, b Line) int {
	return cmp.Or(
		a.At.Compare(b.At),
		cmp.Compare(a.Writer, b.Writer),
		cmp.Compare(a.Seq, b.Seq),
		cmp.Compare(a.Kind, b.Kind),
		cmp.Compare(a.EventID, b.EventID),
		cmp.Compare(a.Run, b.Run),
		cmp.Compare(a.Session, b.Session),
		cmp.Compare(a.ToSession, b.ToSession),
		cmp.Compare(a.Turn, b.Turn),
		cmp.Compare(a.Call, b.Call),
		compareBool(a.Secondary, b.Secondary),
	)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

// Gap is a run of sequence numbers a writer skipped, both ends included.
type Gap struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// gaps lists, per writer, the runs of sequence numbers missing between the
// lowest and highest seen, as ranges, so one corrupt number costs one range.
// Writers with no missing number are left out.
func gaps(lines []Line) map[string][]Gap {
	seqs := map[string][]uint64{}
	for _, l := range lines {
		seqs[l.Writer] = append(seqs[l.Writer], l.Seq)
	}
	out := map[string][]Gap{}
	for writer, seen := range seqs {
		slices.Sort(seen)
		for i := 1; i < len(seen); i++ {
			if seen[i]-seen[i-1] > 1 { // by difference: adding one to the top value would wrap
				out[writer] = append(out[writer], Gap{From: seen[i-1] + 1, To: seen[i] - 1})
			}
		}
	}
	return out
}
