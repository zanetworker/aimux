package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/jsonl"
)

// The namer writes short titles and one-sentence descriptions for sessions
// that have nothing free to show: no owner value, no Claude Code away
// summary, nothing of ours yet. It is economical by design: only sessions a
// phone client shows are queued (Want), each is named automatically once,
// up to five go in one request with small inputs and outputs, a daily limit
// caps automatic sessions, and every call's tokens are recorded under
// "naming". No session text is ever logged or returned in an error.

// DefaultModel is the model used for titles and descriptions.
const DefaultModel = "claude-haiku-5-5"

// modelIDs maps the short names config accepts to full model ids.
var modelIDs = map[string]string{
	"haiku":  DefaultModel,
	"sonnet": "claude-sonnet-5-5",
	"opus":   "claude-opus-5-5",
}

// ResolveModel maps a configured model name ("haiku", "sonnet", "opus", or
// one of their full ids; empty means haiku) to a full model id. Any other
// name gives DefaultModel and known=false, so the caller can log it once.
func ResolveModel(name string) (id string, known bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return DefaultModel, true
	}
	if id, ok := modelIDs[name]; ok {
		return id, true
	}
	for _, id := range modelIDs {
		if name == id {
			return id, true
		}
	}
	return DefaultModel, false
}

var (
	idOnlyTitle    = regexp.MustCompile(`(?i)^(continue\s*:?\s+)?(session\s*:?\s*)?([0-9a-f]{6,}(-[0-9a-f]{4,})*)$`)
	slashOnlyTitle = regexp.MustCompile(`^/[A-Za-z0-9][A-Za-z0-9:_.-]*$`)
)

// UselessTitle reports a title that says nothing about the session: empty,
// only a session id (with or without "Session"), "continue" plus an id, or
// only a slash command. Such titles may be replaced by a model title.
func UselessTitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" || t == "(no prompt)" {
		return true
	}
	if slashOnlyTitle.MatchString(t) {
		return true
	}
	m := idOnlyTitle.FindStringSubmatch(t)
	return m != nil && strings.ContainsAny(m[3], "0123456789")
}

// ShownTitle is the title the owner sees: aimux's metadata title, else
// Claude Code's own name for the session, else the first message.
func ShownTitle(s Session) string {
	switch {
	case s.Title != "":
		return s.Title
	case s.AITitle != "":
		return s.AITitle
	default:
		return s.FirstPrompt
	}
}

const (
	namingBatchSize  = 5
	namingMaxTokens  = 60 // per session in a request
	autoInputLimit   = 500
	updateInputLimit = 1200
	namingMaxAge     = 14 * 24 * time.Hour
	namingBackoff    = time.Hour
	failingAfter     = 3
	titleMaxRunes    = 80
	descMaxRunes     = 200
)

// NamingRequest is one model call: a system prompt, a user prompt and an
// output cap.
type NamingRequest struct {
	Model     string
	System    string
	Prompt    string
	MaxTokens int
}

// NamingReply is a model's answer with the tokens it cost.
type NamingReply struct {
	Text         string
	Model        string
	InputTokens  int64
	OutputTokens int64
	CostUSD      *float64 // when the backend reports it; else from pricing, or unknown
	Estimated    bool     // tokens guessed from text length
}

// NamingBackend makes one model call. Errors carry no prompt or reply text.
type NamingBackend interface {
	Complete(ctx context.Context, req NamingRequest) (NamingReply, error)
}

var (
	// ErrModelNotFound is the API's answer for a model id it does not know.
	ErrModelNotFound = errors.New("model not found")
	// ErrOwnerSet means both title and description are the owner's.
	ErrOwnerSet = errors.New("title and description are set by the owner")
	// ErrBusy means an update for the session is already running.
	ErrBusy = errors.New("an update for this session is already running")
	// ErrNoContent means the transcript has nothing to summarise.
	ErrNoContent = errors.New("the session has no messages to summarise")

	ErrUnauthorized = errors.New("API key rejected")
	ErrBadReply     = errors.New("unreadable model reply")
	ErrNoBackend    = errors.New("no API key and no claude CLI")
	ErrModelCall    = errors.New("model call failed")
	ErrTimeout      = errors.New("timed out")
)

