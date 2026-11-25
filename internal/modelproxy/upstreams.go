package modelproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// A named upstream is another address that speaks the Messages API, paid for with a key the owner holds: an
// internal gateway, say. A request reaches it by naming a model as <name>:<model>, and has to carry the proxy
// key, since it spends the owner's key. Nothing of the caller's credentials goes there.
type namedUpstream struct {
	name   string
	engine *anthropicEngine
	key    func(context.Context) (string, error)
	keyAt  string // where the key is kept, in words

	mu     sync.Mutex
	cached string
	until  time.Time
}

// upstreamKeyTTL is how long an upstream's key is kept after it was read, so a secret store is not asked on
// every request. A rotated key is picked up within it, or at once when the upstream refuses the old one.
const upstreamKeyTTL = time.Minute

var upstreamName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

var errUpstreamNeedsProxyKey = errors.New(`this model is paid for with the owner's key for its upstream and needs the proxy key; start the session from a shell where you ran: eval "$(aimux model proxy env)" (with --port for a proxy on another port than the stable one)`)

// WithMessagesUpstream adds a named upstream at address, a bare origin. key reads its key when a request needs
// it; the proxy keeps it for a minute at most and writes it nowhere.
//
// keyAt says where the key is kept, in words for the owner ("the environment variable X"); empty means the
// secret-store entry <name>-key. It is what an error names, so the owner knows what to check.
func WithMessagesUpstream(name, address string, key func(context.Context) (string, error), keyAt string) (HandlerOption, error) {
	if !upstreamName.MatchString(name) || name == engineCodex || name == engineAnthropic {
		return nil, fmt.Errorf("upstream name %q: use lower-case letters, digits and hyphens, starting with a letter, and not %s or %s", name, engineCodex, engineAnthropic)
	}
	engine, err := newAnthropicEngine(address, "")
	if err != nil {
		return nil, fmt.Errorf("upstream %s: %w", name, err)
	}
	if host := engine.upstream.Hostname(); engine.upstream.Scheme != "https" && host != "localhost" && !net.ParseIP(host).IsLoopback() {
		// The owner's key and every conversation go there.
		return nil, fmt.Errorf("upstream %s: an address on another machine has to be https", name)
	}
	if keyAt == "" {
		keyAt = "the secret-store entry " + name + "-key"
	}
	engine.refusalNote = fmt.Sprintf("the upstream %s refused the proxy's key for it; check %s", name, keyAt)
	return func(c *handlerConfig) {
		if c.upstreams == nil {
			c.upstreams = map[string]*namedUpstream{}
		}
		own := *engine // a handler sets its own proxy key on its engines
		c.upstreams[name] = &namedUpstream{name: name, engine: &own, key: key, keyAt: keyAt}
	}, nil
}

// upstreamNames are the configured names, sorted.
func (c *handlerConfig) upstreamNames() []string {
	names := make([]string, 0, len(c.upstreams))
	for name := range c.upstreams {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// namedModel splits a model name of the form <name>:<model> for one of the configured names. A name with no
// prefix and a codex: name are not named models: name is empty. A prefix nobody knows is an error that says
// which there are, whether or not any upstream is configured.
func namedModel(model string, names []string) (name, rest string, err error) {
	prefix, rest, found := strings.Cut(model, ":")
	if !found || prefix == engineCodex {
		return "", "", nil
	}
	if !slices.Contains(names, prefix) {
		// Never another engine's to answer: a name that was not added, or is switched off, is said to be so.
		others := "no other engine is on"
		if len(names) > 0 {
			others = "or as <name>:<model> for one of: " + strings.Join(names, ", ")
		}
		return "", "", fmt.Errorf("no engine called %q is on here; a model can be named plainly, as codex:<model>, %s", cutID(prefix), others)
	}
	if rest == "" {
		return "", "", fmt.Errorf("the model name gives the upstream %s and no model after it", prefix)
	}
	return prefix, rest, nil
}

// cutID is text from a request as it may be repeated in an error: a plain name, or nothing.
func cutID(text string) string {
	if ledgerID(text) == "" {
		return "?"
	}
	return text
}

// renameModel returns body with its top-level "model" set to name and every other byte as it was. It reports
// false, changing nothing, when body is not an object with a string there.
func renameModel(body []byte, name string) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if open, err := dec.Token(); err != nil || open != json.Delim('{') {
		return body, false
	}
	quoted, _ := json.Marshal(name)
	var out []byte
	copied := 0
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return body, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return body, false
		}
		if key, _ := keyToken.(string); key != "model" {
			continue
		}
		var old string
		if json.Unmarshal(value, &old) != nil {
			return body, false
		}
		end := int(dec.InputOffset())
		out = append(append(out, body[copied:end-len(value)]...), quoted...)
		copied = end
	}
	if out == nil {
		return body, false
	}
	return append(out, body[copied:]...), true
}

