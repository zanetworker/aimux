package remoteapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/autoroute"
	"github.com/zanetworker/aimux/internal/decide"
)

// routeRequest is POST /v1/route: the question and the three facts the
// client knows about the page. Nothing else may be sent.
type routeRequest struct {
	Text  string `json:"text"`
	Facts struct {
		SitePrivate  bool `json:"sitePrivate"`
		PageReadable bool `json:"pageReadable"`
		UsePage      bool `json:"usePage"`
	} `json:"facts"`
}

// RouteResult answers POST /v1/route (autoroute.Result). Mode and Profile
// are "" when no mode is in the client's scope, for source
// override_blocked, and for reason steering. Reason says why the answer is
// undecided (override_blocked's sentence, steering, or the decider's
// reason such as no_key). DecisionID is set when the model was asked, for
// `routed` on the session that follows.
type RouteResult struct {
	Mode          string             `json:"mode"`
	Profile       string             `json:"profile"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Decided       bool               `json:"decided"`
	Source        string             `json:"source"`
	Reason        string             `json:"reason,omitempty"`
	Text          string             `json:"text"`
	DecisionID    string             `json:"decisionId,omitempty"`
}

// routesPerMinute caps each client's POST /v1/route calls that ask the
// model (overrides are free). Every paired device of a client shares the
// client's one budget.
const routesPerMinute = 60

var errRouteLimit = errors.New("this client is routing too many questions; try again shortly")

// route picks the mode for a question (autoroute.Route) among the modes
// whose profile the client may use and the Mac can run. It never fails for
// want of a decider: the answer is then undecided, for the client to ask
// the person. The audit line gets the question's SHA-256, never its text.
func (a *API) route(w http.ResponseWriter, r *http.Request) {
	var req routeRequest
	if err := decodeStrict(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("body must be JSON with text and facts, at most 64 KiB: %w", err))
		return
	}
	_, rest, override := autoroute.ParseOverride(req.Text)
	if strings.TrimSpace(req.Text) == "" || override && rest == "" {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("text is empty")) // nothing to ask, with or without an override
		return
	}
	client := a.clientOf(r)
	// An override never reaches the model, so it costs nothing and is not counted.
	if !override {
		if wait, ok := a.routeCalls.admit(client, a.now()); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "client_limit", errRouteLimit)
			return
		}
	}
	facts := autoroute.Facts{SitePrivate: req.Facts.SitePrivate, PageReadable: req.Facts.PageReadable, UsePage: req.Facts.UsePage}
	res := autoroute.Route(r.Context(), a.ModeRouter, req.Text, facts, a.routeProfiles(client))
	sum := sha256.Sum256([]byte(req.Text))
	noteRoute(w, r, routeAudit{questionSHA: hex.EncodeToString(sum[:]), decisionID: res.DecisionID, source: res.Source})
	if !res.Decided {
		// The serve log (host.log for the serve Wingman starts) says why
		// Auto asked the person and where the time went; never the question.
		a.logf("route: undecided reason=%s source=%s key_ms=%d request_ms=%d total_ms=%d backend=%s decision=%s",
			orDash(res.Reason), res.Source, res.KeyMs, res.CallMs, res.Ms, orDash(res.Backend), orDash(res.DecisionID))
	}
	probs := res.Probabilities
	if probs == nil {
		probs = map[string]float64{}
	}
	writeJSON(w, http.StatusOK, RouteResult{Mode: res.Mode, Profile: res.Profile, Confidence: res.Confidence, Probabilities: probs,
		Decided: res.Decided, Source: res.Source, Reason: res.Reason, Text: res.Text, DecisionID: res.DecisionID})
}

// orDash is s, or "-" when empty, so a log field is never blank.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// routeProfiles are the profiles client may ask for that the Mac can run,
// sorted; none when the catalog cannot be read.
func (a *API) routeProfiles(client string) []string {
	if a.Profiles == nil {
		return nil
	}
	all, err := a.Profiles()
	if err != nil {
		a.logf("route: profiles: %v", err)
		return nil
	}
	var out []string
	for _, m := range all {
		if m.Available && a.profileAllowed(client, m) {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

// routedRequest is `routed` on POST /v1/sessions: the /v1/route answer the
// client acted on. Only DecisionID is used: the session's own profile says
// which mode it finally ran in.
type routedRequest struct {
	DecisionID string `json:"decisionId"`
	Mode       string `json:"mode"`
	Source     string `json:"source"`
}

// routeFeedback records which mode a routed session finally ran in
// (decide.Log.Feedback), so the decision log says whether the pick was
// kept. An id the log does not hold (an override, a decision from before a
// restart's log rotation, a made-up id) is ignored.
func (a *API) routeFeedback(client, profileName string, routed *routedRequest) {
	if routed == nil || routed.DecisionID == "" || a.ModeRouter == nil || a.ModeRouter.Log == nil {
		return
	}
	err := a.ModeRouter.Log.Feedback(routed.DecisionID, modeOfProfile(profileName), map[string]any{"client": client})
	if err != nil && !errors.Is(err, decide.ErrUnknownDecision) {
		a.logf("route feedback for decision %s: %v", routed.DecisionID, err)
	}
}

// modeOfProfile is the mode that runs under profile, or the profile's own
// name when no mode does (the session left Auto's modes).
func modeOfProfile(profile string) string {
	for mode := range autoroute.ModeQuestion.Options {
		if autoroute.ProfileOf(mode) == profile {
			return mode
		}
	}
	return profile
}

// routeAudit is what a /v1/route call adds to its audit line.
type routeAudit struct {
	questionSHA, decisionID, source string
}

// noteRoute puts ra on the request's audit line: the local note, or the
// device's recorder.
func noteRoute(w http.ResponseWriter, r *http.Request, ra routeAudit) {
	if n := noteOf(r); n != nil {
		n.routeAudit = ra
		return
	}
	if rec, ok := w.(*statusRecorder); ok {
		rec.route = ra
	}
}

// routeLimiter counts each client's /v1/route calls in the last minute.
type routeLimiter struct {
	mu    sync.Mutex
	calls map[string][]time.Time // oldest first
}

// admit counts one call of client at now, or says how long to wait.
func (l *routeLimiter) admit(client string, now time.Time) (wait time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls == nil {
		l.calls = map[string][]time.Time{}
	}
	recent := l.calls[client]
	for len(recent) > 0 && now.Sub(recent[0]) >= time.Minute {
		recent = recent[1:]
	}
	if len(recent) >= routesPerMinute {
		l.calls[client] = recent
		return recent[0].Add(time.Minute).Sub(now), false
	}
	l.calls[client] = append(recent, now)
	return 0, true
}
