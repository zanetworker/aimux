package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

// Clients by clientscope name.
const (
	clientDevice  = "device"  // the tailnet routes (iPhone, web)
	clientWingman = "wingman" // the browser extension's local socket
	clientCLI     = "cli"     // the owner's terminal: unconfined
)

// runsHost is the Manifest.Runs of profiles that run on the Mac itself
// (coding): never for Wingman, and only after a step-up for a device.
const runsHost = "host"

// ProfileInfo is one entry of GET /v1/profiles.
type ProfileInfo struct {
	Name      string `json:"name"`
	Runs      string `json:"runs"`
	Available bool   `json:"available"`
	Digest    string `json:"digest"`
	Allowed   bool   `json:"allowed"` // for the caller's client
}

// ProfileList is GET /v1/profiles.
type ProfileList struct {
	Profiles []ProfileInfo `json:"profiles"`
}

// ProfileDetail is GET /v1/profiles/{name}: the resolved manifest.
type ProfileDetail struct {
	profile.Manifest
	Digest    string `json:"digest"`
	Available bool   `json:"available"`
	Allowed   bool   `json:"allowed"`
}

// profilesOn reports whether profile sessions can be started here.
func (a *API) profilesOn() bool {
	return a.Profiles != nil && a.ProfileSessions != nil && a.NewProfileSession != nil
}

// clientOf names the request's client: the one a local socket fixed
// (LocalHandler), else "device" (the tailnet routes). Nothing a tailnet
// request carries can claim another client.
func (a *API) clientOf(r *http.Request) string {
	if c, ok := r.Context().Value(localClientKey{}).(string); ok && c != "" {
		return c // fixed by the socket (LocalHandler)
	}
	if testClientOf != nil { // set by tests only
		if c := testClientOf(a, r); c != "" {
			return c
		}
	}
	return clientDevice
}

// testClientOf is a test-only seam: tests name a request's client without a
// socket. It is nil in every non-test build.
var testClientOf func(a *API, r *http.Request) string

// profileAllowed reports whether client may ask for m: its scope, and
// never a host profile for Wingman, whatever the scopes say.
func (a *API) profileAllowed(client string, m profile.Manifest) bool {
	if client == clientWingman && m.Runs == runsHost {
		return false
	}
	return a.Scopes.Allowed(client, m.Name)
}

var errProfilesUnreadable = errors.New("the profiles could not be read on the Mac")

// loadProfiles returns the profiles, answering the request itself (and false)
// when profiles are not offered or cannot be read.
func (a *API) loadProfiles(w http.ResponseWriter) (map[string]profile.Manifest, bool) {
	if a.Profiles == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("profiles are not available"))
		return nil, false
	}
	all, err := a.Profiles()
	if err != nil {
		a.logf("profiles: %v", err)
		writeError(w, http.StatusInternalServerError, "internal", errProfilesUnreadable)
		return nil, false
	}
	return all, true
}

