package remoteapi

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/trace"
)

// priorRig is a profile rig with research available, a fake session
// history (a.Session) and the prior each sent turn carried.
type priorRig struct {
	*profileRig
	mu      sync.Mutex
	turns   map[string][]Turn
	sent    [][]string
	earlier [][]profilerun.Exchange
	// lastEarlier is the earlier exchanges the last ask's turn carried.
	lastEarlier []profilerun.Exchange
}

const priorURL = "https://e.example/a"

var priorT0 = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

func newPriorRig(t *testing.T) *priorRig {
	t.Helper()
	pr := &priorRig{profileRig: newProfileRig(t), turns: map[string][]Turn{}}
	pr.api.Profiles = func() (map[string]profile.Manifest, error) {
		all := testProfiles()
		m := all["research"]
		m.Available = true
		all["research"] = m
		return all, nil
	}
	pr.api.Session = func(_ context.Context, id string, _ int) (SessionDetail, error) {
		pr.mu.Lock()
		defer pr.mu.Unlock()
		ts, ok := pr.turns[id]
		if !ok {
			return SessionDetail{}, errors.New("no such session")
		}
		return SessionDetail{Turns: append([]Turn(nil), ts...)}, nil
	}
	pr.f.sendF = func(ctx context.Context, _, _ string, _ func(acprun.Event)) (string, error) {
		pr.mu.Lock()
		pr.sent = append(pr.sent, profilerun.PriorFrom(ctx))
		pr.earlier = append(pr.earlier, profilerun.EarlierFrom(ctx))
		pr.mu.Unlock()
		return "end_turn", nil
	}
	return pr
}

// seed records a session of client on url (with page text, so a request
// without page text starts a new one) and its history.
func (pr *priorRig) seed(id, client, prof, instance, url string, turns ...Turn) {
	pr.t.Helper()
	m := profilerun.Meta{Profile: prof, Client: client, Instance: instance, URL: url, Created: priorT0, WithPage: true}
	if err := pr.store.Put(id, m); err != nil {
		pr.t.Fatal(err)
	}
	pr.mu.Lock()
	pr.turns[id] = turns
	pr.mu.Unlock()
}

// ask posts a question and returns the prior its turn carried.
func (pr *priorRig) ask(body string) []string {
	pr.t.Helper()
	resp, out := pr.do("POST", "/v1/sessions", body)
	if resp.StatusCode != 202 {
		pr.t.Fatalf("%s: %d %v", body, resp.StatusCode, out)
	}
	id, _ := out["id"].(string)
	pr.idle(id)
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if len(pr.sent) != 1 {
		pr.t.Fatalf("%s: %d turns sent, want 1", body, len(pr.sent))
	}
	got := pr.sent[0]
	pr.sent = nil
	pr.lastEarlier = pr.earlier[0]
	pr.earlier = nil
	return got
}

// q is a turn at minute min after priorT0.
func q(min int, user, reply string) Turn {
	return Turn{Time: priorT0.Add(time.Duration(min) * time.Minute), User: user, Reply: reply}
}

func wantPrior(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prior %q, want %q", got, want)
	}
}

