package modelproxy

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const sampleCatalog = `{"fetched_at":"2026-11-21","models":[
	{"slug":"gpt-6.2-sol","context_window":500000,"tool_mode":"code_mode_only","multi_agent_version":"v2",
	 "apply_patch_tool_type":"freeform","experimental_supported_tools":["clock","send_message_to_user_async"],
	 "model_messages":{"collaboration_modes":{"x":1}}},
	{"slug":"gpt-5.4","context_window":272000,"tool_mode":"direct","apply_patch_tool_type":"function"}]}`

func TestLeanCatalog(t *testing.T) {
	out, err := leanCatalog([]byte(sampleCatalog))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		FetchedAt string           `json:"fetched_at"`
		Models    []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("lean catalog is not JSON: %v\n%s", err, out)
	}
	if got.FetchedAt != "2026-11-21" || len(got.Models) != 2 {
		t.Fatalf("top-level fields or model count changed: %s", out)
	}
	for _, m := range got.Models {
		if m["tool_mode"] != "direct" {
			t.Errorf("%v: tool_mode = %v, want direct", m["slug"], m["tool_mode"])
		}
		if _, ok := m["multi_agent_version"]; ok {
			t.Errorf("%v: multi_agent_version kept", m["slug"])
		}
		if v, ok := m["apply_patch_tool_type"]; !ok || v != nil {
			t.Errorf("%v: apply_patch_tool_type = %v (present %v), want null", m["slug"], v, ok)
		}
		if tools, ok := m["experimental_supported_tools"].([]any); !ok || len(tools) != 0 {
			t.Errorf("%v: experimental_supported_tools = %v, want []", m["slug"], m["experimental_supported_tools"])
		}
	}
	sol := got.Models[0]
	if sol["context_window"] != float64(500000) || sol["model_messages"] == nil {
		t.Fatalf("unrelated fields were not preserved: %v", sol)
	}
}

func TestLeanCatalogErrors(t *testing.T) {
	for name, in := range map[string]string{
		"not json":               `{`,
		"no models":              `{"fetched_at":"x"}`,
		"empty models":           `{"models":[]}`,
		"model is not an object": `{"models":["gpt"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := leanCatalog([]byte(in)); err == nil {
				t.Fatalf("leanCatalog(%s) succeeded, want an error", in)
			}
		})
	}
}

func TestWriteLeanCatalogFromCommand(t *testing.T) {
	path, err := writeLeanCatalog(context.Background(), fakeCodex)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog file %s: mode %v, err %v; want 0600", path, info.Mode().Perm(), err)
	}
	data, _ := os.ReadFile(path) // #nosec G304 -- test temp file
	if !strings.Contains(string(data), `"tool_mode":"direct"`) || strings.Contains(string(data), "code_mode_only") {
		t.Fatalf("catalog was not made lean: %s", data)
	}
}

func TestWriteLeanCatalogCommandFailure(t *testing.T) {
	t.Setenv("AIMUX_FAKE_CODEX_CATALOG_FAIL", "1")
	if _, err := writeLeanCatalog(context.Background(), fakeCodex); err == nil || !strings.Contains(err.Error(), "model catalog") {
		t.Fatalf("got %v, want a model catalog error", err)
	}
}

func TestCodexBackendRefusesToStartWithoutLeanCatalog(t *testing.T) {
	backend, _ := newFakeBackend(t)
	t.Setenv("AIMUX_FAKE_CODEX_CATALOG_FAIL", "1")
	_, err := backend.Run(context.Background(), fakeTurn("hello"), func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "model catalog") {
		t.Fatalf("got %v, want the backend to refuse without a lean catalog", err)
	}
}
