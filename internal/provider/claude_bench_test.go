package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkParseTrace_50Turns(b *testing.B) {
	// Build a synthetic JSONL file with 50 realistic turns
	// (alternating human/assistant messages with tool use).
	tmpDir := b.TempDir()
	path := filepath.Join(tmpDir, "bench-session.jsonl")

	var lines []byte
	baseTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 50; i++ {
		userTS := baseTime.Add(time.Duration(i*2) * time.Minute)
		assistTS := userTS.Add(30 * time.Second)

		userEntry := map[string]interface{}{
			"type":      "user",
			"timestamp": userTS.Format(time.RFC3339Nano),
			"message": map[string]interface{}{
				"role":    "user",
				"content": fmt.Sprintf("Fix the bug in module %d and add tests", i),
			},
		}

		assistEntry := map[string]interface{}{
			"type":      "assistant",
			"timestamp": assistTS.Format(time.RFC3339Nano),
			"message": map[string]interface{}{
				"role":  "assistant",
				"model": "claude-opus-4-6",
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": fmt.Sprintf("I'll fix the bug in module %d. Let me read the file first.", i),
					},
					{
						"type":  "tool_use",
						"id":    fmt.Sprintf("tu_%d_read", i),
						"name":  "Read",
						"input": map[string]interface{}{"file_path": fmt.Sprintf("internal/pkg%d/handler.go", i)},
					},
					{
						"type":  "tool_use",
						"id":    fmt.Sprintf("tu_%d_edit", i),
						"name":  "Edit",
						"input": map[string]interface{}{"file_path": fmt.Sprintf("internal/pkg%d/handler.go", i), "old_string": "old code", "new_string": "new code"},
					},
				},
				"usage": map[string]interface{}{
					"input_tokens":  800 + i*10,
					"output_tokens": 200 + i*5,
				},
			},
		}

		toolResultEntry := map[string]interface{}{
			"type":      "user",
			"timestamp": assistTS.Add(5 * time.Second).Format(time.RFC3339Nano),
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": fmt.Sprintf("tu_%d_read", i),
						"content":     "package pkg\n\nfunc Handler() error {\n\treturn nil\n}\n",
					},
					{
						"type":        "tool_result",
						"tool_use_id": fmt.Sprintf("tu_%d_edit", i),
						"content":     "OK",
					},
				},
			},
		}

		for _, entry := range []interface{}{userEntry, assistEntry, toolResultEntry} {
			b, err := json.Marshal(entry)
			if err != nil {
				panic(err)
			}
			lines = append(lines, b...)
			lines = append(lines, '\n')
		}
	}

	if err := os.WriteFile(path, lines, 0o600); err != nil {
		b.Fatal(err)
	}

	c := &Claude{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := c.ParseTrace(path)
		if err != nil {
			b.Fatal(err)
		}
	}
}
