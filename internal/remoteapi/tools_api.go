package remoteapi

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// ToolsView is what GET /v1/tools explains: the tool snapshot remote serve
// compiles turns against, and the tool gateway's own live listings of its
// sources (toolgateway.LiveFor); a source missing from Live has not been
// listed by the gateway, so its tools are unknown.
type ToolsView struct {
	Snapshot toolreg.Snapshot
	Live     map[string]toolgateway.LiveSource
	// RelistFailed is the sources the gateway still serves whose last
	// relist failed (toolgateway.RelistFailedFor): unknown, not down.
	RelistFailed map[string]bool
}

// Set statuses of GET /v1/tools; tool statuses are toolgateway's (active,
// unpinned, changed, unavailable, source_changed) plus toolUnknown.
const (
	setAvailable   = "available"   // every tool is granted
	setPartial     = "partial"     // some are
	setUnavailable = "unavailable" // none is, or the set is omitted
	toolUnknown    = "unknown"     // the gateway has not listed its source
)

// ToolsList is GET /v1/tools.
type ToolsList struct {
	Snapshot string        `json:"snapshot"`
	Sets     []ToolSetInfo `json:"sets"`
	Errors   []ToolsError  `json:"errors"`
}

// ToolSetInfo is one set a profile in the caller's scope requests, under
// the name the profile uses. Source is "" for a set that is omitted; URL is
// the source's URL only when it is a loopback one (no userinfo or query).
type ToolSetInfo struct {
	Name   string     `json:"name"`
	Source string     `json:"source"`
	URL    string     `json:"url,omitempty"`
	Status string     `json:"status"`
	Reason string     `json:"reason,omitempty"`
	Tools  []ToolInfo `json:"tools"`
}

// ToolInfo is one tool of a set.
type ToolInfo struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	ApprovedAt *time.Time `json:"approvedAt,omitempty"`
}

