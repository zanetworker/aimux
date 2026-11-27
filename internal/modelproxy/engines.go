package modelproxy

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/ledger"
)

// The engines a request can be answered by.
const (
	engineCodex     = "codex"
	engineAnthropic = "anthropic"
)

// keyPathPrefix starts a path that carries the proxy key: /k/<key>/v1/messages. It is how a session that sends its
// own login in Authorization presents the key as well: a base URL is the one setting every request of Claude
// Code follows (docs/superpowers/specs/2026-11-25-second-engine-evidence.md).
const keyPathPrefix = "/k/"

var (
	errNeedsProxyKey = errors.New(`this model runs on the owner's Codex login and needs the proxy key; start the session from a shell where you ran: eval "$(aimux model proxy env)" (with --port for a proxy on another port than the stable one)`)
	errNoCredential  = errors.New("invalid or missing proxy key")
)

// SessionEnv is the shell lines that point Claude Code sessions started from that shell at the proxy on port, for
// both engines: the base URL carries the proxy key as its path prefix, and the two variables that would make
// Claude Code use something other than the owner's Claude login are unset. Nothing is said about the model.
// The first line holds the key: it is for eval, never for a screen or a log.
func SessionEnv(port int, key string) []string {
	return []string{
		fmt.Sprintf("export ANTHROPIC_BASE_URL='http://127.0.0.1:%d%s%s'", port, keyPathPrefix, url.PathEscape(key)),
		"unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN",
		"export " + SessionEnvMarker + "='" + SessionEnvEngines + "'",
	}
}

// SessionEnvMarker is a variable the lines set to say which form set the shell. Undoing reads it, so what has to
// be unset is known without reading the proxy key again: a secret store that is locked at that moment must not
// leave the key behind in a shell that no longer points at the proxy.
const (
	SessionEnvMarker  = "AIMUX_MODEL_PROXY_ENV"
	SessionEnvEngines = "engines" // SessionEnv: the key is in the base URL
	SessionEnvCodexed = "codex"   // SessionEnvCodex: the key is the API key, and the model is pinned
)

// codexModelName is a Codex model as it may be written into a shell line.
var codexModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SessionEnvCodex is the shell lines that point Claude Code sessions started from that shell at the proxy on port
// for Codex alone, as the proxy has been used from the start: the proxy key is the API key, so no Claude login
// is involved, and the model is pinned to codex:<model>. It needs no second engine on the proxy.
func SessionEnvCodex(port int, key, model string) ([]string, error) {
	if !codexModelName.MatchString(model) {
		return nil, errors.New("a Codex model is named with letters, digits, dots and hyphens, for example gpt-6.2-sol")
	}
	return []string{
		fmt.Sprintf("export ANTHROPIC_BASE_URL='http://127.0.0.1:%d'", port),
		"export ANTHROPIC_API_KEY=" + shellQuote(key),
		"export ANTHROPIC_MODEL='codex:" + model + "'",
		"unset ANTHROPIC_AUTH_TOKEN",
		"export " + SessionEnvMarker + "='" + SessionEnvCodexed + "'",
	}, nil
}

// ValidCodexModel reports whether model is a Codex model's name as SessionEnvCodex accepts it.
func ValidCodexModel(model string) bool { return codexModelName.MatchString(model) }