// keyNow returns the upstream's key, read at most once a minute.
func (u *namedUpstream) keyNow(ctx context.Context) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cached != "" && time.Now().Before(u.until) {
		return u.cached, nil
	}
	key, err := u.key(ctx)
	if err != nil || key == "" {
		return "", errors.New("no key")
	}
	u.cached, u.until = key, time.Now().Add(upstreamKeyTTL)
	return key, nil
}

func (u *namedUpstream) forgetKey() {
	u.mu.Lock()
	u.cached = ""
	u.mu.Unlock()
}

// upstreamHeaders are the only headers of a request that go to a named upstream: what says how to read the
// request and the answer. The caller's login, cookies and ids stay behind; the upstream is another party.
var upstreamHeaders = []string{"Accept", "Accept-Encoding", "Anthropic-Beta", "Anthropic-Version", "Content-Type", "User-Agent"}

// serveNamedUpstream answers a model call that names a configured upstream, and reports whether it did. When
// the request names none, it is left as it came, body included, for the engines to answer.
func serveNamedUpstream(w http.ResponseWriter, r *http.Request, carried credentials, path string, config *handlerConfig) bool {
	message := r.Method == http.MethodPost && r.URL.Path == "/v1/messages"
	counting := r.Method == http.MethodPost && r.URL.Path == "/v1/messages/count_tokens"
	if !message && !counting {
		return false
	}
	answerable := carried.proxyKey || (carried.own && config.anthropic != nil)
	if carried.badPath || !answerable {
		// Nothing here can answer this caller: it is refused where every such request is refused, unread. (A
		// login alone is only looked at when the Anthropic engine is on, where it may name a plain model.)
		return false
	}
	body, ok := readBody(w, r)
	if !ok {
		return true
	}
	// Only the model's name is read here. Every model call passes this point, and most name no upstream.
	var named struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &named)
	name, model, err := namedModel(named.Model, config.upstreamNames())
	switch {
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return true
	case name == "":
		r.Body = io.NopCloser(bytes.NewReader(body))
		return false
	case !carried.proxyKey:
		writeError(w, http.StatusUnauthorized, "authentication_error", errUpstreamNeedsProxyKey.Error())
		return true
	}
	var request messageRequest
	_ = json.Unmarshal(body, &request)
	upstream := config.upstreams[name]
	renamed, ok := renameModel(body, model)
	if !ok || upstream.engine.holdsProxyKey(path) || upstream.engine.holdsProxyKey(r.URL.RawQuery) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "the request could not be sent to "+name)
		return true
	}
	key, err := upstream.keyNow(r.Context())
	if err != nil {
		// What went wrong reading it may name files and accounts; the client is told which entry is wanted.
		writeError(w, http.StatusBadGateway, "api_error", fmt.Sprintf("the proxy has no key for %s: %s is missing or cannot be read", name, upstream.keyAt))
		return true
	}
	out := r.Clone(r.Context())
	out.Header = http.Header{}
	for _, header := range upstreamHeaders {
		if values := r.Header.Values(header); len(values) > 0 {
			out.Header[header] = values
		}
	}
	out.Header.Set("X-Api-Key", key)
	var result forwardResult
	if message {
		// The ledger reads the session from the request as it came; the upstream gets the one built for it.
		result = serveForwardedMessage(w, r, out, path, renamed, request, name, upstream.engine, config)
	} else {
		result = upstream.engine.forward(w, out, path, renamed)
	}
	if result.KeyRefused {
		upstream.forgetKey() // it may have been rotated
	}
	return true
}