func TestProfileSession_ComputesPriorOnModeChange(t *testing.T) {
	pr := newPriorRig(t)
	pr.seed("old-page", "wingman", "page", "", priorURL, q(1, "Q1", "A1"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research","url":"`+priorURL+`","text":"Q2"}`), []string{"Q1"})
}

func TestProfileSession_NoPriorSameMode(t *testing.T) {
	pr := newPriorRig(t)
	pr.seed("old-research", "wingman", "research", "", priorURL, q(1, "Q0", "A0"))
	pr.seed("old-page", "wingman", "page", "", priorURL, q(2, "Q1", "A1"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"page","url":"`+priorURL+`","text":"Q2"}`), nil)

	// No earlier conversation at all: no prior either.
	empty := newPriorRig(t)
	empty.serve()
	wantPrior(t, empty.ask(`{"profile":"research","url":"`+priorURL+`","text":"Q1"}`), nil)
}

func TestProfileSession_ExplicitPriorOverrides(t *testing.T) {
	pr := newPriorRig(t)
	pr.seed("old-page", "wingman", "page", "", priorURL, q(1, "Q1", "A1"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research","url":"`+priorURL+`","text":"Q2","prior":[]}`), nil)
	wantPrior(t, pr.ask(`{"profile":"research","url":"`+priorURL+`","text":"Q3","prior":["X"]}`), []string{"X"})
}

func TestProfileSession_PriorDistinctLast5Cut(t *testing.T) {
	pr := newPriorRig(t)
	long := strings.Repeat("é", 2500) // 2,500 code points, 5,000 bytes
	// Two sessions whose turns interleave in time; the last answered turn
	// (minute 8) ran in page.
	pr.seed("r", "wingman", "research", "", priorURL, q(1, "a", "x"), q(3, "a", "x"), q(5, "current", "x"))
	pr.seed("p", "wingman", "page", "", priorURL, q(2, "b", "x"), q(4, "c", "x"), q(6, "d", "x"), q(7, long, "x"), q(8, "e", "x"))
	pr.serve()
	got := pr.ask(`{"profile":"research","url":"` + priorURL + `","text":"current"}`)
	wantPrior(t, got, []string{"b", "c", "d", strings.Repeat("é", 2000), "e"})
}

func TestProfileSession_PriorScopedToClientInstanceURL(t *testing.T) {
	pr := newPriorRig(t)
	pr.seed("mine", "wingman", "page", "i1", priorURL, q(1, "mine", "x"))
	pr.seed("other-inst", "wingman", "page", "i2", priorURL, q(2, "other instance", "x"))
	pr.seed("legacy", "wingman", "page", "", priorURL, q(3, "no instance", "x"))
	pr.seed("other-url", "wingman", "page", "i1", priorURL+"/b", q(4, "other url", "x"))
	pr.seed("other-client", "device", "page", "i1", priorURL, q(5, "other client", "x"))
	// The page's latest answer overall is in research, but on another url.
	pr.seed("other-url-r", "wingman", "research", "i1", priorURL+"/b", q(6, "other url research", "x"))
	pr.serve()
	got := pr.ask(`{"profile":"research","url":"` + priorURL + `","instance":"i1","text":"now"}`)
	wantPrior(t, got, []string{"mine"})
}

func TestProfileSession_FailedTurnDoesNotCountAsAnswered(t *testing.T) {
	pr := newPriorRig(t)
	pr.seed("r", "wingman", "research", "", priorURL, q(1, "Q1", "A1"))
	// The later page turns failed with no text: research stays the last
	// answered mode, so a page question gets the page's questions.
	pr.seed("p", "wingman", "page", "", priorURL, q(2, "Q2", ""), q(3, "Q2b", "  \n"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"page","url":"`+priorURL+`","text":"Q3"}`), []string{"Q1", "Q2", "Q2b"})
}

// A question over the 8 KiB history cap is compared on its whole text: two
// that share the first 8 KiB stay apart, and the current question is
// excluded only when it is exactly the same. Turns come through turnOf, the
// path GET /v1/sessions/{id} uses.
func TestProfileSession_PriorLongQuestionsExact(t *testing.T) {
	head := strings.Repeat("h", maxTextLen+100)
	l1, l2, l3 := head+" one", head+" two", head+" three"
	// full is a turn as the Claude parser stores it (trimmed lines, no blank
	// lines) and turnOf serves it.
	full := func(min int, question string) Turn {
		var lines []string
		for _, l := range strings.Split(profilerun.Prompt(profilerun.AskRequest{Question: question}), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		return turnOf(trace.Turn{Timestamp: priorT0.Add(time.Duration(min) * time.Minute), UserLines: lines, OutputText: "x"})
	}
	cut := strings.Repeat("h", maxPriorRunes)

	// Asked twice and asked again now: excluded, so only "short" is left.
	pr := newPriorRig(t)
	pr.seed("p", "wingman", "page", "", priorURL, full(1, l1), full(2, l1), full(3, "short"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research","url":"`+priorURL+`","text":"`+l1+`"}`), []string{"short"})

	// Two different long questions (and a third asked now) stay distinct.
	pr2 := newPriorRig(t)
	pr2.seed("p", "wingman", "page", "", priorURL, full(1, l1), full(2, l2), full(3, l1), full(4, "short"))
	pr2.serve()
	wantPrior(t, pr2.ask(`{"profile":"research","url":"`+priorURL+`","text":"`+l3+`"}`), []string{cut, cut, "short"})

	// History keeps a question's lines trimmed and its blank lines dropped;
	// the current question is compared in that same form.
	pr3 := newPriorRig(t)
	pr3.seed("p", "wingman", "page", "", priorURL, full(1, "two\n\n lines "), full(2, "short"))
	pr3.serve()
	wantPrior(t, pr3.ask(`{"profile":"research","url":"`+priorURL+`","text":" two\n\nlines\n"}`), []string{"short"})
}