// failureSentinel maps a model-call error to one of the sentinels above,
// whose texts are the fixed reasons safe to show anywhere.
func failureSentinel(err error) error {
	switch {
	case errors.Is(err, ErrModelNotFound):
		return ErrModelNotFound
	case errors.Is(err, ErrUnauthorized):
		return ErrUnauthorized
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return ErrTimeout
	case errors.Is(err, ErrBadReply):
		return ErrBadReply
	case errors.Is(err, ErrNoBackend), errors.Is(err, exec.ErrNotFound):
		return ErrNoBackend
	default:
		return ErrModelCall
	}
}

// failureReason is the fixed reason for a model-call error.
func failureReason(err error) string { return failureSentinel(err).Error() }

// NamerConfig wires a Namer. Lookup, Primary and Now are required in tests;
// NewNamer fills defaults for the rest.
type NamerConfig struct {
	Model       string                          // "haiku" (default), "sonnet", "opus"
	DailyLimit  int                             // automatic sessions per local day (default 20)
	Lookup      func(id string) (Session, bool) // a session by id, as the history shows it
	Primary     NamingBackend                   // the API when a key is set, else claude -p
	Fallback    NamingBackend                   // tried once with model "haiku" when Primary answers ErrModelNotFound
	RecordUsage func(cost.Usage) error          // default: cost.AppendUsage to cost.DefaultUsagePath()
	StatePath   string                          // today's counts survive a restart here ("" keeps them in memory)
	Now         func() time.Time
	Logf        func(format string, args ...any)
	BatchWait   time.Duration // how long a batch waits to fill (default 3 s)
}

// NamingStatus is what health shows.
type NamingStatus struct {
	Today     int    `json:"today"`      // automatic sessions sent today
	Limit     int    `json:"limit"`      // the daily limit for automatic sessions
	OnRequest int    `json:"on_request"` // "Update summary" calls today (not limited)
	Failing   bool   `json:"failing,omitempty"`
	Reason    string `json:"reason,omitempty"` // fixed text, set while failing
	Paused    bool   `json:"paused,omitempty"` // automatic naming waits after repeated failures
}

// SummaryResult is what "Update summary" returns: the values now stored,
// and which fields it changed or left (owner-set, or the model gave nothing
// usable).
type SummaryResult struct {
	Title       string
	Description string
	Changed     []string
	Kept        []string
}

// Namer names shown sessions in small batches. Use NewNamer, run Run in a
// goroutine, and feed it with Want.
type Namer struct {
	cfg   NamerConfig
	model string
	wake  chan struct{}

	mu        sync.Mutex
	queue     []string
	queued    map[string]bool
	backoff   map[string]time.Time
	noContent map[string]time.Time // id → LastActive when its transcript had no text
	busy      map[string]bool      // "Update summary" in flight
	day       string
	auto      int
	onRequest int
	failures  int
	reason    string
	pausedTil time.Time // after failingAfter consecutive failures, no automatic batch until then
}

// NewNamer builds a Namer from cfg, logging once when the model is unknown.
func NewNamer(cfg NamerConfig) *Namer {
	if cfg.DailyLimit <= 0 {
		cfg.DailyLimit = 20
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.BatchWait <= 0 {
		cfg.BatchWait = 3 * time.Second
	}
	if cfg.RecordUsage == nil {
		path := cost.DefaultUsagePath()
		cfg.RecordUsage = func(u cost.Usage) error { return cost.AppendUsage(path, u) }
	}
	model, known := ResolveModel(cfg.Model)
	if !known {
		cfg.Logf("naming: unknown naming model %q, using %s", cfg.Model, model)
	}
	n := &Namer{
		cfg: cfg, model: model, wake: make(chan struct{}, 1),
		queued: map[string]bool{}, backoff: map[string]time.Time{},
		noContent: map[string]time.Time{}, busy: map[string]bool{},
	}
	n.loadState()
	return n
}

// Status reports today's counts and whether calls are failing.
func (n *Namer) Status() NamingStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rollDay()
	st := NamingStatus{Today: n.auto, Limit: n.cfg.DailyLimit, OnRequest: n.onRequest}
	if n.failures >= failingAfter {
		st.Failing, st.Reason = true, n.reason
	}
	st.Paused = n.pausedTil.After(n.cfg.Now())
	return st
}