// shellQuote is text inside single quotes for a POSIX shell.
func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// SessionEnvOff undoes SessionEnv and SessionEnvCodex. marker is what SessionEnvMarker holds in the shell. The
// base URL and the marker always go. The API key variable and the model go when the marker says the Codex form
// set them. apiKeyIsProxyKey covers a shell set before the marker existed: its API key variable goes when it is
// known to hold the proxy key. A key or a model of the owner's own stays.
// pickerIsOurs: the shell's picker entry is the one SessionEnvPicker set, so it goes too. rolesAreOurs: the same
// for the subagent default SessionEnvRoles set; its marker goes whenever there is one.
func SessionEnvOff(marker string, apiKeyIsProxyKey, pickerIsOurs bool, roles ...bool) []string {
	names := "unset ANTHROPIC_BASE_URL " + SessionEnvMarker
	if marker == SessionEnvCodexed || apiKeyIsProxyKey {
		names += " ANTHROPIC_API_KEY"
	}
	if marker == SessionEnvCodexed {
		names += " ANTHROPIC_MODEL"
	}
	if pickerIsOurs {
		names += " " + PickerVar + " " + PickerVar + "_NAME " + PickerVar + "_DESCRIPTION " + PickerMarker
	}
	// roles[0]: the subagent default is aimux's own. roles[1]: a marker of it is in the shell.
	if len(roles) > 0 && roles[0] {
		names += " " + SubagentModelVar
	}
	if len(roles) > 1 && roles[1] {
		names += " " + RolesMarker
	}
	// roles[2]: the context window is aimux's own. roles[3]: a marker of it is in the shell.
	if len(roles) > 2 && roles[2] {
		names += " " + ContextWindowVar
	}
	if len(roles) > 3 && roles[3] {
		names += " " + ContextWindowMarker
	}
	return []string{names}
}

// The subagent role: Claude Code's own variable for the model its subagents use when they name none, and a
// marker holding what aimux set there, by the same rule as the picker entry.
const (
	SubagentModelVar = "CLAUDE_CODE_SUBAGENT_MODEL"
	RolesMarker      = "AIMUX_MODEL_PROXY_ROLES"
)

