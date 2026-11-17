package httpsource

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zanetworker/aimux/internal/toolreg"
)

// MaxSpecBytes is the largest OpenAPI file aimux reads.
const MaxSpecBytes = 5 << 20

// The parts of an OpenAPI 3.x document aimux reads; everything else is
// ignored. JSON is YAML, so one decoder reads both.
type (
	specDoc struct {
		OpenAPI    string                  `yaml:"openapi"`
		Swagger    string                  `yaml:"swagger"`
		Paths      map[string]specPathItem `yaml:"paths"`
		Components struct {
			Parameters map[string]specParam `yaml:"parameters"`
		} `yaml:"components"`
	}
	specPathItem struct {
		Ref        string      `yaml:"$ref"`
		Parameters []specParam `yaml:"parameters"`
		Get        *specOp     `yaml:"get"`
		Put        *specOp     `yaml:"put"`
		Post       *specOp     `yaml:"post"`
		Delete     *specOp     `yaml:"delete"`
		Options    *specOp     `yaml:"options"`
		Head       *specOp     `yaml:"head"`
		Patch      *specOp     `yaml:"patch"`
		Trace      *specOp     `yaml:"trace"`
	}
	specOp struct {
		OperationID string      `yaml:"operationId"`
		Summary     string      `yaml:"summary"`
		Description string      `yaml:"description"`
		Parameters  []specParam `yaml:"parameters"`
	}
	specParam struct {
		Ref      string `yaml:"$ref"`
		Name     string `yaml:"name"`
		In       string `yaml:"in"`
		Required bool   `yaml:"required"`
		// Serialization: BuildURL sends only the defaults (simple for path,
		// form with explode for query, reserved characters escaped).
		Style           string `yaml:"style"`
		Explode         *bool  `yaml:"explode"`
		AllowReserved   bool   `yaml:"allowReserved"`
		AllowEmptyValue bool   `yaml:"allowEmptyValue"`
		Content         any    `yaml:"content"`
		Schema          *struct {
			Ref  string `yaml:"$ref"` // not followed: the operation is skipped
			Type any    `yaml:"type"` // a string, or a list in 3.1
			Enum []any  `yaml:"enum"`
		} `yaml:"schema"`
	}
)

// SourceOperations reads src's spec file (at most MaxSpecBytes) and returns
// its operations. Errors name the file, never the base URL or credential.
func SourceOperations(src toolreg.Source) ([]Operation, error) {
	ops, _, _, err := new(SpecCache).Load(src.Spec)
	return ops, err
}

// SpecCache holds one spec file's parse, read again only when the file's
// path, modification time or size changes.
type SpecCache struct {
	mu      sync.Mutex
	key     specKey
	ops     []Operation
	ignored []Ignored
	err     error // a parse error is kept with its key too
}

type specKey struct {
	path string
	mod  time.Time
	size int64
}

// Load is the operations in the spec at path and the operations skipped;
// read reports whether the file was parsed now rather than taken from the
// cache. Errors name the file, never the base URL or credential.
func (c *SpecCache) Load(path string) (ops []Operation, ignored []Ignored, read bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.Open(path) // #nosec G304 -- the registry's spec path, an absolute path the owner configured
	if err != nil {
		return nil, nil, false, fmt.Errorf("spec %s: %w", path, unwrapPath(err))
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, false, fmt.Errorf("spec %s: %w", path, unwrapPath(err))
	}
	key := specKey{path: path, mod: st.ModTime(), size: st.Size()}
	if key == c.key {
		return c.ops, c.ignored, false, c.err
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxSpecBytes+1))
	if err != nil {
		return nil, nil, false, fmt.Errorf("spec %s: %w", path, unwrapPath(err))
	}
	c.ops, c.ignored, c.err = Operations(b)
	if c.err != nil {
		c.ops, c.ignored, c.err = nil, nil, fmt.Errorf("spec %s: %w", path, c.err)
	}
	c.key = key
	return c.ops, c.ignored, true, c.err
}

// unwrapPath drops a PathError's own copy of the path, which the caller
// already names.
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}

// MaxOperations is the most GET operations a spec may hold.
const MaxOperations = 500

// Ignored is an operation Operations does not serve, and why.
type Ignored struct {
	Method, Path, OperationID, Reason string
}