// Want queues sessions a client shows; those that need a name are sent in
// the next batch. It never blocks on the model.
func (n *Namer) Want(ids []string) {
	now := n.cfg.Now()
	var add []string
	for _, id := range ids {
		n.mu.Lock()
		n.rollDay()
		skip := n.queued[id] || n.backoff[id].After(now) || n.auto >= n.cfg.DailyLimit || n.pausedTil.After(now)
		n.mu.Unlock()
		if skip {
			continue
		}
		if s, ok := n.cfg.Lookup(id); ok && n.needsName(s, now) {
			add = append(add, id)
		}
	}
	if len(add) == 0 {
		return
	}
	n.mu.Lock()
	for _, id := range add {
		if !n.queued[id] {
			n.queued[id] = true
			n.queue = append(n.queue, id)
		}
	}
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Run sends queued sessions until ctx ends. A batch waits up to BatchWait
// for more sessions, up to five.
func (n *Namer) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.wake:
		}
		timer := time.NewTimer(n.cfg.BatchWait)
	fill:
		for n.queueLen() < namingBatchSize {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-n.wake:
			case <-timer.C:
				break fill
			}
		}
		timer.Stop()
		n.runBatch(ctx)
		if n.queueLen() > 0 {
			select {
			case n.wake <- struct{}{}:
			default:
			}
		}
	}
}

func (n *Namer) queueLen() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.queue)
}

// needsName is the automatic rule: an interactive session active in the last
// 14 days with no owner value, no away summary and nothing of ours.
func (n *Namer) needsName(s Session, now time.Time) bool {
	if s.Automated || s.IsSubagent || s.FilePath == "" || s.AwaySummary != "" {
		return false
	}
	if now.Sub(s.LastActive) > namingMaxAge {
		return false
	}
	n.mu.Lock()
	seenEmpty, empty := n.noContent[s.ID]
	n.mu.Unlock()
	if empty && seenEmpty.Equal(s.LastActive) {
		return false
	}
	m := LoadMeta(s.FilePath)
	if m.TitleBy == "owner" || m.DescriptionBy == "owner" {
		return false
	}
	return m.Title == "" && m.Description == "" && m.DescribedAt == ""
}

// namingItem is one session in a request.
type namingItem struct {
	session   Session
	input     string
	wantTitle bool
	wantDesc  bool
}

