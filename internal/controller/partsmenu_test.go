package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/parts"
)

func menuOf(t *testing.T, yaml string) map[string]PartChoice {
	t.Helper()
	s, err := parts.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]PartChoice{}
	for _, c := range PartsMenu(s) {
		out[c.ID] = c
	}
	return out
}

func TestPartsMenu_NothingStated(t *testing.T) {
	s, _ := parts.Parse(nil)
	menu := PartsMenu(s)
	if len(menu) != len(parts.Parts()) {
		t.Fatalf("%d lines for %d parts", len(menu), len(parts.Parts()))
	}
	for i, p := range parts.Parts() {
		if menu[i].ID != p.ID || menu[i].Name != p.Name || menu[i].About != p.About {
			t.Errorf("line %d = %+v, want part %s in the registry's order", i, menu[i], p.ID)
		}
	}
	got := menuOf(t, "")
	// The two parts aimux starts a service for are offered, numbered from 1 in menu order.
	if c := got["inference-proxy"]; c.State != PartNotSet || c.Number != 1 || c.Why != "" {
		t.Errorf("inference proxy = %+v", c)
	}
	if c := got["mcp-proxy"]; c.State != PartNotSet || c.Number != 2 {
		t.Errorf("mcp proxy = %+v", c)
	}
	if c := got["dashboard"]; c.State != PartOn || c.Number != 0 || c.Why == "" {
		t.Errorf("dashboard = %+v", c)
	}
	for _, id := range []string{"schedules", "evals"} {
		if c := got[id]; c.State != PartNotAvailable || c.Number != 0 {
			t.Errorf("%s = %+v", id, c)
		}
	}
	// Its tool gateway runs another session's copy: shown, never offered.
	if c := got["sandboxes"]; c.Number != 0 || c.Why != "its tool gateway is managed by Wingman" {
		t.Errorf("sandboxes = %+v", c)
	}
	if c := got["phone-access"]; c.Number != 0 || c.Why == "" {
		t.Errorf("phone access = %+v", c)
	}
}

func TestPartsMenu_APartThatIsOnIsNotOffered(t *testing.T) {
	got := menuOf(t, "parts:\n  inference-proxy:\n    enabled: true\n  mcp-proxy:\n    enabled: false\n  sandboxes:\n    enabled: true\n")
	if c := got["inference-proxy"]; c.State != PartOn || c.Number != 0 {
		t.Errorf("inference proxy = %+v", c)
	}
	// The only one left to choose takes number 1.
	if c := got["mcp-proxy"]; c.State != PartOff || c.Number != 1 {
		t.Errorf("mcp proxy = %+v", c)
	}
	if c := got["sandboxes"]; c.State != PartOn || c.Number != 0 {
		t.Errorf("sandboxes = %+v", c)
	}
}