// Operations reads an OpenAPI 3.0 or 3.1 document (YAML or JSON): every
// get with an operationId whose path and parameters aimux can send becomes
// an Operation, sorted by path then ID. Every other operation (other
// methods, a get without operationId, a path CheckPath refuses or whose
// {param}s do not match its path parameters, a required header, cookie or
// non-scalar parameter) is returned in ignored with the reason. Optional
// parameters aimux cannot send are dropped. A spec over MaxSpecBytes or
// with more than MaxOperations gets, not OpenAPI 3, with a $ref it cannot
// resolve or two operations with one tool name is an error.
func Operations(spec []byte) (ops []Operation, ignored []Ignored, err error) {
	if len(spec) > MaxSpecBytes {
		return nil, nil, errors.New("larger than 5 MiB")
	}
	var doc specDoc
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, nil, fmt.Errorf("not YAML or JSON: %w", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		if doc.Swagger != "" {
			return nil, nil, errors.New("swagger 2.0 is not supported: convert it to OpenAPI 3")
		}
		return nil, nil, errors.New("not an OpenAPI 3 document (no openapi: 3.x)")
	}
	gets := 0
	for _, item := range doc.Paths {
		if item.Get != nil {
			gets++
		}
	}
	if gets > MaxOperations {
		return nil, nil, fmt.Errorf("%d GET operations; at most %d are served from one spec", gets, MaxOperations)
	}
	ids := map[string]string{} // tool ID -> the operationId that took it
	for path, item := range doc.Paths {
		if item.Ref != "" {
			ignored = append(ignored, Ignored{Path: path, Reason: "a $ref path item is not followed"})
			continue
		}
		for method, other := range map[string]*specOp{"put": item.Put, "post": item.Post, "delete": item.Delete, "options": item.Options, "head": item.Head, "patch": item.Patch, "trace": item.Trace} {
			if other != nil {
				ignored = append(ignored, Ignored{Method: method, Path: path, OperationID: other.OperationID, Reason: "only GET operations are served"})
			}
		}
		if item.Get == nil {
			continue
		}
		op, reason, err := operation(doc, path, item.Parameters, item.Get)
		if err != nil {
			return nil, nil, err
		}
		if reason != "" {
			ignored = append(ignored, Ignored{Method: "get", Path: path, OperationID: item.Get.OperationID, Reason: reason})
			continue
		}
		if prev, dup := ids[op.ID]; dup {
			return nil, nil, fmt.Errorf("operationIds %q and %q are both tool %s", prev, item.Get.OperationID, op.ID)
		}
		ids[op.ID] = item.Get.OperationID
		ops = append(ops, op)
	}
	slices.SortFunc(ops, func(a, b Operation) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(ignored, func(a, b Ignored) int {
		return strings.Compare(a.Path+" "+a.Method, b.Path+" "+b.Method)
	})
	return ops, ignored, nil
}

// operation is the get at path as an Operation, or the reason aimux
// cannot serve it.
func operation(doc specDoc, path string, shared []specParam, get *specOp) (Operation, string, error) {
	id := sanitizeID(get.OperationID)
	if id == "" {
		return Operation{}, "no operationId", nil
	}
	if err := CheckPath(path); err != nil {
		return Operation{}, err.Error(), nil
	}
	op := Operation{ID: id, Path: path, Description: get.Summary}
	if op.Description == "" {
		op.Description = get.Description
	}
	// Path-level parameters first; an operation-level one replaces the one
	// with its name and location.
	var all []specParam
	for _, list := range [][]specParam{shared, get.Parameters} {
		for _, p := range list {
			p, err := resolve(doc, p)
			if err != nil {
				return Operation{}, "", fmt.Errorf("operation %s: %w", get.OperationID, err)
			}
			if i := slices.IndexFunc(all, func(q specParam) bool { return q.Name == p.Name && q.In == p.In }); i >= 0 {
				all[i] = p
			} else {
				all = append(all, p)
			}
		}
	}
	for _, p := range all {
		if why := serialization(p); why != "" {
			return Operation{}, fmt.Sprintf("parameter %q: unsupported parameter serialization (%s)", p.Name, why), nil
		}
		if p.Schema != nil && p.Schema.Ref != "" {
			return Operation{}, fmt.Sprintf("parameter %q: unsupported schema $ref %q", p.Name, p.Schema.Ref), nil
		}
		param, ok := convert(p)
		switch {
		case ok && slices.ContainsFunc(op.Params, func(q Param) bool { return q.Name == param.Name }):
			return Operation{}, fmt.Sprintf("parameter %q is both a path and a query parameter", p.Name), nil
		case ok:
			op.Params = append(op.Params, param)
		case p.Required || p.In == "path":
			return Operation{}, fmt.Sprintf("required %s parameter %q cannot be sent (only path and query strings, integers, booleans and enums)", p.In, p.Name), nil
		}
	}
	if !paramsMatch(path, op.Params) {
		return Operation{}, "the path's {param}s do not match its path parameters", nil
	}
	return op, "", nil
}

// serialization is why p, a path or query parameter, would not be sent the
// way BuildURL sends it ("" when it would). Header and cookie parameters
// are never sent, so their serialization does not matter.
func serialization(p specParam) string {
	style, explode := "", false
	switch p.In {
	case "path":
		style = "simple"
	case "query":
		style, explode = "form", true
	default:
		return ""
	}
	switch {
	case p.Content != nil:
		return "content instead of schema"
	case p.Style != "" && p.Style != style:
		return "style " + p.Style
	case p.Explode != nil && *p.Explode != explode:
		return fmt.Sprintf("explode: %v", *p.Explode)
	case p.AllowReserved:
		return "allowReserved"
	case p.AllowEmptyValue:
		return "allowEmptyValue"
	}
	return ""
}

// resolve follows a parameter's $ref into components/parameters (one level).
func resolve(doc specDoc, p specParam) (specParam, error) {
	if p.Ref == "" {
		return p, nil
	}
	name, ok := strings.CutPrefix(p.Ref, "#/components/parameters/")
	if !ok {
		return specParam{}, fmt.Errorf("$ref %q: only #/components/parameters/ is supported", p.Ref)
	}
	name = strings.NewReplacer("~1", "/", "~0", "~").Replace(name)
	q, ok := doc.Components.Parameters[name]
	if !ok || q.Ref != "" {
		return specParam{}, fmt.Errorf("$ref %q does not resolve to a parameter", p.Ref)
	}
	return q, nil
}

// convert is p as a Param, if aimux can send it: a path or query parameter
// whose schema is a string, integer or boolean, or an enum of scalars.
func convert(p specParam) (Param, bool) {
	if (p.In != "path" && p.In != "query") || p.Name == "" || p.Schema == nil {
		return Param{}, false
	}
	typ, ok := schemaType(p.Schema.Type)
	if !ok {
		return Param{}, false
	}
	out := Param{Name: p.Name, In: p.In, Type: typ, Required: p.Required || p.In == "path"}
	for _, v := range p.Schema.Enum {
		switch v.(type) {
		case string, int, bool:
			out.Enum = append(out.Enum, fmt.Sprint(v))
		case nil:
			// a nullable enum's null: nothing to send
		default:
			return Param{}, false
		}
	}
	return out, true
}

// schemaType is a schema's one scalar type ("string" when unset); a 3.1
// list may add "null".
func schemaType(t any) (string, bool) {
	var types []string
	switch v := t.(type) {
	case nil:
		return "string", true
	case string:
		types = []string{v}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && s != "null" {
				types = append(types, s)
			}
		}
	}
	if len(types) != 1 || !slices.Contains([]string{"string", "integer", "boolean"}, types[0]) {
		return "", false
	}
	return types[0], true
}

