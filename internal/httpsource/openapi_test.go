package httpsource

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/toolreg"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) // #nosec G304 -- a fixture under testdata
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Only GET operations with an operationId become operations; every other
// operation is counted as ignored. Path-level parameters (here through
// $ref) are merged in, and parameters aimux cannot send (an optional
// header) are dropped.
func TestOperations_OnlyGETWithID(t *testing.T) {
	ops, ignored, err := Operations(fixture(t, "mixed.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 4 { // post, get without operationId, put, delete
		t.Errorf("ignored = %+v, want 4", ignored)
	}
	want := []Operation{
		{ID: "issues_list_for_repo", Path: "/repos/{owner}/{repo}/issues", Description: "List repository issues", Params: []Param{
			{Name: "owner", In: "path", Type: "string", Required: true},
			{Name: "repo", In: "path", Type: "string", Required: true},
			{Name: "state", In: "query", Type: "string", Enum: []string{"open", "closed", "all"}},
			{Name: "per_page", In: "query", Type: "integer"},
		}},
		{ID: "meta_get_zen", Path: "/zen", Params: []Param{{Name: "pretty", In: "query", Type: "boolean"}}},
	}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops =\n%+v\nwant\n%+v", ops, want)
	}
}

// JSON specs and OpenAPI 3.1 parse too. An operation-level parameter
// replaces the path-level one of the same name and location; an optional
// parameter of a type aimux does not send is dropped; an operation with a
// required one it cannot send is ignored.
func TestOperations_JSONOverridesAndUnsupported(t *testing.T) {
	ops, ignored, err := Operations(fixture(t, "override.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 {
		t.Errorf("ignored = %+v, want 1 (the required header)", ignored)
	}
	want := []Operation{{ID: "get_item", Path: "/items/{id}", Params: []Param{
		{Name: "id", In: "path", Type: "integer", Required: true},
		{Name: "fields", In: "query", Type: "string"},
		{Name: "note", In: "query", Type: "string"},
	}}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops =\n%+v\nwant\n%+v", ops, want)
	}
}

func TestOperations_Refused(t *testing.T) {
	big := append([]byte("openapi: 3.0.0\npaths: {}\n#"), bytes.Repeat([]byte("x"), MaxSpecBytes)...)
	for name, spec := range map[string]string{
		"swagger 2":     "swagger: '2.0'\npaths: {}\n",
		"no version":    "paths: {}\n",
		"not yaml":      "openapi: [3\n",
		"too big":       string(big),
		"dup operation": "openapi: 3.0.0\npaths:\n  /a:\n    get: {operationId: x}\n  /b:\n    get: {operationId: x}\n",
		"dup sanitized": "openapi: 3.0.0\npaths:\n  /a:\n    get: {operationId: get-x}\n  /b:\n    get: {operationId: get_x}\n",
		"dangling ref":  "openapi: 3.0.0\npaths:\n  /a:\n    get:\n      operationId: x\n      parameters: [{$ref: '#/components/parameters/nope'}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Operations([]byte(spec)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// A path the runner policy could not express exactly, or one whose
// {param} has no path parameter, is ignored rather than served.
func TestOperations_BadPathsIgnored(t *testing.T) {
	spec := `openapi: 3.0.0
paths:
  /a/{x}y:
    get: {operationId: partial, parameters: [{name: x, in: path, required: true, schema: {type: string}}]}
  /a/*:
    get: {operationId: star}
  /a/../b:
    get: {operationId: dots}
  /a/{undeclared}:
    get: {operationId: undeclared}
  relative:
    get: {operationId: relative}
  /ok/{id}:
    get: {operationId: ok, parameters: [{name: id, in: path, required: true, schema: {type: string}}]}
`
	ops, ignored, err := Operations([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].ID != "ok" || len(ignored) != 5 {
		t.Fatalf("ops %+v ignored %+v", ops, ignored)
	}
}

func TestToolName(t *testing.T) {
	if got := ToolName("api", Operation{ID: "get_x"}); got != "api_get_x" {
		t.Fatalf("ToolName = %q", got)
	}
	for in, want := range map[string]string{
		"issues/list-for-repo": "issues_list_for_repo",
		"getItem":              "get_item",
		"GetHTTPThing2":        "get_httpthing2",
		"__a..b__":             "a_b",
	} {
		if got := sanitizeID(in); got != want {
			t.Errorf("sanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
}

// A missing or unreadable spec names its path, and the size cap holds for
// files too.
func TestSourceOperations_FileErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.yaml")
	_, err := SourceOperations(toolreg.Source{Type: toolreg.TypeHTTPOpenAPI, Spec: missing})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing spec: %v", err)
	}
	big := filepath.Join(dir, "big.yaml")
	if err := os.WriteFile(big, bytes.Repeat([]byte("#"), MaxSpecBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SourceOperations(toolreg.Source{Spec: big}); err == nil || !strings.Contains(err.Error(), big) || !strings.Contains(err.Error(), "5 MiB") {
		t.Fatalf("big spec: %v", err)
	}
	ok := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(ok, fixture(t, "mixed.yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops, err := SourceOperations(toolreg.Source{Spec: ok})
	if err != nil || len(ops) != 2 {
		t.Fatalf("ops %d err %v", len(ops), err)
	}
}

// Literal path segments are RFC 3986 pchar only: no '%', brackets,
// backslash, whitespace or empty inner segment, so the template means one
// thing to the policy, curl and the API. Each skipped operation says why.
func TestOperations_LiteralSegments(t *testing.T) {
	bad := []string{`/files/[a-z]`, `/a%2F..%2Fadmin`, `/a\..\b`, `/sp ace`, `/a//b/`, `/a/b*c`, `/a/"q"`, `/a/b|c`, "/a/\u00e9"}
	good := []string{`/users/`, `/a/b:c@d!$&'()+,;=~-._`, `/`}
	var spec strings.Builder
	spec.WriteString("openapi: 3.0.0\npaths:\n")
	for i, p := range append(append([]string{}, bad...), good...) {
		fmt.Fprintf(&spec, "  %q:\n    get: {operationId: op%d}\n", p, i)
	}
	ops, ignored, err := Operations([]byte(spec.String()))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range ops {
		got = append(got, op.Path)
	}
	slices.Sort(got)
	want := slices.Clone(good)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("served %q, want %q", got, want)
	}
	if len(ignored) != len(bad) {
		t.Fatalf("ignored %+v", ignored)
	}
	for _, ig := range ignored {
		if ig.Method != "get" || ig.Path == "" || ig.Reason == "" {
			t.Errorf("ignored without a reason: %+v", ig)
		}
	}
}

// A spec with more than MaxOperations GET operations is refused.
func TestOperations_Cap(t *testing.T) {
	spec := func(n int) []byte {
		var b strings.Builder
		b.WriteString("openapi: 3.0.0\npaths:\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "  /p%d:\n    get: {operationId: op%d}\n", i, i)
		}
		return []byte(b.String())
	}
	if ops, _, err := Operations(spec(MaxOperations)); err != nil || len(ops) != MaxOperations {
		t.Fatalf("at the cap: %d ops, %v", len(ops), err)
	}
	if _, _, err := Operations(spec(MaxOperations + 1)); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("over the cap: %v", err)
	}
}

func TestCheckPath(t *testing.T) {
	for _, p := range []string{"/repos/{owner}/{repo}/issues", "/", "/users/"} {
		if err := CheckPath(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"", "repos", "/a?b", "/a#b", "/a/{x}y", "/a/*", "/a/../b", "/a/./b", "/a//b", "/a%20b", "/a b", "/[x]", `/a\b`} {
		if err := CheckPath(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

// Enums on booleans and integers are kept; a schema $ref (not resolved)
// refuses the operation rather than guessing a type.
func TestOperations_EnumsAndSchemaRefs(t *testing.T) {
	spec := `openapi: 3.0.0
paths:
  /a:
    get:
      operationId: a
      parameters:
        - {name: flag, in: query, schema: {type: boolean, enum: [true]}}
        - {name: n, in: query, schema: {type: integer, enum: [1, 2]}}
  /b:
    get:
      operationId: b
      parameters:
        - {name: state, in: query, schema: {$ref: '#/components/schemas/State'}}
components:
  schemas:
    State: {type: string, enum: [open]}
`
	ops, ignored, err := Operations([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	want := []Operation{{ID: "a", Path: "/a", Params: []Param{
		{Name: "flag", In: "query", Type: "boolean", Enum: []string{"true"}},
		{Name: "n", In: "query", Type: "integer", Enum: []string{"1", "2"}},
	}}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v", ops)
	}
	if len(ignored) != 1 || ignored[0].Path != "/b" || !strings.Contains(ignored[0].Reason, "$ref") {
		t.Fatalf("ignored = %+v", ignored)
	}
}

// A path or query parameter that declares a serialization other than the
// default (which BuildURL uses) skips its operation; explicit defaults
// still load.
func TestOperations_ParameterSerialization(t *testing.T) {
	op := func(param string) []byte {
		return []byte("openapi: 3.0.0\npaths:\n  /a/{id}:\n    get:\n      operationId: a\n      parameters:\n" +
			"        - {name: id, in: path, required: true, schema: {type: string}}\n" +
			"        - " + param + "\n")
	}
	for name, param := range map[string]string{
		"query style":      "{name: q, in: query, style: spaceDelimited, schema: {type: string}}",
		"query explode":    "{name: q, in: query, explode: false, schema: {type: string}}",
		"allowReserved":    "{name: q, in: query, allowReserved: true, schema: {type: string}}",
		"allowEmptyValue":  "{name: q, in: query, allowEmptyValue: true, schema: {type: string}}",
		"content":          "{name: q, in: query, content: {application/json: {schema: {type: object}}}}",
		"content optional": "{name: q, in: query, required: false, content: {text/plain: {schema: {type: string}}}}",
		"path style":       "{name: id, in: path, required: true, style: matrix, schema: {type: string}}",
		"path explode":     "{name: id, in: path, required: true, explode: true, schema: {type: string}}",
		"required header":  "{name: X-K, in: header, required: true, schema: {type: string}}",
		"required cookie":  "{name: k, in: cookie, required: true, schema: {type: string}}",
	} {
		t.Run(name, func(t *testing.T) {
			ops, ignored, err := Operations(op(param))
			if err != nil {
				t.Fatal(err)
			}
			if len(ops) != 0 || len(ignored) != 1 || ignored[0].Reason == "" {
				t.Fatalf("ops %+v ignored %+v", ops, ignored)
			}
		})
	}
	for name, param := range map[string]string{
		"query defaults": "{name: q, in: query, style: form, explode: true, allowReserved: false, allowEmptyValue: false, schema: {type: string}}",
		"path defaults":  "{name: id, in: path, required: true, style: simple, explode: false, schema: {type: string}}",
	} {
		t.Run(name, func(t *testing.T) {
			ops, ignored, err := Operations(op(param))
			if err != nil || len(ops) != 1 || len(ignored) != 0 {
				t.Fatalf("ops %+v ignored %+v err %v", ops, ignored, err)
			}
		})
	}
	// An optional header or cookie is not sent, which the API must accept
	// (it is optional), so its operation still loads without it.
	ops, _, err := Operations(op("{name: X-K, in: header, schema: {type: string}}"))
	if err != nil || len(ops) != 1 || len(ops[0].Params) != 1 {
		t.Fatalf("optional header: %+v %v", ops, err)
	}
}
