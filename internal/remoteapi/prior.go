package remoteapi

import (
	"context"
	"sort"
	"strings"

	"github.com/zanetworker/aimux/internal/profilerun"
)

// priorHistoryTurns is how many of each session's latest turns pagePrior
// reads (the most GET /v1/sessions/{id} gives).
const priorHistoryTurns = 200

// pageTurn is a turn of a page's conversation and the profile it ran in.
type pageTurn struct {
	Turn
	profile string
}

// pagePrior is the prior aimux sends when a request on a page has none:
// the page conversation is every session client's instance has on req.URL
// (any profile, with or without page text, not hidden). When its last
// answered turn (one that ended with text) ran in a profile other than
// prof, prior is the conversation's questions in time order, distinct by
// the whole question (Turn.question, not User's 8 KiB cut), compared in
// the form history stores it (profilerun.StoredForm, applied to the
// current question too; first kept), without the current question, the
// last maxPrior of them, and only then each cut to maxPriorRunes code
// points. Otherwise, and without a url or history, it is nil. A session
// whose history cannot be read is left out.
func (a *API) pagePrior(ctx context.Context, client, prof string, req newSessionRequest) []string {
	if req.URL == "" || a.Session == nil || a.ProfileSessions == nil {
		return nil
	}
	var turns []pageTurn
	for id, m := range a.ProfileSessions.List(client) {
		if m.Instance != req.Instance || m.URL != req.URL || m.Hidden {
			continue
		}
		d, err := a.Session(ctx, id, priorHistoryTurns)
		if err != nil {
			continue // no transcript yet, or unreadable: nothing to carry
		}
		for _, t := range d.Turns {
			turns = append(turns, pageTurn{Turn: t, profile: m.Profile})
		}
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].Time.Before(turns[j].Time) })
	last := -1
	for i, t := range turns {
		if strings.TrimSpace(t.Reply) != "" {
			last = i
		}
	}
	if last < 0 || turns[last].profile == prof {
		return nil
	}
	seen := map[string]bool{profilerun.StoredForm(req.Text): true}
	var qs []string
	for _, t := range turns {
		q := t.question
		if q == "" {
			q = t.User // a history source without the whole question
		}
		q = profilerun.StoredForm(q)
		if q == "" || seen[q] {
			continue
		}
		seen[q] = true
		qs = append(qs, q)
	}
	qs = qs[max(0, len(qs)-maxPrior):]
	for i, q := range qs {
		if r := []rune(q); len(r) > maxPriorRunes {
			qs[i] = string(r[:maxPriorRunes])
		}
	}
	return qs
}
