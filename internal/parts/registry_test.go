package parts

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestServices_AreTheOnesStatusAndUpgradeKnow(t *testing.T) {
	type row struct {
		id, name, label string
		port            int
		managed         bool
		update          string
	}
	want := []row{
		{"model-proxy", "model proxy", "com.aimux.model-proxy", 7385, true, "aimux upgrade model-proxy"},
		{"mcp-proxy", "mcp proxy", "com.aimux.mcp-proxy", 7381, true, "aimux upgrade mcp-proxy"},
		{"collector", "collector", "com.azaalouk.aimux-collect", 4318, true, "aimux upgrade collector"},
		{"tool-gateway", "tool gateway", "com.aimux.tool-gateway", 7390, false, "aimux mcp proxy gateway install"},
	}
	var got []row
	for _, s := range Services() {
		got = append(got, row{s.ID, s.Name, s.Label, s.Port, s.Managed, s.Update})
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Services() =\n%+v\nwant\n%+v", got, want)
	}
	// The caller gets its own copy: changing it changes nothing for the next caller.
	Services()[0].Serve[0] = "changed"
	if Services()[0].Serve[0] != "model" {
		t.Error("Services() hands out its own table")
	}
}

func TestServiceByID(t *testing.T) {
	if s, ok := ServiceByID("collector"); !ok || s.Label != "com.azaalouk.aimux-collect" || !slices.Equal(s.Serve, []string{"collect"}) {
		t.Errorf("ServiceByID(collector) = %+v, %v", s, ok)
	}
	for _, id := range []string{"", "model proxy", "Model-Proxy", "nope"} {
		if _, ok := ServiceByID(id); ok {
			t.Errorf("ServiceByID(%q) found something", id)
		}
	}
}

func TestParts_NameOnlyServicesThatExist(t *testing.T) {
	seen := map[string]bool{}
	always := 0
	for _, p := range Parts() {
		if p.ID == "" || p.Name == "" || p.About == "" || seen[p.ID] {
			t.Errorf("part %+v: every part has an id of its own, a name and a line for the menu", p)
		}
		seen[p.ID] = true
		if p.Always {
			always++
		}
		for _, id := range p.Services {
			if _, ok := ServiceByID(id); !ok {
				t.Errorf("part %s names the service %q, which is not in the registry", p.ID, id)
			}
		}
	}
	if !seen["inference-proxy"] || !seen["mcp-proxy"] || always != 1 {
		t.Errorf("parts = %v, %d always on; want the inference proxy and the MCP proxy among them and the dashboard always on", seen, always)
	}
	if p, ok := PartByID("inference-proxy"); !ok || !slices.Equal(p.Services, []string{"model-proxy"}) || p.Name != "Inference proxy" {
		t.Errorf("PartByID(inference-proxy) = %+v, %v", p, ok)
	}
}

// The two commands that list the services read the registry: a launchd label written out in either is a second
// list, which is how the lists drifted before.
func TestStatusAndUpgradeReadTheRegistry(t *testing.T) {
	label := regexp.MustCompile(`"com\.(aimux|azaalouk)\.[a-z-]+"`)
	for _, file := range []string{"upgrade.go", "status_probes.go"} {
		path := filepath.Join("..", "..", "cmd", "aimux", "cmd", file)
		source, err := os.ReadFile(path) // #nosec G304 -- two files of this repository
		if err != nil {
			t.Fatal(err)
		}
		if found := label.FindAllString(string(source), -1); len(found) > 0 {
			t.Errorf("%s holds %s; take it from internal/parts", file, strings.Join(found, ", "))
		}
	}
}