// ToolsError is a registry problem with a set or source in the caller's
// scope, or (neither set) a load failure.
type ToolsError struct {
	Set     string `json:"set,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
}

const (
	reasonLoadFailed = "the tool configuration did not load (see errors)"
	msgLoadFailed    = "the tool registry or pins could not be loaded; no tool is granted or served until it is fixed (aimux tools status on the Mac shows why)"
	reasonNotListed  = "not listed by the tool gateway yet (it lists its sources at the first sandbox turn)"
	// reasonRelistFailed is a source still served whose last relist failed.
	reasonRelistFailed = "unknown (relist failed): still served under its pin check; the gateway lists it again within a minute"
)

var setNamePattern = regexp.MustCompile(`^[^:]+:[^:]+$`)

// listTools explains, for each set the profiles in the caller's scope
// request, what remote serve grants and serves now: read-only, from the
// current snapshot and what the gateway already listed (no backend is
// contacted). Reasons and messages carry no definition, credential or
// non-loopback URL.
func (a *API) listTools(w http.ResponseWriter, r *http.Request) {
	all, ok := a.loadProfiles(w)
	if !ok {
		return
	}
	if a.Tools == nil {
		writeError(w, http.StatusNotFound, "not_supported", errors.New("the tool sets are not available"))
		return
	}
	client := a.clientOf(r)
	requested := map[string]bool{}
	for _, m := range all {
		if a.profileAllowed(client, m) {
			for _, s := range m.Tools {
				requested[s] = true
			}
		}
	}
	v := a.Tools()
	snap := v.Snapshot
	out := ToolsList{Snapshot: snap.Version(), Sets: []ToolSetInfo{}, Errors: []ToolsError{}}
	reg := snap.Registry()
	loadErr := snap.Err()
	if loadErr != nil {
		a.logf("tools: %v", loadErr)
		out.Errors = append(out.Errors, ToolsError{Message: msgLoadFailed})
	}
	byTool := map[string]toolgateway.ToolStatus{}
	for _, st := range toolgateway.Review(reg, toolgateway.PinsOf(snap), v.Live) {
		byTool[st.Source+"/"+st.Tool] = st
	}
	sourceErrs := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(requested)) {
		info := ToolSetInfo{Name: name, Status: setUnavailable, Tools: []ToolInfo{}}
		set, err := reg.Resolve(name)
		switch {
		case loadErr != nil && err != nil:
			info.Reason = reasonLoadFailed
		case !setNamePattern.MatchString(name):
			info.Reason = "not a <name>:<kind> set name"
		case reg.SetErrors[name] != nil:
			info.Reason = "tool-sources.yaml rejects this set (see errors)"
			out.Errors = append(out.Errors, ToolsError{Set: name, Message: redactText(reg.SetErrors[name].Error())})
			for _, src := range slices.Sorted(maps.Keys(reg.SourceErrors)) {
				if errors.Is(reg.SetErrors[name], reg.SourceErrors[src]) && !sourceErrs[src] {
					sourceErrs[src] = true
					out.Errors = append(out.Errors, ToolsError{Source: src, Message: redactText(reg.SourceErrors[src].Error())})
				}
			}
		case err != nil:
			info.Reason = "not in tool-sources.yaml"
		default:
			info.Source, info.URL = set.Source, loopbackURL(reg.Sources[set.Source].URL)
			info.Tools, info.Status, info.Reason = setTools(snap, set, reg.Sources[set.Source], byTool, v.Live, v.RelistFailed[set.Source], loadErr != nil)
		}
		out.Sets = append(out.Sets, info)
	}
	writeJSON(w, http.StatusOK, out)
}

// setTools is the tools of set, sorted, and the set's status and reason. A
// tool is granted when it is active, or pinned (and not source_changed)
// with its source not yet listed by the gateway. With failed, every tool is
// unavailable: nothing is granted.
func setTools(snap toolreg.Snapshot, set toolreg.Set, src toolreg.Source, byTool map[string]toolgateway.ToolStatus, live map[string]toolgateway.LiveSource, relistFailed, failed bool) ([]ToolInfo, string, string) {
	tools := []ToolInfo{}
	granted, unknown := 0, 0
	l, listed := live[set.Source]
	for _, name := range slices.Sorted(slices.Values(set.Tools)) {
		st := byTool[set.Source+"/"+name]
		ti := ToolInfo{Name: name, Status: st.Status, Reason: redactText(st.Reason), ApprovedAt: st.ApprovedAt}
		_, pinned := snap.Pin(set.Source, name)
		switch {
		case failed:
			ti.Status, ti.Reason = toolgateway.StatusUnavailable, reasonLoadFailed
		case ti.Status == toolgateway.StatusActive:
			granted++
		case ti.Status == toolgateway.StatusSourceChanged:
			// Review names both endpoints for the owner; a client gets the source only.
			ti.Reason = "approved for a different address of source " + set.Source
		case ti.Status == toolgateway.StatusUnavailable && listed && l.Err != nil:
			ti.Reason = downReason(src) // the error itself stays in remote serve's log
		case ti.Status == toolgateway.StatusUnavailable && !listed:
			ti.Status, ti.Reason = toolUnknown, reasonNotListed
			if relistFailed {
				ti.Reason = reasonRelistFailed
			}
			if pinned {
				granted++
				unknown++
			}
		}
		tools = append(tools, ti)
	}
	n := len(tools)
	switch {
	case failed:
		return tools, setUnavailable, reasonLoadFailed
	case n == 0:
		return tools, setUnavailable, "lists no tools"
	case granted == n && unknown > 0:
		return tools, setAvailable, "the tool gateway has not listed it live yet"
	case granted == n:
		return tools, setAvailable, ""
	case granted > 0:
		return tools, setPartial, fmt.Sprintf("%d of %d tools active", granted, n)
	}
	return tools, setUnavailable, "no tool is active (see each tool)"
}

// downReason is what a client is told about a source the gateway could not
// list: never the error, which can name host paths or endpoints.
func downReason(src toolreg.Source) string {
	if src.Type == toolreg.TypeLocalNotes {
		return "notes folder not found"
	}
	return "backend unavailable"
}

var (
	quotedPath   = regexp.MustCompile(`"/[^"]*"`)
	barePath     = regexp.MustCompile(`(^|[\s(=])/[^\s"':,;)]+`)
	urlInText    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
	registryPath = regexp.MustCompile(`tool sources \S+: `)
)

// redactText is s fit for a client: the registry's path shortened to its
// name, each URL kept only when loopback (without userinfo, query or
// fragment) and otherwise "<url>", any other absolute path "<path>", and
// token-shaped strings hidden.
func redactText(s string) string {
	s = registryPath.ReplaceAllString(s, "tool-sources.yaml: ")
	s = urlInText.ReplaceAllStringFunc(s, func(raw string) string {
		if u := loopbackURL(raw); u != "" {
			return u
		}
		return "<url>"
	})
	s = quotedPath.ReplaceAllString(s, `"<path>"`)
	s = barePath.ReplaceAllString(s, "${1}<path>")
	return mcpproxy.ScrubTokens(s)
}

// loopbackURL is raw as scheme://host[:port]/path when its host is
// loopback (localhost, 127.0.0.0/8, ::1), else "".
func loopbackURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	h := u.Hostname()
	if ip := net.ParseIP(h); h != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}
