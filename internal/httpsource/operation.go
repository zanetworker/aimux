// Package httpsource turns an http-openapi source's OpenAPI file into the
// read-only operations aimux serves as tools: Operations reads the spec,
// Tools describes them for the gateway and BuildURL makes one request URL
// from an agent's arguments.
package httpsource

import "errors"

// CurlBinary is the one binary an http-openapi request runs as in the
// runner: the runner policy allows it and the backend execs it (RunTool
// takes only an absolute path).
const CurlBinary = "/usr/bin/curl"

// ErrCredential is why an http-openapi source that names a credential
// serves nothing: OpenShell 0.1.7 offers no checked way for curl in the
// runner to send a provider's credential without its value in argv.
var ErrCredential = errors.New("http-openapi sources cannot send a credential yet; remove credential to serve its public GET operations")

// Operation is one GET operation an http-openapi source serves as a tool.
type Operation struct {
	ID          string  // the sanitized operationId; the tool is ToolName
	Path        string  // the path template, "/repos/{owner}/{repo}/issues"
	Description string  // the summary (or description); not pinned
	Params      []Param // path and query parameters
}

// Param is one operation parameter.
type Param struct {
	Name     string
	In       string // path | query
	Type     string // string | integer | boolean
	Enum     []string
	Required bool
}

// ToolName is op's tool name for source: <source>_<operationId>.
func ToolName(source string, op Operation) string { return source + "_" + op.ID }