// A history source that does not normalize its questions is compared in the
// same stored form: "a \n\n b" and "a\nb" are one question.
func TestProfileSession_PriorComparesStoredForm(t *testing.T) {
	pr := newPriorRig(t)
	pr.seed("p", "wingman", "page", "", priorURL, q(1, "a \n\n b", "x"), q(2, "a\nb", "x"), q(3, "c", "x"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research","url":"`+priorURL+`","text":"z"}`), []string{"a\nb", "c"})
}

// A run profile (research-with-work) starts fresh stages every turn and
// keeps no conversation, so a follow-up in it carries the earlier
// questions even when its last answer ran in the same profile.
func TestProfileSession_RunProfileFollowUpCarriesPrior(t *testing.T) {
	pr := newPriorRig(t)
	pr.api.Profiles = func() (map[string]profile.Manifest, error) {
		all := testProfiles()
		all["research-with-work"] = profile.Manifest{Name: "research-with-work", Harness: "claude-code", Runs: "run", Available: true,
			Network: profile.NetworkSpec{Default: "deny", Allow: []string{"model"}}}
		return all, nil
	}
	pr.seed("old-run", "wingman", "research-with-work", "", priorURL, q(1, "Q1", "A1"), q(2, "Q2", "A2"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research-with-work","url":"`+priorURL+`","text":"how does that compare?"}`), []string{"Q1", "Q2"})
}

// runRig is a prior rig whose catalog has research-with-work (runs: run).
func runPriorRig(t *testing.T) *priorRig {
	pr := newPriorRig(t)
	pr.api.Profiles = func() (map[string]profile.Manifest, error) {
		all := testProfiles()
		m := all["research"]
		m.Available = true
		all["research"] = m
		all["research-with-work"] = profile.Manifest{Name: "research-with-work", Harness: "claude-code", Runs: "run", Available: true,
			Network: profile.NetworkSpec{Default: "deny", Allow: []string{"model"}}}
		return all, nil
	}
	return pr
}

// A run profile's follow-up carries the page's earlier questions and the
// answers they got (its stages keep no conversation): answered turns only,
// oldest first, the last maxPrior, each answer cut to maxEarlierAnswerRunes.
func TestProfileSession_RunProfileFollowUpCarriesEarlierAnswers(t *testing.T) {
	pr := runPriorRig(t)
	long := strings.Repeat("a", maxEarlierAnswerRunes+50)
	pr.seed("old-run", "wingman", "research-with-work", "", priorURL,
		q(1, "Q1", "A1"), q(2, "Q2 failed", ""), q(3, "Q3", "A3"), q(4, "Q4", "A4"), q(5, "Q5", "A5"), q(6, "Q6", "A6"), q(7, "Q7", long))
	pr.serve()
	pr.ask(`{"profile":"research-with-work","url":"` + priorURL + `","text":"expand on the first point"}`)
	want := []profilerun.Exchange{{Question: "Q3", Answer: "A3"}, {Question: "Q4", Answer: "A4"}, {Question: "Q5", Answer: "A5"}, {Question: "Q6", Answer: "A6"}, {Question: "Q7", Answer: long[:maxEarlierAnswerRunes]}}
	if !reflect.DeepEqual(pr.lastEarlier, want) {
		t.Fatalf("earlier %q\nwant      %q", pr.lastEarlier, want)
	}
}

// Answers go only to a run profile: a mode change into a session profile
// carries the earlier questions alone, as before.
func TestProfileSession_ModeChangeCarriesNoAnswers(t *testing.T) {
	pr := runPriorRig(t)
	pr.seed("old-run", "wingman", "research-with-work", "", priorURL, q(1, "Q1", "A1"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research","url":"`+priorURL+`","text":"and on the web?"}`), []string{"Q1"})
	if pr.lastEarlier != nil {
		t.Fatalf("earlier %q, want none for a session profile", pr.lastEarlier)
	}
}

// An explicit prior from the client replaces what aimux would compute,
// answers included.
func TestProfileSession_ExplicitPriorNoAnswers(t *testing.T) {
	pr := runPriorRig(t)
	pr.seed("old-run", "wingman", "research-with-work", "", priorURL, q(1, "Q1", "A1"))
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research-with-work","url":"`+priorURL+`","text":"next","prior":["mine"]}`), []string{"mine"})
	if pr.lastEarlier != nil {
		t.Fatalf("earlier %q, want none with an explicit prior", pr.lastEarlier)
	}
}

// An earlier question in an exchange is cut like the prior's questions.
func TestProfileSession_EarlierQuestionsCut(t *testing.T) {
	pr := runPriorRig(t)
	long := strings.Repeat("q", maxPriorRunes+10)
	pr.seed("old-run", "wingman", "research-with-work", "", priorURL, q(1, long, "A1"))
	pr.serve()
	pr.ask(`{"profile":"research-with-work","url":"` + priorURL + `","text":"next"}`)
	if len(pr.lastEarlier) != 1 || pr.lastEarlier[0].Question != long[:maxPriorRunes] {
		t.Fatalf("earlier %q, want the question cut to %d", pr.lastEarlier, maxPriorRunes)
	}
}

// A follow-up sent to the run session by id (POST /v1/sessions/{id}/messages)
// carries the same earlier exchanges as one through POST /v1/sessions.
func TestSendMessage_RunSessionCarriesEarlier(t *testing.T) {
	pr := runPriorRig(t)
	pr.seed("old-run", "wingman", "research-with-work", "", priorURL, q(1, "Q1", "A1"))
	pr.serve()
	resp, out := pr.do("POST", "/v1/sessions/old-run/messages", `{"text":"expand on that"}`)
	if resp.StatusCode != 202 {
		t.Fatalf("messages: %d %v", resp.StatusCode, out)
	}
	pr.idle("old-run")
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if len(pr.earlier) != 1 || !reflect.DeepEqual(pr.earlier[0], []profilerun.Exchange{{Question: "Q1", Answer: "A1"}}) {
		t.Fatalf("earlier %q, want Q1/A1", pr.earlier)
	}
}

// A run session without a page URL still carries its own answered turns on
// a follow-up by id: there is no page to group by, so its transcript is it.
func TestSendMessage_RunSessionWithoutURLCarriesItsOwnHistory(t *testing.T) {
	pr := runPriorRig(t)
	pr.seed("old-run", "wingman", "research-with-work", "", "", q(1, "Q1", "A1"), q(2, "Q2 failed", ""))
	pr.serve()
	resp, out := pr.do("POST", "/v1/sessions/old-run/messages", `{"text":"expand on that"}`)
	if resp.StatusCode != 202 {
		t.Fatalf("messages: %d %v", resp.StatusCode, out)
	}
	pr.idle("old-run")
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if len(pr.earlier) != 1 || !reflect.DeepEqual(pr.earlier[0], []profilerun.Exchange{{Question: "Q1", Answer: "A1"}}) {
		t.Fatalf("earlier %q, want the session's own Q1/A1", pr.earlier)
	}
}
