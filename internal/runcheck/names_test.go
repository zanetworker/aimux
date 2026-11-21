package runcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// slackOutput is a planner's Slack lookup: names of a customer, a person and
// a codename, ordinary capitalized words, and public projects.
const slackOutput = `Jane Smith said Acme Bank wants the Zephyr pilot before Kubernetes 1.36. ` +
	`Configure the egress proxy first. The bank team uses OpenShell on Red Hat OpenShift. ` +
	`codename: bluefin. Next sync on Monday in March.`

func TestCheck_PrivateNames(t *testing.T) {
	p := Private{ToolOutputs: []string{slackOutput}}
	cases := []struct {
		query, reason string // reason "" means accepted
	}{
		// Names seen mid-sentence in private data, in any case or form.
		{"Acme Bank AI strategy", ReasonPrivateName},
		{"acme ai strategy", ReasonPrivateName},
		{"ACME's agent platform", ReasonPrivateName},
		{"jane smith keynote", ReasonPrivateName},
		{"zephyr pilot results", ReasonPrivateName},
		{"acme%20bank news", ReasonPrivateName},
		// A labeled lowercase codename.
		{"bluefin sandbox design", ReasonPrivateName},
		// Public names on the shipped list, as whole phrases.
		{"OpenShell egress proxy release notes", ""},
		{"Red Hat OpenShift sandboxing", ""},
		{"Kubernetes 1.36 network policy", ""},
		// A word capitalized only at a sentence start is ordinary.
		{"configure egress proxy for sandboxes", ""},
		// A word that also appears lowercase in private data is ordinary.
		{"bank sector agent adoption", ""},
		// Months and weekdays are never names.
		{"agent security news march 2026", ""},
		{"monday release cadence", ""},
	}
	for _, c := range cases {
		acc, rej := Check([]string{c.query}, p)
		switch {
		case c.reason == "" && (len(acc) != 1 || len(rej) != 0):
			t.Errorf("%q: accepted %v rejected %v, want accepted", c.query, acc, rej)
		case c.reason != "" && (len(acc) != 0 || len(rej) != 1 || rej[0].Reason != c.reason):
			t.Errorf("%q: accepted %v rejected %v, want rejected %s", c.query, acc, rej, c.reason)
		}
	}
}

// A public phrase is exempt only as a whole: "Red Hat" does not make "Red"
// on its own, or a private name next to it, public.
func TestCheck_PublicPhraseBoundaries(t *testing.T) {
	p := Private{ToolOutputs: []string{"We met Red Falcon and the Red Hat team. Then Acme joined."}}
	for q, want := range map[string]string{
		"red hat agent news":   "",
		"red falcon funding":   ReasonPrivateName,
		"red hat acme partner": ReasonPrivateName,
	} {
		acc, rej := Check([]string{q}, p)
		got := ""
		if len(rej) == 1 {
			got = rej[0].Reason
		}
		if got != want || (want == "" && len(acc) != 1) {
			t.Errorf("%q: accepted %v rejected %v, want %q", q, acc, rej, want)
		}
	}
}

// Names count from every private input: question, page, notes, tool inputs
// and outputs, earlier questions and answers.
func TestCheck_PrivateNamesFromEveryInput(t *testing.T) {
	text := "the call with Globex went well"
	for name, p := range map[string]Private{
		"question":    {Question: text},
		"page":        {PageText: text},
		"notes":       {Notes: text},
		"tool input":  {ToolInputs: []string{text}},
		"tool output": {ToolOutputs: []string{text}},
		"prior":       {Prior: []string{text}},
	} {
		if _, rej := Check([]string{"globex agents"}, p); len(rej) != 1 || rej[0].Reason != ReasonPrivateName {
			t.Errorf("%s: rejected %v, want private_name", name, rej)
		}
	}
}

// The owner's own public names extend the shipped list, as phrases.
func TestCheck_ExtraPublicNames(t *testing.T) {
	p := Private{ToolOutputs: []string{"We compared it with Daytona Cloud and Acme."}, PublicNames: []string{"Daytona Cloud"}}
	if acc, rej := Check([]string{"daytona cloud sandboxes"}, p); len(acc) != 1 {
		t.Fatalf("rejected %v, want an extra public name accepted", rej)
	}
	if _, rej := Check([]string{"daytona cloud acme"}, p); len(rej) != 1 || rej[0].Reason != ReasonPrivateName {
		t.Fatalf("rejected %v, want acme still refused next to a public name", rej)
	}
}

// A rejection never carries the name.
func TestCheck_PrivateNameReasonHidesName(t *testing.T) {
	_, rej := Check([]string{"acme bank strategy"}, Private{Notes: "talked to Acme Bank"})
	if len(rej) != 1 || strings.Contains(strings.ToLower(rej[0].String()), "acme") {
		t.Fatalf("rejection %v names the private name", rej)
	}
}

// LoadPublicNames reads one phrase per line, skipping blanks and # comments;
// a missing file is no names and no error.
func TestLoadPublicNames(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "public-names.txt")
	if err := os.WriteFile(f, []byte("# mine\nDaytona Cloud\n\n  E2B  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPublicNames(f)
	if err != nil || len(got) != 2 || got[0] != "Daytona Cloud" || got[1] != "E2B" {
		t.Fatalf("LoadPublicNames = %q, %v", got, err)
	}
	if got, err := LoadPublicNames(filepath.Join(dir, "missing.txt")); err != nil || got != nil {
		t.Fatalf("missing file = %q, %v; want nil, nil", got, err)
	}
}
