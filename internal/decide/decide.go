// Package decide is aimux's one decision client: it asks a small model to
// pick one option for a question about some state, and returns the choice
// with its probabilities and whether it is confident enough to act on. It
// knows nothing about its callers' use cases; a caller supplies a Question
// (instructions and one criteria text per option), the state, and which
// options are allowed.
//
// A Decider owns the policy: API keys read first under their own budget,
// then backends tried in order within one total timeout, the allowed filter, the confidence threshold, one usage line per
// answered call (via internal/cost, labelled "decide:<name>") and one line
// in the decision log. Ask never returns an error: anything that goes wrong
// is an undecided Answer saying why. API keys are never logged, and neither
// errors nor logs carry the state.
package decide

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
)

// Why an Answer is undecided ("" when it is decided).
const (
	UndecidedLowConfidence = "low_confidence" // a backend answered below the threshold, or with no allowed option
	UndecidedError         = "error"          // every backend failed
	UndecidedTimeout       = "timeout"        // the total timeout passed first
	UndecidedCanceled      = "canceled"       // the caller's context ended first (the caller gave up)
	UndecidedNoKey         = "no_key"         // every backend failed for want of an API key
	UndecidedKeyError      = "key_error"      // an API key could not be read (not merely absent), and no backend answered
)

// DefaultTimeout is the total time Ask allows when Decider.Timeout is zero.
const DefaultTimeout = 2 * time.Second

// DefaultKeyTimeout bounds reading the API keys when Decider.KeyTimeout is
// zero.
const DefaultKeyTimeout = 5 * time.Second

var (
	// ErrNoKey means the backend's key source has no key; no call was made.
	ErrNoKey = errors.New("no API key")
	// ErrKeyRead means the backend's key source failed (for example a locked
	// secret store); no call was made.
	ErrKeyRead = errors.New("API key could not be read")
	// ErrBadReply means the backend answered with something unreadable.
	ErrBadReply = errors.New("unreadable decision reply")
	// ErrCall means the backend's API refused or failed the call.
	ErrCall = errors.New("decision call failed")
)

// Question is what to decide: Name identifies it (and keys the answer in the
// OpenRouter Decisions API), Instructions say how to choose, and Options
// maps each option key to the criteria text that describes when it fits.
type Question struct {
	Name         string
	Instructions string
	Options      map[string]string
}

// Request is one decision: the State the question is about (marshaled to
// JSON and sent to the backend as is, so it must hold only what may leave
// the machine), the Question, and the Allowed option keys (empty means all
// of Question.Options).
type Request struct {
	State    any
	Question Question
	Allowed  []string
}

// Answer is a decision. Choice is the picked option ("" when none could be
// picked); Probabilities are over the allowed options only and sum to 1 when
// present; Decided is Confidence >= Threshold (the answering backend's
// threshold, else the Decider's), and Undecided says why not. ID keys the
// decision log line and Log.Feedback. Backend and Model name what answered,
// Ms is the time Ask took, and CostUSD is the call's cost when the backend
// reports it.
type Answer struct {
	ID            string
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
	Decided       bool
	Undecided     string
	Threshold     float64
	Backend       string
	Model         string
	Ms            int64
	CostUSD       *float64
	// KeyMs is the time spent reading API keys before the decision call
	// (outside Timeout), CallMs the time the backends took within it.
	KeyMs, CallMs int64

	// tokens the call used, for the usage line, when the backend reports them
	inputTokens, outputTokens int64
}

// Backend answers one Request. It returns the raw choice, probabilities and
// confidence over all of the Question's options (the Decider applies the
// allowed filter and the threshold), and must honor ctx. Errors carry no
// state text or API key; an error for a missing key wraps ErrNoKey, one for
// a key that could not be read wraps ErrKeyRead.
type Backend interface {
	Decide(ctx context.Context, r Request) (Answer, error)
}

// Decider asks its Backends, in order, until one answers. Name labels its
// usage lines ("decide:<Name>") and log lines. Threshold is the confidence
// at or above which an answer is Decided, unless the answering backend was
// wrapped by WithThreshold (models report confidence on different scales).
// Timeout bounds the backends' calls, all backends together, after their
// keys are read under KeyTimeout (zero means DefaultTimeout). Log, when
// set, gets one line per Ask. Usage records the answering call (default:
// cost.AppendUsage to cost.DefaultUsagePath()).
type Decider struct {
	Name      string
	Backends  []Backend
	Threshold float64
	Timeout   time.Duration
	// KeyTimeout bounds reading the backends' API keys before Timeout
	// starts (zero means DefaultKeyTimeout).
	KeyTimeout time.Duration
	Log        *Log
	Usage      func(cost.Usage) error
	// Test marks this decider's log lines "test": true (aimux router test),
	// so evaluation of real decisions can leave them out.
	Test bool
}

