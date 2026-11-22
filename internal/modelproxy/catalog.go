package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// leanCatalog rewrites Codex's model catalog so that every model offers the model only the client's dynamic
// tools: direct tool mode (no JavaScript exec runner), no multi-agent version (no sub-agent tools or team
// instructions), no apply_patch and no experimental tools. Every other field is kept as Codex reported it.
func leanCatalog(raw []byte) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("parse Codex model catalog: %w", err)
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(top["models"], &models); err != nil || len(models) == 0 {
		return nil, errors.New("codex model catalog has no model objects")
	}
	for _, m := range models {
		m["tool_mode"] = json.RawMessage(`"direct"`)
		delete(m, "multi_agent_version")
		m["apply_patch_tool_type"] = json.RawMessage(`null`)
		m["experimental_supported_tools"] = json.RawMessage(`[]`)
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		return nil, fmt.Errorf("encode lean Codex model catalog: %w", err)
	}
	top["models"] = encoded
	return json.Marshal(top)
}

// writeLeanCatalog reads the live catalog with `<command> debug models` (Codex refreshes it from OpenAI first),
// makes it lean and writes it to a new private (0600) temp file. The caller removes the file.
func writeLeanCatalog(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "debug", "models").Output() // #nosec G204 -- the owner-configured Codex command, fixed args
	if err != nil {
		detail := ""
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			detail = ": " + strings.TrimSpace(string(exit.Stderr))
		}
		return "", fmt.Errorf("read Codex model catalog with %q debug models: %w%s", command, err, detail)
	}
	lean, err := leanCatalog(out)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "aimux-codex-catalog-*.json")
	if err != nil {
		return "", fmt.Errorf("create lean model catalog file: %w", err)
	}
	if _, err := f.Write(lean); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write lean model catalog file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close lean model catalog file: %w", err)
	}
	return f.Name(), nil
}
