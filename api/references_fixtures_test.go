package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The references fixtures are consumed by both clients. This keeps them
// well formed: the pass only wraps text, so the visible text is unchanged,
// and every span's target comes from the case's own context.

type refInline struct {
	T    string      `json:"t"`
	V    string      `json:"v"`
	Kind string      `json:"kind"`
	Href string      `json:"href"`
	Code bool        `json:"code"`
	C    []refInline `json:"c"`
}

type refCase struct {
	Name    string `json:"name"`
	Context struct {
		Repo         *struct{ Web, Branch string } `json:"repo"`
		JiraBase     *string                       `json:"jiraBase"`
		JiraProjects []string                      `json:"jiraProjects"`
		Project      *string                       `json:"project"`
	} `json:"context"`
	Inlines []refInline `json:"inlines"`
	Expect  []refInline `json:"expect"`
}

func flatten(in []refInline) string {
	var b strings.Builder
	for _, x := range in {
		b.WriteString(x.V)
		b.WriteString(flatten(x.C))
	}
	return b.String()
}

func walkRefs(in []refInline, f func(refInline)) {
	for _, x := range in {
		if x.T == "ref" {
			f(x)
		}
		walkRefs(x.C, f)
	}
}

func noAdjacentText(t *testing.T, in []refInline, where string) {
	t.Helper()
	for i, x := range in {
		if x.T == "text" && x.V == "" {
			t.Errorf("%s: empty text inline", where)
		}
		if i > 0 && x.T == "text" && in[i-1].T == "text" {
			t.Errorf("%s: adjacent text inlines %q %q", where, in[i-1].V, x.V)
		}
		noAdjacentText(t, x.C, where)
	}
}

func TestReferencesFixturesAreWellFormed(t *testing.T) {
	files, err := filepath.Glob("testdata/clients/references/*.json")
	if err != nil || len(files) < 7 {
		t.Fatalf("references fixtures: %v, %d files", err, len(files))
	}
	kinds := []string{"pr", "commit", "jira", "path"}
	for _, file := range files {
		raw, err := os.ReadFile(file) //nolint:gosec // a glob of the fixtures directory
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Name  string    `json:"name"`
			Cases []refCase `json:"cases"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil || doc.Name == "" || len(doc.Cases) == 0 {
			t.Fatalf("%s: %v, name %q, %d cases", file, err, doc.Name, len(doc.Cases))
		}
		for _, c := range doc.Cases {
			where := filepath.Base(file) + ": " + c.Name
			if c.Name == "" || len(c.Inlines) == 0 {
				t.Errorf("%s: missing name or inlines", where)
			}
			if got, want := flatten(c.Expect), flatten(c.Inlines); got != want {
				t.Errorf("%s: expect reads %q, input reads %q; the pass only wraps text", where, got, want)
			}
			noAdjacentText(t, c.Expect, where)
			walkRefs(c.Inlines, func(refInline) { t.Errorf("%s: input already holds a ref", where) })
			walkRefs(c.Expect, func(r refInline) {
				if !slices.Contains(kinds, r.Kind) || r.V == "" || !strings.HasPrefix(r.Href, "https://") {
					t.Errorf("%s: bad ref %+v", where, r)
				}
				switch r.Kind {
				case "jira":
					if c.Context.JiraBase == nil || !strings.HasPrefix(r.Href, strings.TrimRight(*c.Context.JiraBase, "/")+"/browse/") ||
						!slices.Contains(c.Context.JiraProjects, strings.Split(r.V, "-")[0]) {
						t.Errorf("%s: jira ref %+v does not follow from the context", where, r)
					}
				default:
					if c.Context.Repo == nil {
						t.Errorf("%s: %s ref without a repo", where, r.Kind)
					} else if host := strings.Join(strings.SplitN(c.Context.Repo.Web, "/", 4)[:3], "/"); !strings.HasPrefix(r.Href, host+"/") {
						t.Errorf("%s: ref %+v leaves the repo host %s", where, r, host)
					}
				}
				if r.Code && r.Kind != "path" {
					t.Errorf("%s: only a path can come from inline code: %+v", where, r)
				}
			})
		}
	}
}