// Preparer is a Backend with work to do before its decision call that must
// not count against the Decider's Timeout: reading its API key, which a
// cold secret store can take seconds over. Ask runs Prepare first, within
// KeyTimeout. A Prepare error wraps ErrNoKey or ErrKeyRead like Decide's.
type Preparer interface {
	Prepare(ctx context.Context) error
}

// Warmer is a Backend that can get ready for a first decision without
// deciding anything or spending money (read its key, open its connection).
type Warmer interface {
	Warm(ctx context.Context) error
}

// Warm readies each backend that is a Warmer for a first decision (in
// order, within ctx); it decides nothing and records nothing. Failures are
// left for the first Ask to report.
func (d *Decider) Warm(ctx context.Context) {
	for _, b := range d.Backends {
		if w, ok := inner(b).(Warmer); ok {
			_ = w.Warm(ctx) // a failure shows on the first Ask, with its reason
		}
	}
}

// inner is b without a WithThreshold wrapper.
func inner(b Backend) Backend {
	if t, ok := b.(*thresholded); ok {
		return t.Backend
	}
	return b
}

// Ask decides r. It never returns an error and returns within KeyTimeout
// plus Timeout even when a backend ignores its context. The answer's probabilities are
// filtered to the allowed options and renormalized. When the backend's own
// choice is allowed it stands with the backend's confidence, judged against
// the threshold. When it is not, the choice is the renormalized argmax with
// its renormalized probability as Confidence, and the answer is never
// Decided (low_confidence): it is a suggestion for the caller to confirm. A
// failed or timed-out Ask is undecided with no choice.
func (d *Decider) Ask(ctx context.Context, r Request) Answer {
	start := time.Now()
	stateJSON, err := json.Marshal(r.State)
	var a Answer
	if err != nil {
		a = Answer{Undecided: UndecidedError}
	} else {
		a = d.ask(ctx, r)
	}
	a.ID = newID()
	a.Ms = time.Since(start).Milliseconds()
	if d.Log != nil {
		sum := sha256.Sum256(stateJSON)
		if err := d.Log.record(d.Name, hex.EncodeToString(sum[:]), a, d.Test); err != nil {
			log.Printf("decide:%s: %v", d.Name, err)
		}
	}
	return a
}

// ask reads the backends' keys (prepare), then runs the backend chain
// within Timeout and applies the allowed filter and threshold. The answer
// carries the time of each stage.
func (d *Decider) ask(ctx context.Context, r Request) Answer {
	start := time.Now()
	prepErr, stop := d.prepare(ctx)
	keyMs := time.Since(start).Milliseconds()
	var a Answer
	if stop != "" {
		a = Answer{Undecided: stop}
	} else {
		called := time.Now()
		a = d.chain(ctx, r, prepErr)
		a.CallMs = time.Since(called).Milliseconds()
	}
	a.KeyMs = keyMs
	return a
}

// prepare runs each Preparer backend's Prepare within KeyTimeout, before
// the decision timeout starts, and returns each one's error (a key read
// that ran out of time is ErrKeyRead). stop is UndecidedCanceled when the
// caller gave up meanwhile.
func (d *Decider) prepare(ctx context.Context) (errs []error, stop string) {
	budget := d.KeyTimeout
	if budget <= 0 {
		budget = DefaultKeyTimeout
	}
	errs = make([]error, len(d.Backends))
	for i, b := range d.Backends {
		p, ok := inner(b).(Preparer)
		if !ok {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, budget)
		err := prepareOne(pctx, p)
		cancel()
		switch {
		case ctx.Err() != nil:
			return nil, UndecidedCanceled
		case err != nil && !errors.Is(err, ErrNoKey) && !errors.Is(err, ErrKeyRead):
			err = fmt.Errorf("%w (%w)", ErrKeyRead, err) // the key read ran out of its budget
		}
		errs[i] = err
	}
	return errs, ""
}

