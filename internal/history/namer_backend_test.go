package history

import (
	"testing"

	"github.com/zanetworker/aimux/internal/llm"
)

// TestNamingBackends: the namer's backends are the shared client's, aimed at
// the endpoint tests can replace. Backend behaviour is tested in internal/llm.
func TestNamingBackends(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	p, fb := NamingBackends("")
	if _, ok := p.(*llm.CLI); !ok || fb != nil {
		t.Errorf("no key: primary %T fallback %v, want claude -p only", p, fb)
	}
	old := anthropicMessagesURL
	anthropicMessagesURL = "http://fake.invalid"
	t.Cleanup(func() { anthropicMessagesURL = old })
	p, fb = NamingBackends("cfg-key")
	if a, ok := p.(*llm.API); !ok || a.Key != "cfg-key" || a.URL != "http://fake.invalid" {
		t.Errorf("config key: primary %T", p)
	}
	if _, ok := fb.(*llm.CLI); !ok {
		t.Errorf("fallback %T, want claude -p", fb)
	}
}
