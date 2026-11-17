package mcpproxy

import "testing"

func TestReload_HTTPDefinitionChangeRetiresBackend(t *testing.T) {
	cur := ServerSet{Configs: map[string]ServerConfig{"dw": {Type: "http", URL: "https://a/mcp"}}}
	next := ServerSet{Configs: map[string]ServerConfig{"dw": {Type: "http", URL: "https://b/mcp"}}}
	if !definitionChanged(cur, next, "dw") {
		t.Fatal("URL change not seen")
	}
	same := ServerSet{Configs: map[string]ServerConfig{"dw": {Type: "http", URL: "https://a/mcp"}}}
	if definitionChanged(cur, same, "dw") {
		t.Fatal("no change seen as change")
	}
	if !sameDefinition(cur.Configs["dw"], same.Configs["dw"]) || sameDefinition(cur.Configs["dw"], next.Configs["dw"]) {
		t.Fatal("sameDefinition ignores the URL")
	}
}