// prepareOne runs p.Prepare but gives up when ctx is done, so a key
// source that ignores its context cannot hold Ask past KeyTimeout.
func prepareOne(ctx context.Context, p Preparer) error {
	ch := make(chan error, 1)
	go func() { ch <- p.Prepare(ctx) }()
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// chain asks the backends in order within Timeout; a backend whose Prepare
// failed is not called and fails with that error.
func (d *Decider) chain(ctx context.Context, r Request, prepErr []error) Answer {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	keyOnly, keyRead := len(d.Backends) > 0, false
	for i, b := range d.Backends {
		threshold := d.Threshold
		if t, ok := b.(*thresholded); ok {
			threshold = t.threshold
		}
		var a Answer
		err := prepErr[i]
		if err == nil {
			a, err = call(ctx, b, r)
		}
		if ctx.Err() != nil {
			why := UndecidedTimeout
			if errors.Is(parent.Err(), context.Canceled) {
				why = UndecidedCanceled
			}
			return Answer{Undecided: why, Backend: a.Backend, Model: a.Model}
		}
		if err != nil {
			keyRead = keyRead || errors.Is(err, ErrKeyRead)
			keyOnly = keyOnly && (errors.Is(err, ErrNoKey) || errors.Is(err, ErrKeyRead))
			continue
		}
		d.recordUsage(a)
		return apply(a, allowed(r), threshold)
	}
	switch {
	case keyOnly && keyRead:
		return Answer{Undecided: UndecidedKeyError}
	case keyOnly:
		return Answer{Undecided: UndecidedNoKey}
	}
	return Answer{Undecided: UndecidedError}
}

// thresholded is a Backend judged against its own threshold.
type thresholded struct {
	Backend
	threshold float64
}

// WithThreshold makes b's answers Decided at confidence >= threshold
// instead of the Decider's threshold, for a backend whose confidence is on
// another scale (a chat model's self-reported confidence, say).
func WithThreshold(b Backend, threshold float64) Backend {
	return &thresholded{Backend: b, threshold: threshold}
}

// call runs one backend but gives up when ctx is done, so a backend that
// ignores its context cannot hold Ask past the timeout.
func call(ctx context.Context, b Backend, r Request) (Answer, error) {
	type result struct {
		a   Answer
		err error
	}
	ch := make(chan result, 1)
	go func() {
		a, err := b.Decide(ctx, r)
		ch <- result{a, err}
	}()
	select {
	case res := <-ch:
		return res.a, res.err
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

// allowed is the set of option keys the answer may pick.
func allowed(r Request) map[string]bool {
	set := map[string]bool{}
	if len(r.Allowed) == 0 {
		for k := range r.Question.Options {
			set[k] = true
		}
		return set
	}
	for _, k := range r.Allowed {
		if _, ok := r.Question.Options[k]; ok {
			set[k] = true
		}
	}
	return set
}

// apply filters a backend's answer to the allowed options, renormalizes,
// and decides against threshold. Only the backend's own allowed choice can
// be Decided: a renormalized probability is not on the backend's confidence
// scale (over two options it is always at least 0.5), so a choice that
// takes over from a filtered-out one is only a suggestion.
func apply(a Answer, ok map[string]bool, threshold float64) Answer {
	probs := map[string]float64{}
	var total float64
	for k, p := range a.Probabilities {
		if ok[k] && p > 0 {
			probs[k] = p
			total += p
		}
	}
	out := a
	out.Probabilities = nil
	if total > 0 {
		for k := range probs {
			probs[k] /= total
		}
		out.Probabilities = probs
	}
	out.Threshold = threshold
	out.Decided, out.Undecided = false, UndecidedLowConfidence
	switch {
	case ok[a.Choice]:
		out.Decided = a.Confidence >= threshold
	case total > 0:
		out.Choice = rank(probs)[0]
		out.Confidence = probs[out.Choice]
	default:
		out.Choice, out.Confidence = "", 0 // a disallowed choice and nothing else to offer (chat)
	}
	if out.Decided {
		out.Undecided = ""
	}
	return out
}

// rank orders option keys by probability, highest first (ties by key, so
// the result is stable).
func rank(probs map[string]float64) []string {
	keys := make([]string, 0, len(probs))
	for k := range probs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if probs[keys[i]] != probs[keys[j]] {
			return probs[keys[i]] > probs[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// recordUsage writes the answering call's usage line; a failed write is
// logged, not returned.
func (d *Decider) recordUsage(a Answer) {
	label := "decide:" + d.Name
	if d.Test {
		label += ":test"
	}
	u := cost.Usage{At: time.Now().UTC(), Label: label, Model: a.Model,
		InputTokens: a.inputTokens, OutputTokens: a.outputTokens, CostUSD: a.CostUSD}
	rec := d.Usage
	if rec == nil {
		rec = func(u cost.Usage) error { return cost.AppendUsage(cost.DefaultUsagePath(), u) }
	}
	if err := rec(u); err != nil {
		log.Printf("decide:%s: %v", d.Name, err)
	}
}

// newID is a random decision id.
func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return "dec_" + hex.EncodeToString(b)
}