// runBatch sends up to five queued sessions in one request, within the
// daily limit.
func (n *Namer) runBatch(ctx context.Context) {
	now := n.cfg.Now()
	n.mu.Lock()
	n.rollDay()
	room := n.cfg.DailyLimit - n.auto
	if n.pausedTil.After(now) {
		room = 0 // failing: one gate for every automatic batch
	}
	if room <= 0 {
		for _, id := range n.queue {
			delete(n.queued, id)
		}
		n.queue = nil
		n.mu.Unlock()
		return
	}
	take := min(namingBatchSize, room, len(n.queue))
	ids := n.queue[:take]
	n.queue = append([]string(nil), n.queue[take:]...)
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		for _, id := range ids {
			delete(n.queued, id)
		}
		n.mu.Unlock()
	}()

	var items []namingItem
	for _, id := range ids {
		s, ok := n.cfg.Lookup(id)
		if !ok || !n.needsName(s, now) {
			continue
		}
		input := namingInput(s.FilePath, autoInputLimit, false)
		if input == "" {
			n.mu.Lock()
			n.noContent[s.ID] = s.LastActive
			n.mu.Unlock()
			continue
		}
		items = append(items, namingItem{session: s, input: input, wantTitle: UselessTitle(ShownTitle(s)), wantDesc: true})
	}
	if len(items) == 0 {
		return
	}
	n.mu.Lock()
	n.auto += len(items)
	n.saveState()
	n.mu.Unlock()

	results, err := n.call(ctx, items)
	if err != nil && ctx.Err() != nil {
		return // shutting down: no backoff, they are named another time
	}
	if err != nil {
		n.mu.Lock()
		for _, it := range items {
			n.backoff[it.session.ID] = now.Add(namingBackoff)
		}
		n.saveState()
		n.mu.Unlock()
		n.cfg.Logf("naming: %d sessions not named (%s); retry after %s", len(items), failureReason(err), namingBackoff)
		return
	}
	for i, it := range items {
		if results[i].cut {
			n.mu.Lock()
			n.backoff[it.session.ID] = now.Add(namingBackoff)
			n.saveState()
			n.mu.Unlock()
			continue
		}
		if _, _, err := n.write(it, results[i], true); err != nil {
			// Not retried on every poll: the model call is spent either way.
			n.mu.Lock()
			n.backoff[it.session.ID] = now.Add(namingBackoff)
			n.saveState()
			n.mu.Unlock()
			n.cfg.Logf("naming: session %s: %v; retry after %s", it.session.ID, err, namingBackoff)
		}
	}
}

// UpdateSummary writes a new title and description for s now, on the
// owner's request: no daily limit (but counted), no age or automated rule;
// owner-set fields stay. ErrOwnerSet when both are the owner's, ErrBusy when
// an update for s is already running.
func (n *Namer) UpdateSummary(ctx context.Context, s Session) (SummaryResult, error) {
	m := LoadMeta(s.FilePath)
	if m.TitleBy == "owner" && m.DescriptionBy == "owner" {
		return SummaryResult{Title: m.Title, Description: m.Description, Kept: []string{"title", "description"}}, ErrOwnerSet
	}
	n.mu.Lock()
	if n.busy[s.ID] {
		n.mu.Unlock()
		return SummaryResult{}, ErrBusy
	}
	n.busy[s.ID] = true
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.busy, s.ID)
		n.mu.Unlock()
	}()

	input := namingInput(s.FilePath, updateInputLimit, true)
	if input == "" {
		return SummaryResult{}, ErrNoContent
	}
	it := namingItem{session: s, input: input, wantTitle: m.TitleBy != "owner", wantDesc: m.DescriptionBy != "owner"}
	n.mu.Lock()
	n.rollDay()
	n.onRequest++
	n.saveState()
	n.mu.Unlock()

	results, err := n.call(ctx, []namingItem{it})
	if err != nil {
		return SummaryResult{}, fmt.Errorf("update summary for session %s: %w", s.ID, failureSentinel(err))
	}
	if results[0].cut {
		return SummaryResult{}, fmt.Errorf("update summary for session %s: %w", s.ID, ErrBadReply)
	}
	meta, changed, err := n.write(it, results[0], false)
	if err != nil {
		return SummaryResult{}, err
	}
	res := SummaryResult{Title: meta.Title, Description: meta.Description, Changed: changed}
	for _, f := range []string{"title", "description"} {
		if !contains(changed, f) {
			res.Kept = append(res.Kept, f)
		}
	}
	return res, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// namingResult is one session's validated answer ("" for nothing usable).
type namingResult struct {
	title, desc string
	cut         bool // the reply ended before this session's answer
}

