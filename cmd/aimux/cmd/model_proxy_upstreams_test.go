package cmd

import (
	"strings"
	"testing"
)

func TestParseMessagesUpstreams(t *testing.T) {
	got, err := parseMessagesUpstreams(
		[]string{"enmaas=https://gw.example", "lab=http://127.0.0.1:9000"},
		[]string{`lab={"env":{"name":"LAB_KEY"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].name != "enmaas" || got[0].address != "https://gw.example" || got[0].ref != nil || got[0].keyAt() != "" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].ref == nil || got[1].ref.Env == nil || got[1].ref.Env.Name != "LAB_KEY" || got[1].keyAt() != "the environment variable LAB_KEY" {
		t.Errorf("second = %+v, kept at %q", got[1], got[1].keyAt())
	}
	for name, tc := range map[string]struct {
		pairs, keys []string
		want        string
	}{
		"no address":                          {[]string{"enmaas"}, nil, "name=address"},
		"a name twice":                        {[]string{"a=https://x.example", "a=https://y.example"}, nil, "twice"},
		"a key for a name that is not there":  {[]string{"a=https://x.example"}, []string{`b={"env":{"name":"K"}}`}, "name=reference"},
		"a key where a reference belongs":     {[]string{"a=https://x.example"}, []string{"a=sk-live-pasted-by-mistake"}, "never the key itself"},
		"a reference with a misspelling":      {[]string{"a=https://x.example"}, []string{`a={"keychain":{"item":"x","feild":"y"}}`}, "never the key itself"},
		"a reference with something after it": {[]string{"a=https://x.example"}, []string{`a={"env":{"name":"K"}} sk-live-pasted-by-mistake`}, "never the key itself"},
		"a key given twice for one name":      {[]string{"a=https://x.example"}, []string{`a={"env":{"name":"K"}}`, `a={"env":{"name":"L"}}`}, "twice"},
		"a reference to two places":           {[]string{"a=https://x.example"}, []string{`a={"keychain":{"item":"x"},"env":{"name":"K"}}`}, "never the key itself"},
	} {
		_, err := parseMessagesUpstreams(tc.pairs, tc.keys)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "sk-live-pasted-by-mistake") {
			t.Errorf("%s: the error repeats what was pasted", name)
		}
	}
}