// roleModelName is a model a role may put into a shell line: an engine's name and a model, or a model alone.
var roleModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:\[\]-]{0,127}$`)

// SessionEnvRoles is the shell lines that make the owner's own subagents, those that name no model, use model in
// sessions started from that shell. codex:default stands for the proxy's default Codex model at the time. ok is
// false for a model that is not one, which is then put in no line.
func SessionEnvRoles(model string) (lines []string, ok bool) {
	if model == "codex:default" {
		model = "codex:" + DefaultCodexModel
	}
	if !roleModelName.MatchString(model) {
		return nil, false
	}
	return []string{
		"export " + SubagentModelVar + "=" + shellQuote(model),
		"export " + RolesMarker + "=" + shellQuote(model),
	}, true
}

// PickerVar is the variable Claude Code reads, when it starts, for one extra entry in its /model picker.
const PickerVar = "ANTHROPIC_CUSTOM_MODEL_OPTION"

// SessionEnvPicker is the shell lines that put a Codex model in the /model picker of sessions started from that
// shell, so switching engine is a choice from the list and not a name to remember. model is a Codex model's
// name, as ValidCodexModel accepts it.
func SessionEnvPicker(model string) []string {
	return []string{
		"export " + PickerVar + "='codex:" + model + "'",
		"export " + PickerVar + "_NAME='Codex " + model + "'",
		"export " + PickerVar + "_DESCRIPTION='aimux proxy, on your ChatGPT plan'",
		"export " + PickerMarker + "='codex:" + model + "'",
	}
}

// PickerMarker is a variable SessionEnvPicker sets beside the entry, holding the entry it set. An entry is
// aimux's only while the two agree: one the owner set, a Codex one included, is never replaced or removed.
const PickerMarker = "AIMUX_MODEL_PROXY_PICKER"

// PickerIsOurs reports whether value, the shell's picker entry, is the one SessionEnvPicker set, given what
// PickerMarker holds in that shell.
func PickerIsOurs(value, marker string) bool {
	return value != "" && value == marker
}

// credentials is what a request carried, read once. It holds no credential, only which were present.
type credentials struct {
	proxyKey bool // the proxy key: as x-api-key, as a Bearer token, or as the path prefix
	own      bool // an Authorization that is not the proxy key: the caller's own login
	viaPath  bool // the proxy key came as the path prefix
	badPath  bool // the path has the key prefix and the key in it is wrong
}

// readCredentials reports what r carried, and its path as the rest of the proxy should see it: escapes kept, the
// key prefix removed when the key in it was right.
func readCredentials(r *http.Request, key string) (credentials, string) {
	var carried credentials
	path := r.URL.EscapedPath()
	if rest, ok := strings.CutPrefix(path, keyPathPrefix); ok {
		segment, after, _ := strings.Cut(rest, "/")
		if got, err := url.PathUnescape(segment); err == nil && sameKey(got, key) {
			carried.proxyKey, carried.viaPath = true, true
			path = "/" + after
		} else {
			carried.badPath = true
		}
	} else if strings.HasPrefix(r.URL.Path, keyPathPrefix) {
		// The prefix spelled with escapes. Only the plain spelling is the prefix; another one is refused, so a
		// key behind it is never taken for part of a path and passed on.
		carried.badPath = true
	}
	if sameKey(r.Header.Get("x-api-key"), key) {
		carried.proxyKey = true
	}
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && bearer != "" {
		if sameKey(bearer, key) {
			carried.proxyKey = true
		} else {
			carried.own = true
		}
	}
	return carried, path
}

// sameKey compares in constant time. An empty key matches nothing, so the proxy never runs open.
func sameKey(got, key string) bool {
	return key != "" && subtle.ConstantTimeCompare([]byte(got), []byte(key)) == 1
}

// withoutKeyPrefix is r with path (escaped, the key prefix already removed) as its path.
func withoutKeyPrefix(r *http.Request, path string) *http.Request {
	stripped := r.Clone(r.Context())
	stripped.URL.Path, stripped.URL.RawPath = path, ""
	if plain, err := url.PathUnescape(path); err == nil {
		stripped.URL.Path, stripped.URL.RawPath = plain, path
	}
	stripped.RequestURI = stripped.URL.RequestURI()
	return stripped
}

// chooseEngine decides who answers. It reads the model name and the credentials, never the prompt.
//
//	codex:* model, proxy key          -> codex
//	codex:* model, no proxy key       -> refused: errNeedsProxyKey
//	other model, own login            -> anthropic
//	other model, proxy key, no login  -> codex (as before there was a second engine: the default Codex model)
//	nothing usable                    -> refused: errNoCredential
func chooseEngine(model string, carried credentials) (string, error) {
	switch {
	case !carried.proxyKey && !carried.own:
		return "", errNoCredential
	case strings.HasPrefix(model, "codex:"):
		if !carried.proxyKey {
			return "", errNeedsProxyKey
		}
		return engineCodex, nil
	case carried.own:
		return engineAnthropic, nil
	default:
		return engineCodex, nil
	}
}

// WithAnthropic turns the Anthropic engine on, sending to upstream (AnthropicUpstream outside tests). Without it
// every request is answered as before there was a second engine.
func WithAnthropic(upstream string) (HandlerOption, error) {
	engine, err := newAnthropicEngine(upstream, "")
	if err != nil {
		return nil, err
	}
	return func(c *handlerConfig) {
		// A copy each time: a handler sets its own proxy key on its engine, and one option may serve two.
		own := *engine
		c.anthropic = &own
	}, nil
}

// serveEngines answers a request when both engines are on. A request with the proxy key and no login of its own
// goes to codexRoutes exactly as it would without the second engine.
func serveEngines(w http.ResponseWriter, r *http.Request, carried credentials, path string, codexRoutes http.Handler, config *handlerConfig) {
	if carried.badPath || (!carried.proxyKey && !carried.own) {
		writeError(w, http.StatusUnauthorized, "authentication_error", errNoCredential.Error())
		return
	}
	if !carried.own || r.URL.Path == "/health" {
		// The proxy key and no login, or the proxy's own health: exactly what it was before there was a second
		// engine, untouched by anything below. (Health with a login and no key is refused by the key check there.)
		if !carried.proxyKey {
			writeError(w, http.StatusUnauthorized, "authentication_error", errNoCredential.Error())
			return
		}
		// One thing is new for a session that presents the key in its base URL. Claude Code opens every
		// session with HEAD /api/hello, which carries no login; sent on, it opens the proxy's connection to
		// Anthropic before the first prompt, as it would have opened the client's own.
		if carried.viaPath && r.Method == http.MethodHead && r.URL.Path == "/api/hello" && config.anthropic.hello(w, r) {
			return
		}
		codexRoutes.ServeHTTP(w, r)
		return
	}
	if config.anthropic.holdsProxyKey(path) || config.anthropic.holdsProxyKey(r.URL.RawQuery) {
		// Refused before anything is recorded: this request reaches no engine.
		writeError(w, http.StatusBadRequest, "invalid_request_error", keyInAddress)
		return
	}
	// Routes are told by the path as it reads, however it was spelled; the upstream is sent the spelling.
	message := r.Method == http.MethodPost && r.URL.Path == "/v1/messages"
	counting := r.Method == http.MethodPost && r.URL.Path == "/v1/messages/count_tokens"
	// The whole body or none of it: a body over the bound is refused here, never sent on in part.
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if !message && !counting {
		// No model to choose by; with a login it is Anthropic's to answer.
		config.anthropic.forward(w, r, path, body)
		return
	}
	var named struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &named) // a body that is not JSON names no model; the engine it reaches says so
	engine, err := chooseEngine(named.Model, carried)
	switch {
	case err != nil:
		writeError(w, http.StatusUnauthorized, "authentication_error", err.Error())
	case engine == engineCodex:
		r.Body = io.NopCloser(bytes.NewReader(body))
		codexRoutes.ServeHTTP(w, r)
	case message:
		serveAnthropicMessage(w, r, path, body, config, carried.proxyKey)
	default:
		config.anthropic.forward(w, r, path, body)
	}
}

// serveAnthropicMessage forwards one model call to Anthropic and records it.
func serveAnthropicMessage(w http.ResponseWriter, r *http.Request, path string, body []byte, config *handlerConfig, withProxyKey bool) {
	// Read for the session, the turn and the model's name only. A body this does not fit is still forwarded.
	var request messageRequest
	_ = json.Unmarshal(body, &request)
	serveForwardedMessage(w, r, r, path, body, request, engineAnthropic, config.anthropic, config, withProxyKey)
}

// serveForwardedMessage forwards one model call to engine and records it as the Codex path records its own: a
// ledger line and a request-history record, with figures and ids and no text. r is the request as it came, read
// for whose call this is; out is the request the engine sends on, which may be r itself; request is what body
// held before anything was changed for the upstream. withProxyKey: the request carried the proxy key.
func serveForwardedMessage(w http.ResponseWriter, r, out *http.Request, path string, body []byte, request messageRequest, name string, engine *anthropicEngine, config *handlerConfig, withProxyKey bool) forwardResult {
	timing := &messageTiming{ResponseWriter: w, started: time.Now(), firstByte: -time.Millisecond}
	facts := callFactsOfMessages(r, request, request.OutputConfig.Effort)
	var call *ledger.Line
	if config.ledger != nil {
		var change *ledger.Line
		call, change = startModelCall(config.turns, facts, name, "")
		if change != nil {
			config.ledger.Write(*change)
		}
	}
	result := engine.forward(timing, out, path, body)
	failure := ""
	if result.Outcome != forwardOK {
		failure = result.Outcome
	}
	session := NoSession
	if id := metadataSessionID(request.Metadata); id != "" {
		session = SessionTag(id)
	}
	config.requests.Record(Record{
		At: timing.started, Outcome: "forwarded", Detail: name, Session: session, Model: ledgerModel(request.Model),
		Effort: ledgerID(request.OutputConfig.Effort), Status: timing.status, TTFB: timing.firstByte,
		Duration: time.Since(timing.started), Usage: result.Usage, Error: failure,
	})
	if result.AllowanceKnown {
		config.allowances.note(name, result.Allowance, time.Now())
	}
	if call != nil {
		finishModelCall(call, timing.started, timing.firstByte, result.Usage, failure, timing.status)
		call.ResponseModel, call.UsageUnknown, call.Limit = ledgerModel(result.Model), !result.UsageKnown, result.Limit
		// A report only: the answer has gone to the session as the upstream gave it.
		if config.onLimit == OnLimitReport && result.Limit == limitPlan {
			call.OnLimit = onLimitReport(facts, withProxyKey)
		}
		config.ledger.Write(*call)
	}
	return result
}