// call sends items in one request, records its tokens and validates the
// reply; it tracks consecutive failures for Status.
func (n *Namer) call(ctx context.Context, items []namingItem) ([]namingResult, error) {
	req := buildNamingRequest(n.model, items)
	reply, err := n.complete(ctx, req)
	if err == nil {
		n.record(reply, req.Model)
		var results []namingResult
		results, err = n.parseReply(reply.Text, items)
		if err == nil {
			n.mu.Lock()
			if n.failures > 0 {
				n.failures, n.reason, n.pausedTil = 0, "", time.Time{}
				n.saveState()
			}
			n.mu.Unlock()
			return results, nil
		}
	}
	if ctx.Err() != nil {
		return nil, err // cut short by the caller (a client left, shutdown): not the model's failure
	}
	n.mu.Lock()
	n.failures++
	n.reason = failureReason(err)
	if n.failures >= failingAfter {
		n.pausedTil = n.cfg.Now().Add(namingBackoff)
		n.cfg.Logf("naming: failing (%s); automatic naming paused for %s", n.reason, namingBackoff)
	}
	n.saveState()
	n.mu.Unlock()
	return nil, err
}

// complete calls Primary, and once more through Fallback with "haiku" when
// the API does not know the model.
func (n *Namer) complete(ctx context.Context, req NamingRequest) (NamingReply, error) {
	if n.cfg.Primary == nil {
		return NamingReply{}, ErrNoBackend
	}
	reply, err := n.cfg.Primary.Complete(ctx, req)
	if errors.Is(err, ErrModelNotFound) && n.cfg.Fallback != nil {
		n.cfg.Logf("naming: model %s not found, retrying once with claude -p --model haiku", req.Model)
		req.Model = "haiku"
		return n.cfg.Fallback.Complete(ctx, req)
	}
	return reply, err
}

func (n *Namer) record(reply NamingReply, model string) {
	if reply.Model != "" {
		model = reply.Model
	}
	if id, known := ResolveModel(model); known {
		model = id // "haiku" from the CLI fallback is recorded as the model it means
	}
	u := cost.Usage{At: n.cfg.Now().UTC(), Label: "naming", Model: model, InputTokens: reply.InputTokens,
		OutputTokens: reply.OutputTokens, CostUSD: reply.CostUSD, Estimated: reply.Estimated}
	if err := n.cfg.RecordUsage(u); err != nil {
		n.cfg.Logf("naming: %v", err)
	}
}

const namingSystem = `You name coding sessions so their owner can find them again. For each session, write only the fields asked for: "title" is 3 to 6 words naming the task; "description" is one sentence of at most 20 words saying what the session is about. Reply with JSON only, no other text: {"sessions":[{"id":"1","title":"...","description":"..."}]}`