// CheckPath reports why tmpl cannot be an operation's path template: it
// must be absolute, and each segment either one whole {param} or literal
// RFC 3986 pchar (unreserved, sub-delims, ':' and '@') other than '*',
// '%' and the dot segments, with no empty segment but a trailing one. So
// the template means the same path to the runner policy, curl and the API.
func CheckPath(tmpl string) error {
	if !strings.HasPrefix(tmpl, "/") {
		return fmt.Errorf("path %q is not absolute", tmpl)
	}
	segs := strings.Split(tmpl, "/")[1:]
	for i, seg := range segs {
		if _, ok := wholeParam(seg); ok {
			continue
		}
		switch {
		case seg == "" && i != len(segs)-1:
			return fmt.Errorf("path %q has an empty segment", tmpl)
		case seg == "." || seg == "..":
			return fmt.Errorf("path %q has a dot segment", tmpl)
		case strings.IndexFunc(seg, func(r rune) bool { return !pchar(r) }) >= 0:
			return fmt.Errorf("path %q: segment %q is neither one whole {param} nor plain characters", tmpl, seg)
		}
	}
	return nil
}

// pchar reports whether r may appear in a literal segment: RFC 3986
// unreserved, sub-delims but '*', ':' and '@'.
func pchar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("-._~!$&'()+,;=:@", r)
}

// paramsMatch reports whether path has exactly one path Param per {param}.
func paramsMatch(path string, params []Param) bool {
	var inTemplate []string
	for _, seg := range strings.Split(path, "/")[1:] {
		if name, ok := wholeParam(seg); ok {
			inTemplate = append(inTemplate, name)
		}
	}
	var declared []string
	for _, p := range params {
		if p.In == "path" {
			declared = append(declared, p.Name)
		}
	}
	slices.Sort(inTemplate)
	slices.Sort(declared)
	return slices.Equal(inTemplate, declared) && len(slices.Compact(slices.Clone(inTemplate))) == len(inTemplate)
}

// wholeParam reports whether seg is "{name}" and returns name.
func wholeParam(seg string) (string, bool) {
	if len(seg) < 3 || seg[0] != '{' || seg[len(seg)-1] != '}' {
		return "", false
	}
	name := seg[1 : len(seg)-1]
	if strings.ContainsAny(name, "{}*/") {
		return "", false
	}
	return name, true
}

// sanitizeID maps an operationId to [a-z0-9_]: camelCase becomes
// snake_case, every other character an underscore, runs collapse and the
// ends are trimmed.
func sanitizeID(id string) string {
	var b strings.Builder
	prevLowerOrDigit := false
	for _, r := range id {
		switch {
		case r >= 'A' && r <= 'Z':
			if prevLowerOrDigit {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			prevLowerOrDigit = false
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevLowerOrDigit = true
		default:
			b.WriteByte('_')
			prevLowerOrDigit = false
		}
	}
	parts := strings.FieldsFunc(b.String(), func(r rune) bool { return r == '_' })
	return strings.Join(parts, "_")
}
