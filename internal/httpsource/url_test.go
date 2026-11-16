package httpsource

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

var issuesParams = []Param{
	{Name: "owner", In: "path", Type: "string", Required: true},
	{Name: "repo", In: "path", Type: "string", Required: true},
	{Name: "state", In: "query", Type: "string", Enum: []string{"open", "closed", "all"}},
	{Name: "per_page", In: "query", Type: "integer"},
	{Name: "pretty", In: "query", Type: "boolean"},
	{Name: "q", In: "query", Type: "string"},
}

const issuesPath = "/repos/{owner}/{repo}/issues"

func TestBuildURL_Happy(t *testing.T) {
	got, err := BuildURL("https://api.github.com", issuesPath, map[string]any{"owner": "cli", "repo": "cli", "state": "open"}, issuesParams)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.github.com/repos/cli/cli/issues?state=open" {
		t.Fatalf("url = %s", got)
	}
	// A base path is kept; integers (as JSON decodes them), booleans and a
	// string with spaces encode in the query.
	got, err = BuildURL("https://h.example.com/api/v3/", issuesPath, map[string]any{
		"owner": "a", "repo": "b", "per_page": float64(5), "pretty": true, "q": "is:open label:bug",
	}, issuesParams)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://h.example.com/api/v3/repos/a/b/issues?per_page=5&pretty=true&q=is%3Aopen+label%3Abug" {
		t.Fatalf("url = %s", got)
	}
	// json.Number is an integer too.
	if _, err := BuildURL("https://h.example.com", issuesPath, map[string]any{"owner": "a", "repo": "b", "per_page": json.Number("7")}, issuesParams); err != nil {
		t.Fatal(err)
	}
}

// A path value stays inside its one segment, whatever it holds.
func TestBuildURL_ParamCannotEscapeTemplate(t *testing.T) {
	for _, owner := range []string{"..admin", "a?b=c", "a#b", "a[1]{2}", "a b", "a;b=c"} {
		t.Run(owner, func(t *testing.T) {
			got, err := BuildURL("https://api.github.com", issuesPath, map[string]any{"owner": owner, "repo": "r"}, issuesParams)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			if u.Host != "api.github.com" || u.RawQuery != "" || u.Fragment != "" {
				t.Fatalf("url %s: host %q query %q fragment %q", got, u.Host, u.RawQuery, u.Fragment)
			}
			segs := strings.Split(u.EscapedPath(), "/")
			if len(segs) != 5 || segs[1] != "repos" || segs[3] != "r" || segs[4] != "issues" {
				t.Fatalf("url %s: segments %q", got, segs)
			}
			if back, _ := url.PathUnescape(segs[2]); back != owner {
				t.Fatalf("segment %q decodes to %q, want %q", segs[2], back, owner)
			}
			if strings.ContainsAny(segs[2], "[]{}") {
				t.Fatalf("segment %q holds a curl glob character", segs[2])
			}
		})
	}
}

func TestBuildURL_UnknownOrMissing(t *testing.T) {
	ok := map[string]any{"owner": "a", "repo": "b"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{"owner": "a", "repo": "b"}
		m[k] = v
		return m
	}
	for name, in := range map[string]map[string]any{
		"unknown key":        with("token", "x"),
		"missing required":   {"owner": "a"},
		"dot segment":        with("owner", ".."),
		"slash":              with("owner", "a/b"),
		"slash and dots":     with("owner", "x/../../admin"),
		"backslash":          with("owner", `a\..\b`),
		"encoded dots":       with("owner", "%2e%2e"),
		"encoded slash":      with("owner", "a%2Fb"),
		"single dot":         with("owner", "."),
		"empty path value":   with("owner", ""),
		"leading dash":       with("q", "-x"),
		"control char":       with("q", "a\nb"),
		"nul":                with("owner", "a\x00"),
		"enum miss":          with("state", "merged"),
		"string as integer":  with("per_page", "5"),
		"fraction":           with("per_page", 1.5),
		"string as bool":     with("pretty", "true"),
		"number as string":   with("q", float64(3)),
		"object":             with("q", map[string]any{"a": 1}),
		"null":               with("q", nil),
		"integer not number": with("per_page", json.Number("1e400")),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := BuildURL("https://api.github.com", issuesPath, in, issuesParams); err == nil {
				t.Fatalf("accepted: %s", got)
			}
		})
	}
	if _, err := BuildURL("https://api.github.com", issuesPath, ok, issuesParams); err != nil {
		t.Fatalf("base case refused: %v", err)
	}
	// The base URL must be https with a host.
	for _, base := range []string{"http://api.github.com", "https://", "::"} {
		if _, err := BuildURL(base, issuesPath, ok, issuesParams); err == nil {
			t.Errorf("base %q accepted", base)
		}
	}
}

func TestBuildURL_BooleanAndIntegerEnums(t *testing.T) {
	params := []Param{{Name: "flag", In: "query", Type: "boolean", Enum: []string{"true"}}, {Name: "n", In: "query", Type: "integer", Enum: []string{"1", "2"}}}
	if got, err := BuildURL("https://h.example.com", "/a", map[string]any{"flag": true, "n": float64(2)}, params); err != nil || got != "https://h.example.com/a?flag=true&n=2" {
		t.Fatalf("%s %v", got, err)
	}
	for _, in := range []map[string]any{{"flag": false}, {"n": float64(3)}} {
		if got, err := BuildURL("https://h.example.com", "/a", in, params); err == nil {
			t.Errorf("%v accepted: %s", in, got)
		}
	}
}
