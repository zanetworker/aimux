// Package httpsource turns an http-openapi source's OpenAPI file into the
// read-only operations aimux serves as tools. This file holds only the
// types the runner policy compiles from; the parser comes later.
package httpsource

// Operation is one GET operation an http-openapi source serves as a tool.
type Operation struct {
	ID     string  // the tool name
	Path   string  // the path template, "/repos/{owner}/{repo}/issues"
	Params []Param // path and query parameters
}

// Param is one operation parameter.
type Param struct {
	Name     string
	In       string // path | query
	Type     string // string | integer | number | boolean
	Enum     []string
	Required bool
}
