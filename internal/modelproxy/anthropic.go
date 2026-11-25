package modelproxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
)

// AnthropicUpstream is where the Anthropic engine sends requests.
const AnthropicUpstream = "https://api.anthropic.com"

// Outcomes of a forwarded request.
const (
	forwardOK          = "ok"
	forwardUpstreamErr = "upstream_error"       // the upstream answered, with something other than success
	forwardUnreachable = "upstream_unreachable" // no answer came back
	forwardClientGone  = "client_gone"
	forwardCutShort    = "stream_cut_short" // the answer started and did not finish
	forwardRefused     = "refused"          // not sent: the address held the proxy key
)

// anthropicEngine forwards a request to its upstream unchanged and reports how it went. The caller's credential
// goes to the upstream and nowhere else: it is not kept, logged or returned, and redirects are not followed.
type anthropicEngine struct {
	upstream *url.URL
	client   *http.Client // no overall timeout: a turn can stream for minutes; the client's context ends it
	proxyKey string       // removed from a request wherever the caller put it; the upstream never sees it
}

// forwardResult is what the engine saw. It holds no credential and no text of the request or the answer.
type forwardResult struct {
	Status     int    // what the client was sent; 0 when it left before any answer
	Model      string // the model the response names; empty when it named none
	Usage      Usage
	UsageKnown bool // false when the response carried no usage, or none that could be read
	Outcome    string
}

// newAnthropicEngine checks upstream is a bare origin (scheme and host, nothing else), so a path, a query or a
// user name in a setting can never change where requests and credentials go.
func newAnthropicEngine(upstream, proxyKey string) (*anthropicEngine, error) {
	origin, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("anthropic upstream: %w", err)
	}
	bare := origin.User == nil && origin.RawQuery == "" && origin.Fragment == "" && (origin.Path == "" || origin.Path == "/")
	if (origin.Scheme != "https" && origin.Scheme != "http") || origin.Host == "" || !bare {
		return nil, errors.New("anthropic upstream must be an http or https address with a host and nothing after it")
	}
	return &anthropicEngine{
		upstream: &url.URL{Scheme: origin.Scheme, Host: origin.Host},
		proxyKey: proxyKey,
		client: &http.Client{
			// A redirect would carry the caller's credential to another address. The client gets it instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// forward sends r to the upstream at requestPath (the path as it arrived, escapes included, less the proxy's own
// prefix), with the proxy's own key removed, copies the response to w as it
// arrives, and returns what it saw. The body bytes are sent as given and the answer's bytes reach the client as
// received. One header is narrowed: the upstream is asked for gzip or for no compression, never an encoding the
// usage reader cannot follow.
func (e *anthropicEngine) forward(w http.ResponseWriter, r *http.Request, requestPath string, body []byte) forwardResult {
	if e.holdsProxyKey(requestPath) || e.holdsProxyKey(r.URL.RawQuery) {
		// Whatever put it there, the key does not leave in an address.
		writeError(w, http.StatusBadRequest, "invalid_request_error", keyInAddress)
		return forwardResult{Status: http.StatusBadRequest, Outcome: forwardRefused}
	}
	target := *e.upstream
	target.Path, target.RawPath, target.RawQuery = upstreamPath(requestPath), "", r.URL.RawQuery
	if plain, err := url.PathUnescape(target.Path); err == nil {
		// Sent with its escapes as they came: an escaped slash is not a slash.
		target.Path, target.RawPath = plain, target.Path
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "api_error", "the request could not be forwarded")
		return forwardResult{Status: http.StatusBadGateway, Outcome: forwardUnreachable}
	}
	out.Header = e.outgoingHeader(r.Header)

	// The address is the engine's fixed upstream; the request supplies only the path and query.
	resp, err := e.client.Do(out) //nolint:gosec // G704: scheme and host come from newAnthropicEngine, never the request
	if err != nil {
		if r.Context().Err() != nil {
			return forwardResult{Outcome: forwardClientGone}
		}
		// The error text can name addresses; the client gets a fixed sentence.
		writeError(w, http.StatusBadGateway, "api_error", "the model provider could not be reached")
		return forwardResult{Status: http.StatusBadGateway, Outcome: forwardUnreachable}
	}
	defer func() { _ = resp.Body.Close() }()

	result := forwardResult{Status: resp.StatusCode, Outcome: forwardOK}
	succeeded := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !succeeded {
		result.Outcome = forwardUpstreamErr
	}
	// Only a success with a body is read for usage. An error body is copied unread, and the answer to a HEAD
	// describes a body it does not send.
	bodyless := r.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent
	usage := newUsageReader("", "")
	if succeeded && !bodyless {
		usage = newUsageReader(resp.Header.Get("Content-Type"), resp.Header.Get("Content-Encoding"))
	}
	header := w.Header()
	for name, values := range resp.Header {
		if !hopByHop(name, resp.Header) {
			header[name] = values
		}
	}
	if usage.judges() {
		// With a promised length the client would take an answer found unfinished for a whole one: it gets
		// every promised byte before that is known. Without it, the connection can be broken.
		header.Del("Content-Length")
	}
	w.WriteHeader(resp.StatusCode)

	// Through the controller, so a writer that wraps another (to time the answer, say) still flushes.
	flusher := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = usage.Write(buf[:n])
			if _, err := w.Write(buf[:n]); err != nil {
				result.Outcome = forwardClientGone
				break
			}
			_ = flusher.Flush()
		}
		if readErr == nil {
			continue
		}
		switch {
		case errors.Is(readErr, io.EOF):
		case r.Context().Err() != nil:
			result.Outcome = forwardClientGone
		default:
			result.Outcome = forwardCutShort
		}
		break
	}
	result.Model, result.Usage, result.UsageKnown = usage.result()
	if result.Outcome == forwardOK && !usage.complete() {
		result.Outcome = forwardCutShort
	}
	if result.Outcome == forwardCutShort {
		dropConnection(w)
	}
	return result
}