func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
	all, ok := a.loadProfiles(w)
	if !ok {
		return
	}
	client := a.clientOf(r)
	out := ProfileList{Profiles: []ProfileInfo{}}
	for _, m := range all {
		out.Profiles = append(out.Profiles, ProfileInfo{Name: m.Name, Runs: m.Runs, Available: m.Available,
			Digest: m.Digest(), Allowed: a.profileAllowed(client, m)})
	}
	sort.Slice(out.Profiles, func(i, j int) bool { return out.Profiles[i].Name < out.Profiles[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request) {
	all, ok := a.loadProfiles(w)
	if !ok {
		return
	}
	m, found := all[r.PathValue("name")]
	if !found {
		writeError(w, http.StatusNotFound, "unknown_profile", errUnknownProfile)
		return
	}
	writeJSON(w, http.StatusOK, ProfileDetail{Manifest: m, Digest: m.Digest(), Available: m.Available,
		Allowed: a.profileAllowed(a.clientOf(r), m)})
}

// newSessionRequest is POST /v1/sessions. Profile, URL, Title and PageText
// start a session under a profile; without Profile they are ignored.
type newSessionRequest struct {
	Cwd      string `json:"cwd"`
	Text     string `json:"text"`
	Key      string `json:"key"`
	Profile  string `json:"profile"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	PageText string `json:"pageText"`
	Model    string `json:"model"` // optional turn model: sonnet or opus
}

// checkModel answers a model choice other than "", sonnet or opus with 400
// bad_request (and false). A model is a harmless per-turn input: it never
// widens the profile.
func checkModel(w http.ResponseWriter, model string) bool {
	switch model {
	case "", profilerun.DefaultPageModel, profilerun.OpusPageModel:
		return true
	}
	writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("model must be %s or %s", profilerun.DefaultPageModel, profilerun.OpusPageModel))
	return false
}

// Body limits of POST /v1/sessions: maxSessionBody without a profile (as
// before profiles), maxProfileBody with one, room for a pageText of
// maxPageText (decoded, as Wingman caps it) even when JSON escaping makes
// each byte six.
const (
	maxPageText    = 512 << 10
	maxSessionBody = 1 << 20
	maxProfileBody = 4 << 20
)

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

var errProfileNotAllowed = errors.New("this client may not use that profile")

// newProfileSession starts (or continues, for the same client, page and
// profile) a session under req.Profile. Every check runs before anything
// starts: an error leaves no session and no turn.
func (a *API) newProfileSession(w http.ResponseWriter, r *http.Request, req newSessionRequest) {
	if len(req.PageText) > maxPageText {
		writeError(w, http.StatusRequestEntityTooLarge, "page_too_large", fmt.Errorf("pageText is longer than %d KiB", maxPageText>>10))
		return
	}
	all, ok := a.loadProfiles(w)
	if !ok {
		return
	}
	client := a.clientOf(r)
	n := noteOf(r)
	if n != nil {
		n.profile, n.urlHost = req.Profile, urlHost(req.URL)
	}
	m, found := all[req.Profile]
	if !found {
		writeError(w, http.StatusBadRequest, "unknown_profile", errUnknownProfile)
		return
	}
	if n != nil {
		n.digest = m.Digest()
	}
	if !a.profileAllowed(client, m) {
		writeError(w, http.StatusForbidden, "profile_not_allowed", errProfileNotAllowed)
		return
	}
	if client == clientDevice && m.Runs == runsHost && !a.steppedUp(w, r) {
		return
	}
	if !m.Available {
		writeError(w, http.StatusConflict, "profile_not_available", notAvailable(m))
		return
	}
	if a.ProfileSessions == nil || a.NewProfileSession == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("starting profile sessions is not available"))
		return
	}
	done, ok := a.reserveTurn(w, client)
	if !ok {
		return
	}
	id, err := a.profileSession(r.Context(), client, m, req)
	if err != nil {
		done(false)
		a.writeProfileFailure(w, req.Profile, err)
		return
	}
	turn, err := a.Broker.StartWith(id, req.Text, req.Key, TurnOptions{Model: req.Model})
	done(err == nil)
	if err != nil {
		a.writeProfileFailure(w, req.Profile, err)
		return
	}
	writeJSON(w, http.StatusAccepted, a.accepted(id, turn))
}

// createdKey is where a retry key is remembered: a device's request
// without a profile under the key itself (as before profiles), any other
// under its client, profile and page too, so one client's key never
// reaches another's session.
func createdKey(client, profile, url, key string) string {
	if client == clientDevice && profile == "" {
		return key
	}
	return client + "\x00" + profile + "\x00" + url + "\x00" + key
}

// profileSession is the session the turn goes to: the one a retried key
// made, else client's latest on the same page under the same profile, else
// a new one, recorded in ProfileSessions before any turn reaches it (an
// unrecorded session would be routed as a plain host session).
//
// The engine creation (a sandbox Ensure can take minutes) runs outside
// createMu: requests for the same page (or, without a page, the same key)
// wait for the one in flight and get its result; others never wait.
func (a *API) profileSession(ctx context.Context, client string, m profile.Manifest, req newSessionRequest) (string, error) {
	key := createdKey(client, m.Name, req.URL, req.Key)
	flightKey := ""
	switch {
	case req.URL != "":
		flightKey = createdKey(client, m.Name, req.URL, "")
	case req.Key != "":
		flightKey = key
	}
	return a.createOnce(ctx, flightKey,
		func() (string, bool) {
			if id, ok := a.created[key]; ok && req.Key != "" {
				return id, true
			}
			if req.URL != "" {
				return a.ProfileSessions.FindPage(client, req.URL, m.Name)
			}
			return "", false
		},
		func() (string, error) { return a.startProfileSession(ctx, client, m, req) },
		func(id string) {
			if req.Key != "" {
				a.remember(key, id)
			}
		})
}

// startProfileSession makes a new engine session and records it; one the
// store cannot record is cancelled.
func (a *API) startProfileSession(ctx context.Context, client string, m profile.Manifest, req newSessionRequest) (string, error) {
	meta := profilerun.Meta{Profile: m.Name, Digest: m.Digest(), Client: client, URL: req.URL, Title: req.Title, Created: a.now()}
	id, err := a.NewProfileSession(ctx, meta, req.PageText)
	if err != nil {
		return "", err
	}
	if err := a.ProfileSessions.Put(id, meta); err != nil {
		a.logf("profile session %s: not recorded, so it is cancelled and no turn is sent: %v", id, err)
		if a.CancelProfileSession != nil {
			if cerr := a.CancelProfileSession(id); cerr != nil {
				a.logf("profile session %s: cancel: %v", id, cerr)
			}
		}
		return "", errors.New("the profile session could not be recorded on the Mac")
	}
	return id, nil
}

// flight is one session creation in progress.
type flight struct {
	done chan struct{}
	id   string
	err  error
}

// createOnce returns the session a request goes to. Under createMu it asks
// lookup (a session already made) and reserves flightKey; create runs
// outside the lock. A request whose flightKey is in flight waits for that
// creation and gets its result (or gives up with ctx); a flightKey of ""
// never shares. remember runs under createMu with the new id.
func (a *API) createOnce(ctx context.Context, flightKey string, lookup func() (string, bool), create func() (string, error), remember func(id string)) (string, error) {
	a.createMu.Lock()
	if id, ok := lookup(); ok {
		a.createMu.Unlock()
		return id, nil
	}
	if f := a.flights[flightKey]; flightKey != "" && f != nil {
		a.createMu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if f.err == nil {
			a.createMu.Lock()
			remember(f.id)
			a.createMu.Unlock()
		}
		return f.id, f.err
	}
	f := &flight{done: make(chan struct{})}
	if flightKey != "" {
		if a.flights == nil {
			a.flights = map[string]*flight{}
		}
		a.flights[flightKey] = f
	}
	a.createMu.Unlock()

	f.id, f.err = create()

	a.createMu.Lock()
	if flightKey != "" {
		delete(a.flights, flightKey)
	}
	if f.err == nil {
		remember(f.id)
	}
	a.createMu.Unlock()
	close(f.done)
	return f.id, f.err
}

// remember records a retry key's session; createMu is held.
func (a *API) remember(key, id string) {
	if a.created == nil {
		a.created = map[string]string{}
	}
	a.created[key] = id
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// notAvailable is the 409 for an unavailable profile: a host profile's
// sessions start without a profile today; any other is not there yet.
func notAvailable(m profile.Manifest) error {
	if m.Runs == runsHost {
		return fmt.Errorf("%s sessions start without a profile today (POST /v1/sessions with cwd)", m.Name)
	}
	return fmt.Errorf("%s is not available yet", m.Name)
}

var errProfileStart = errors.New("the profile session could not be started on the Mac; see the aimux remote serve log")

// writeProfileFailure answers a failed profile session start or turn: a
// known failure as writeFailure does, anything else as a 500 whose body
// carries no internal error text (the detail is logged).
func (a *API) writeProfileFailure(w http.ResponseWriter, name string, err error) {
	status, code := failureStatus(err)
	if status == http.StatusInternalServerError {
		a.logf("profile %s: %v", name, err)
		writeError(w, status, code, errProfileStart)
		return
	}
	writeError(w, status, code, err)
}

// hostProfile is the scope name a session without a profile counts as: it
// runs on the host, like the coding profile.
const hostProfile = "coding"

// legacyAllowed checks a new session without a profile, answering the
// request (and false) when refused. Devices are as before profiles; a local
// client needs the host profile in its scope, and Wingman never has it.
func (a *API) legacyAllowed(w http.ResponseWriter, r *http.Request) bool {
	client := a.clientOf(r)
	if client == clientDevice {
		return !a.StepUpLegacy || a.steppedUp(w, r)
	}
	if !a.profileAllowed(client, profile.Manifest{Name: hostProfile, Runs: runsHost}) {
		writeError(w, http.StatusForbidden, "profile_not_allowed", errProfileNotAllowed)
		return false
	}
	return true
}

// stepUpMaxAge is how recent a device's login must be to start a session
// on the host.
const stepUpMaxAge = 60 * time.Second

var errStepUp = errors.New("starting a session that can change your Mac needs a login in the last 60 seconds from a device allowed for it; log in again")

// steppedUp checks the step-up for a device request that would run on the
// host: a device login under stepUpMaxAge old, from a device kind in the
// device client's StepUpKinds. It answers the request (and false) when
// refused, also without device login, where there is no login to check.
func (a *API) steppedUp(w http.ResponseWriter, r *http.Request) bool {
	var age time.Duration
	var kind string
	ok := false
	if a.DeviceLogin != nil {
		age, kind, ok = a.DeviceLogin.LoginAge(r)
	}
	if ok && age < stepUpMaxAge && slices.Contains(a.Scopes.Clients[clientDevice].StepUpKinds, kind) {
		return true
	}
	writeJSON(w, http.StatusForbidden, ErrorBody{Error: errStepUp.Error(), Code: "step_up_required", MaxAge: int(stepUpMaxAge / time.Second)})
	return false
}

var errUnknownProfile = errors.New("no profile by that name (GET /v1/profiles lists them)")

// profileListing reports whether GET /v1/sessions lists profile sessions:
// with a client or url filter, and always for a confined client, which
// never sees the host's sessions.
func (a *API) profileListing(r *http.Request) bool {
	q := r.URL.Query()
	return q.Has("client") || q.Has("url") || confined(a.clientOf(r))
}

// profileSessions are the recorded profile sessions matching the request's
// filters, newest first, at most limit. A confined client's own only.
func (a *API) profileSessions(r *http.Request, limit int) []Session {
	out := []Session{}
	if a.ProfileSessions == nil {
		return out
	}
	q := r.URL.Query()
	var clients []string
	switch caller := a.clientOf(r); {
	case confined(caller):
		clients = []string{caller}
	case q.Has("client"):
		clients = []string{q.Get("client")}
	default:
		for c := range a.Scopes.Clients {
			clients = append(clients, c)
		}
	}
	created := map[string]time.Time{}
	for _, c := range clients {
		for id, m := range a.ProfileSessions.List(c) {
			if q.Has("url") && m.URL != q.Get("url") || !history.Visible(history.Session{Hidden: m.Hidden}, history.RemoteOpts(false, false)) {
				continue
			}
			created[id] = m.Created
			out = append(out, profileEntry(id, m, a.Broker.Busy(id)))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if ci, cj := created[out[i].ID], created[out[j].ID]; !ci.Equal(cj) {
			return ci.After(cj)
		}
		return out[i].ID > out[j].ID
	})
	return out[:min(len(out), limit)]
}

// profileEntry is a profile session as the session list shows it.
func profileEntry(id string, m profilerun.Meta, busy bool) Session {
	s := Session{ID: id, Provider: "claude", Title: m.Title, LastActive: m.Created, State: StateIdle,
		Profile: m.Profile, Client: m.Client}
	if s.Title == "" {
		s.Title = m.URL
	}
	if busy {
		s.State = StateBusy
	}
	if m.URL != "" {
		s.Page = &Page{URL: m.URL, Title: m.Title}
	}
	return s
}

// hideProfileSession handles a meta edit of a profile session, and any
// edit by a local client, answering it when done is true. A local client
// may only hide its own profile sessions; a profile session can only be
// hidden (its title is the page's). Other edits fall through to the
// history record.
func (a *API) hideProfileSession(w http.ResponseWriter, r *http.Request, req metaRequest) (done bool) {
	id := r.PathValue("id")
	var m profilerun.Meta
	var found bool
	if a.ProfileSessions != nil {
		m, found = a.ProfileSessions.Get(id)
	}
	caller := a.clientOf(r)
	if confined(caller) && (!found || m.Client != caller) {
		writeError(w, http.StatusForbidden, "profile_not_allowed", errNotYourSession)
		return true
	}
	if !found {
		return false
	}
	if req.Title != nil || req.Description != nil || req.Pinned != nil {
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("a profile session can only be hidden"))
		return true
	}
	m.Hidden = true
	if err := a.ProfileSessions.Put(id, m); err != nil {
		a.logf("hide profile session %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal", errors.New("the session could not be hidden on the Mac"))
		return true
	}
	writeJSON(w, http.StatusOK, MetaResult{Title: profileEntry(id, m, false).Title, Hidden: true})
	return true
}

var errNotYourSession = errors.New("this client may change only the sessions it started")

// reserveTurn admits one turn of client under its limits, answering the
// request with 429 client_limit (and false) when it is at one. done is
// called once the turn started or failed.
func (a *API) reserveTurn(w http.ResponseWriter, client string) (done func(started bool), ok bool) {
	done, wait, ok := a.limits.reserve(client, a.Scopes, a.now(), a.runningProfileSessions)
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "client_limit", errClientLimit)
	}
	return done, ok
}

var errClientLimit = errors.New("this client is running as many questions as it may; try again shortly")

// runningProfileSessions counts client's profile sessions with a turn
// running in this API's broker.
func (a *API) runningProfileSessions(client string) int {
	n := 0
	for id := range a.ProfileSessions.List(client) {
		if a.Broker.Busy(id) {
			n++
		}
	}
	return n
}

// sessionRetry is the Retry-After of a refusal for running sessions: a
// turn usually ends within seconds.
const sessionRetry = 5 * time.Second

// confined reports whether client is held to its own sessions: every
// local client but cli (the owner's own terminal).
func confined(client string) bool {
	return client != clientDevice && client != clientCLI
}

// openToConfined are the routes a confined client may use beyond its own
// sessions' routes; every other route is 403 forbidden_for_client.
var openToConfined = map[string]bool{
	"GET /v1/health": true, "GET /v1/openapi.yaml": true,
	"GET /v1/profiles": true, "GET /v1/profiles/{name}": true,
	"GET /v1/sessions": true, "POST /v1/sessions": true,
}

// ownSessionRoutes are the session routes a confined client may use on
// the sessions it started (profilerun Meta.Client); any other session id,
// a host session or an unknown one, is 404 not_found. The chat queue and
// describe are not among them (403 forbidden_for_client).
var ownSessionRoutes = map[string]bool{
	"GET /v1/sessions/{id}":           true,
	"GET /v1/sessions/{id}/events":    true,
	"POST /v1/sessions/{id}/messages": true,
	"POST /v1/sessions/{id}/cancel":   true,
	"PATCH /v1/sessions/{id}/meta":    true,
}

var (
	errForbiddenForClient = errors.New("this client may not use this route")
	errNotFound           = errors.New("no such session for this client")
)

// localScope holds confined clients to openToConfined and to their own
// sessions, before any handler runs: nothing is read or done for a refused
// request. Device and cli requests pass through.
func (a *API) localScope(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := a.clientOf(r)
		if !confined(client) {
			mux.ServeHTTP(w, r)
			return
		}
		_, pattern := mux.Handler(r)
		session := sessionIDOf(r)
		if n := noteOf(r); n != nil { // a refusal never reaches the mux: name the route here
			n.route, n.session = pattern, session
		}
		switch {
		case openToConfined[pattern]:
		case ownSessionRoutes[pattern]:
			if !a.ownsSession(client, session) {
				writeError(w, http.StatusNotFound, "not_found", errNotFound)
				return
			}
		default:
			writeError(w, http.StatusForbidden, "forbidden_for_client", errForbiddenForClient)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// ownsSession reports whether client started session under a profile.
func (a *API) ownsSession(client, session string) bool {
	if a.ProfileSessions == nil || session == "" {
		return false
	}
	m, ok := a.ProfileSessions.Get(session)
	return ok && m.Client == client
}

var errProfileSessionsUnreadable = errors.New("the profile sessions could not be read on the Mac")

// followUpAllowed checks a follow-up turn on session against client's
// scope as it is now (the scopes may have changed since the session
// started), answering the request (and ok false) when refused. profiled
// reports whether session runs under a profile; a host session passes
// unchecked. A store or catalog that cannot be read refuses: a profile
// session must never skip the check.
func (a *API) followUpAllowed(w http.ResponseWriter, client, session string) (profiled, ok bool) {
	if a.ProfileSessions == nil || session == "" {
		return false, true
	}
	all, err := a.ProfileSessions.All()
	if err != nil {
		a.logf("follow-up on %s: %v", session, err)
		writeError(w, http.StatusInternalServerError, "internal", errProfileSessionsUnreadable)
		return false, false
	}
	meta, found := all[session]
	if !found {
		return false, true
	}
	profiles, loaded := a.loadProfiles(w)
	if !loaded {
		return true, false
	}
	m, known := profiles[meta.Profile]
	if !known || !a.profileAllowed(client, m) {
		writeError(w, http.StatusForbidden, "profile_not_allowed", errProfileNotAllowed)
		return true, false
	}
	return true, true
}

// sessionIDOf is the {id} of a /v1/sessions/{id}/... path, before the mux
// has matched the request, unescaped from the escaped path as the mux does
// (so an id holding %2F is the id the handler will see).
func sessionIDOf(r *http.Request) string {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/v1/sessions/")
	if !ok {
		return ""
	}
	seg, _, _ := strings.Cut(rest, "/")
	id, err := url.PathUnescape(seg)
	if err != nil {
		return ""
	}
	return id
}

// auditNote is what a local client's request adds to its audit line,
// filled in by the handlers.
type auditNote struct {
	action, profile, digest, urlHost string
	route, session                   string // set by localScope before the mux matches
}

type auditNoteKey struct{}

// noteOf is the request's audit note; nil for a device request.
func noteOf(r *http.Request) *auditNote {
	n, _ := r.Context().Value(auditNoteKey{}).(*auditNote)
	return n
}

// localAudit writes one audit line for each request of a local client
// (clientOf other than device), refused ones included: the client, the
// action (the route's audited action, else its pattern), the profile, its
// digest and the page's host, the session in the path, and the result.
// Never a message, a page, a token or a full URL. Device requests pass
// through; device login audits those.
func (a *API) localAudit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := a.clientOf(r)
		if client == clientDevice {
			next.ServeHTTP(w, r)
			return
		}
		n := &auditNote{}
		r = r.WithContext(context.WithValue(r.Context(), auditNoteKey{}, n))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		action := n.action
		if rec.action != "" {
			action = rec.action
		}
		if action == "" {
			action = r.Pattern // set by the mux on this request
		}
		if action == "" {
			action = n.route
		}
		if action == "" {
			action = "unknown_route"
		}
		result := "ok"
		if rec.status >= 400 {
			result = "http_" + strconv.Itoa(rec.status)
		}
		session := r.PathValue("id")
		if session == "" {
			session = n.session
		}
		a.auditLog().Write(devicelogin.AuditEntry{Client: client, Action: action, Session: session,
			Profile: n.profile, Digest: n.digest, URLHost: n.urlHost, Result: result})
	})
}

// auditLog is where local clients' lines go: Audit, else device login's;
// nil (no writer) drops them.
func (a *API) auditLog() *devicelogin.Audit {
	if a.Audit != nil {
		return a.Audit
	}
	return a.DeviceLogin.Audit()
}

// urlHost is the host of a page URL, "" when it has none.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// limiter counts each client's profile turns being started and those
// started in the last minute (clientscope.Client.MaxSessions,
// TurnsPerMinute; 0 is unlimited).
type limiter struct {
	mu       sync.Mutex
	starting map[string]int         // reserved, not yet running in the broker
	turns    map[string][]time.Time // starts in the last minute, oldest first
}

// reserve admits one turn of client at now, or says how long to wait. The
// caller calls done once the turn started (true) or failed (false); a
// failed one is not counted. running counts the client's running sessions;
// it is read under the lock, so concurrent starts cannot both pass.
func (l *limiter) reserve(client string, scopes clientscope.Scopes, now time.Time, running func(string) int) (done func(started bool), wait time.Duration, ok bool) {
	maxSessions, perMinute := scopes.Limit(client)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.starting == nil {
		l.starting, l.turns = map[string]int{}, map[string][]time.Time{}
	}
	recent := l.turns[client]
	for len(recent) > 0 && now.Sub(recent[0]) >= time.Minute {
		recent = recent[1:]
	}
	l.turns[client] = recent
	if perMinute > 0 && len(recent) >= perMinute {
		return nil, recent[0].Add(time.Minute).Sub(now), false
	}
	if maxSessions > 0 && running(client)+l.starting[client] >= maxSessions {
		return nil, sessionRetry, false
	}
	l.starting[client]++
	l.turns[client] = append(recent, now)
	var once sync.Once
	return func(started bool) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.starting[client]--
			if !started {
				ts := l.turns[client]
				if i := slices.Index(ts, now); i >= 0 {
					l.turns[client] = slices.Delete(slices.Clone(ts), i, i+1)
				}
			}
		})
	}, 0, true
}