// buildNamingRequest numbers the sessions 1..n (short ids keep the prompt
// small) and caps the output at 60 tokens per session.
func buildNamingRequest(model string, items []namingItem) NamingRequest {
	var b strings.Builder
	for i, it := range items {
		var want []string
		if it.wantTitle {
			want = append(want, "title")
		}
		if it.wantDesc {
			want = append(want, "description")
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Session %d (%s):\n%s", i+1, strings.Join(want, ", "), it.input)
	}
	return NamingRequest{Model: model, System: namingSystem, Prompt: b.String(), MaxTokens: namingMaxTokens * len(items)}
}

// replySession is one session in the model's JSON reply.
type replySession struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// parseReply validates the model's JSON: ids must be ones asked for; titles
// that are themselves useless are dropped; fields not asked for are ignored.
// A reply cut short (by max_tokens) keeps its complete sessions; the others
// are marked cut. Anything unreadable is ErrBadReply. Logs carry counts,
// never text.
func (n *Namer) parseReply(text string, items []namingItem) ([]namingResult, error) {
	sessions, complete := decodeReply(text)
	if len(sessions) == 0 && !complete {
		n.cfg.Logf("naming: unreadable reply (%d bytes)", len(text))
		return nil, ErrBadReply
	}
	if !complete {
		n.cfg.Logf("naming: reply cut short; kept %d complete session(s)", len(sessions))
	}
	results := make([]namingResult, len(items))
	answered := make([]bool, len(items))
	unknown := 0
	for _, s := range sessions {
		i, err := strconv.Atoi(s.ID)
		if err != nil || i < 1 || i > len(items) {
			unknown++
			continue
		}
		it := items[i-1]
		answered[i-1] = true
		if it.wantTitle {
			if t := cleanModelText(s.Title, titleMaxRunes); !UselessTitle(t) {
				results[i-1].title = t
			} else if s.Title != "" {
				n.cfg.Logf("naming: session %s: model title was useless, kept none", it.session.ID)
			}
		}
		if it.wantDesc {
			results[i-1].desc = cleanModelText(s.Description, descMaxRunes)
		}
	}
	if unknown > 0 {
		n.cfg.Logf("naming: reply named %d unknown session(s); ignored", unknown)
	}
	for i := range results {
		results[i].cut = !complete && !answered[i]
	}
	return results, nil
}

// decodeReply reads {"sessions":[...]} from the model's text, which may be
// wrapped in prose or a code fence. complete is false when the JSON ends
// early; sessions then holds the objects read in full before the cut.
func decodeReply(text string) (sessions []replySession, complete bool) {
	start := strings.Index(text, "{")
	if start < 0 {
		return nil, false
	}
	if end := strings.LastIndex(text, "}"); end > start {
		var parsed struct {
			Sessions []replySession `json:"sessions"`
		}
		if json.Unmarshal([]byte(text[start:end+1]), &parsed) == nil {
			return parsed.Sessions, true
		}
	}
	// Cut short: walk the array and keep each object that decodes whole.
	arr := strings.Index(text[start:], "[")
	if arr < 0 {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(text[start+arr:]))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	for dec.More() {
		var s replySession
		if dec.Decode(&s) != nil {
			break
		}
		sessions = append(sessions, s)
	}
	return sessions, false
}

// cleanModelText trims quotes and whitespace, keeps one line, and caps the
// length in runes.
func cleanModelText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'` ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// write stores a result through UpdateMeta: never over an owner's value;
// an automatic write is skipped when someone else described the session
// meanwhile, and always records the attempt (DescribedAt), so a session is
// named automatically once even when the model gave nothing usable.
func (n *Namer) write(it namingItem, r namingResult, automatic bool) (Meta, []string, error) {
	var out Meta
	var changed []string
	err := UpdateMeta(it.session.FilePath, func(m *Meta) {
		defer func() { out = *m }()
		if automatic && (m.DescribedAt != "" || m.TitleBy == "owner" || m.DescriptionBy == "owner") {
			return
		}
		if it.wantTitle && r.title != "" && m.TitleBy != "owner" {
			m.Title, m.TitleBy = r.title, "model"
			changed = append(changed, "title")
		}
		if it.wantDesc && r.desc != "" && m.DescriptionBy != "owner" {
			m.Description, m.DescriptionBy = r.desc, "model"
			changed = append(changed, "description")
		}
		m.DescribedAt = n.cfg.Now().UTC().Format(time.RFC3339)
		m.DescribedTurns = it.session.TurnCount
	})
	if err != nil {
		return Meta{}, nil, fmt.Errorf("save naming for session %s: %w", it.session.ID, err)
	}
	return out, changed, nil
}

// rollDay resets today's counts when the local day changed. Hold n.mu.
func (n *Namer) rollDay() {
	today := n.cfg.Now().Local().Format("2006-01-02")
	if n.day != today {
		n.day, n.auto, n.onRequest = today, 0, 0
	}
}

// namingState is what survives a restart: today's counts, the failing state
// with its pause, and the sessions still backing off.
type namingState struct {
	Day         string               `json:"day"`
	Automatic   int                  `json:"automatic"`
	OnRequest   int                  `json:"on_request"`
	Failures    int                  `json:"failures,omitempty"`
	Reason      string               `json:"reason,omitempty"`
	PausedUntil time.Time            `json:"paused_until,omitempty"`
	Backoff     map[string]time.Time `json:"backoff,omitempty"`
}

func (n *Namer) loadState() {
	n.rollDay()
	if n.cfg.StatePath == "" {
		return
	}
	data, err := os.ReadFile(n.cfg.StatePath) // #nosec G304 -- aimux's own state file
	if err != nil {
		return
	}
	var st namingState
	if json.Unmarshal(data, &st) != nil {
		return
	}
	if st.Day == n.day {
		n.auto, n.onRequest = st.Automatic, st.OnRequest
	}
	n.failures, n.reason, n.pausedTil = st.Failures, st.Reason, st.PausedUntil
	for id, until := range st.Backoff {
		n.backoff[id] = until
	}
}

// saveState keeps today's counts across restarts. Hold n.mu.
func (n *Namer) saveState() {
	if n.cfg.StatePath == "" {
		return
	}
	now := n.cfg.Now()
	st := namingState{Day: n.day, Automatic: n.auto, OnRequest: n.onRequest,
		Failures: n.failures, Reason: n.reason, PausedUntil: n.pausedTil, Backoff: map[string]time.Time{}}
	for id, until := range n.backoff {
		if until.After(now) {
			st.Backoff[id] = until
		} else {
			delete(n.backoff, id)
		}
	}
	data, _ := json.Marshal(st)
	// temp file in the same folder, 0600, synced, renamed over the state:
	// a write that fails part way never leaves a truncated state behind
	if err := os.MkdirAll(filepath.Dir(n.cfg.StatePath), 0o700); err == nil {
		if err = replaceFile(n.cfg.StatePath, data, 0o600); err == nil {
			return
		}
	}
	n.cfg.Logf("naming: could not save its state to %s", n.cfg.StatePath)
}

// namingInput reads a transcript and returns, capped at limit characters,
// the first user message and the start of the latest reply. For "Update
// summary" (update) it adds the latest user message and needs no reply; an
// automatic input is "" until the session has a reply.
func namingInput(path string, limit int, update bool) string {
	f, err := os.Open(path) // #nosec G304 -- a session file from the history
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var first, lastUser, lastReply string
	sc := jsonl.NewScanner(f)
	for sc.Scan() {
		var e struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message *struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Message == nil || e.IsMeta {
			continue
		}
		switch {
		case e.Message.Role == "user" && (e.Type == "user" || e.Type == ""):
			raw := allUserText(e.Message.Content)
			if machinePrompt(raw) {
				continue
			}
			if t := plainText(raw); t != "" {
				if first == "" {
					first = t
				}
				lastUser = t
			}
		case e.Message.Role == "assistant":
			if t := plainText(extractTextFromContent(e.Message.Content)); t != "" {
				lastReply = t
			}
		}
	}
	if first == "" || (!update && lastReply == "") {
		return ""
	}
	var b strings.Builder
	b.WriteString("First message: " + capRunes(first, limit*3/5))
	if update && lastUser != first {
		b.WriteString("\nLatest message: " + capRunes(lastUser, limit/4))
	}
	if lastReply != "" {
		b.WriteString("\nLatest reply: " + lastReply)
	}
	return capRunes(b.String(), limit)
}

// plainText drops XML-like tags and collapses whitespace.
func plainText(s string) string {
	return strings.Join(strings.Fields(stripXMLTags(s)), " ")
}

func capRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// ShownDescription is the description the owner sees, and where it came
// from: the owner's ("owner"); else Claude Code's away summary
// ("away_summary") when there is none of ours or the recap is newer than
// ours; else ours ("model"). An unreadable DescribedAt counts as older.
func ShownDescription(s Session) (text, source string) {
	if s.DescriptionBy == "owner" && s.Description != "" {
		return s.Description, "owner"
	}
	if s.AwaySummary != "" {
		ours, err := time.Parse(time.RFC3339, s.DescribedAt)
		if s.Description == "" || err != nil || s.AwaySummaryAt.After(ours) {
			return s.AwaySummary, "away_summary"
		}
	}
	if s.Description != "" {
		return s.Description, "model"
	}
	return "", ""
}