// outgoingHeader is the caller's header as the upstream should get it: everything, less the headers that belong
// to one connection, less every value that holds the proxy's key (as a credential, inside an address in a
// Referer, anywhere), with the accepted encoding narrowed.
func (e *anthropicEngine) outgoingHeader(from http.Header) http.Header {
	to := make(http.Header, len(from))
	for name, values := range from {
		if !hopByHop(name, from) {
			to[name] = values
		}
	}
	to.Del("X-Aimux-Key")
	if e.proxyKey != "" {
		for name := range to {
			keepValues(to, name, func(value string) bool { return !e.holdsProxyKey(value) })
		}
	}
	encoding := "identity"
	for _, offered := range strings.Split(strings.Join(from.Values("Accept-Encoding"), ","), ",") {
		name, params, _ := strings.Cut(offered, ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") && !refused(params) {
			encoding = "gzip"
		}
	}
	// Always set: left empty, the transport would add its own and decompress, and the answer would change.
	to.Set("Accept-Encoding", encoding)
	return to
}

// keepValues leaves only the values of a header that keep accepts, and removes the header when none are left.
func keepValues(header http.Header, name string, keep func(string) bool) {
	var kept []string
	for _, value := range header.Values(name) {
		if keep(value) {
			kept = append(kept, value)
		}
	}
	if len(kept) == 0 {
		header.Del(name)
		return
	}
	header[name] = kept
}

// refused reports whether an Accept-Encoding entry's parameters give it a weight of zero, which means "not this".
func refused(params string) bool {
	for _, param := range strings.Split(params, ";") {
		name, value, _ := strings.Cut(param, "=")
		if strings.EqualFold(strings.TrimSpace(name), "q") {
			weight, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			return err == nil && weight == 0
		}
	}
	return false
}

// upstreamPath is requestPath as the upstream is asked for it: unchanged but for a leading slash when it had none.
func upstreamPath(requestPath string) string {
	if strings.HasPrefix(requestPath, "/") {
		return requestPath
	}
	return "/" + requestPath
}

const keyInAddress = "the request's address holds the proxy key, which is not sent on"

// holdsProxyKey reports whether part of an address, or a header value, holds the proxy key, as written or behind
// percent escapes.
func (e *anthropicEngine) holdsProxyKey(part string) bool {
	if e.proxyKey == "" {
		return false
	}
	if strings.Contains(part, e.proxyKey) {
		return true
	}
	plain, err := url.QueryUnescape(part)
	return err == nil && strings.Contains(plain, e.proxyKey)
}

var hopByHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// hopByHop reports whether a header belongs to one connection and must not be passed on: the standard ones, and
// any the Connection header names.
func hopByHop(name string, all http.Header) bool {
	name = textproto.CanonicalMIMEHeaderKey(name)
	if hopByHopHeaders[name] {
		return true
	}
	for _, listed := range all.Values("Connection") {
		for _, token := range strings.Split(listed, ",") {
			if textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(token)) == name {
				return true
			}
		}
	}
	return false
}

// dropConnection closes the client's connection without ending the response properly, so a client that was sent
// part of an answer sees it break and does not take the part for the whole.
func dropConnection(w http.ResponseWriter) {
	if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
		_ = conn.Close()
	}
}
