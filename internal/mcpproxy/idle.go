package mcpproxy

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/zanetworker/aimux/internal/jsonedit"
)

// Claude Code gives up on a silent tool call after an idle limit that
// depends on the transport: 30 minutes for a stdio server, 5 for an http
// one (Claude Code 2.1.319). Moved servers are http entries, so install
// sets the http limit to the stdio one in ~/.claude/settings.json's env,
// and uninstall takes it out again.
const (
	idleTimeoutKey   = "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT"
	idleTimeoutValue = "1800000" // ms: Claude Code's stdio idle limit

	idleAdded    = "added"     // install added the key to an existing env
	idleAddedEnv = "added-env" // install created env for it
)

// addIdleTimeout returns env with the idle limit set, and what it did
// (idleAdded, idleAddedEnv, or "" when env already had the key, whatever
// its value: the owner's own value wins). A nil env is an absent key.
func addIdleTimeout(env json.RawMessage) (json.RawMessage, string, error) {
	value := json.RawMessage(`"` + idleTimeoutValue + `"`)
	if env == nil {
		return json.RawMessage(`{"` + idleTimeoutKey + `": "` + idleTimeoutValue + `"}`), idleAddedEnv, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(env, &members); err != nil || members == nil {
		return nil, "", fmt.Errorf("env in settings.json is not an object")
	}
	if _, ok := members[idleTimeoutKey]; ok {
		return env, "", nil
	}
	out, err := jsonedit.SetMember(env, idleTimeoutKey, value)
	if err != nil {
		return nil, "", err
	}
	return out, idleAdded, nil
}

// removeIdleTimeout undoes addIdleTimeout: the key goes only when install
// added it (added) and it still holds install's value; env itself goes when
// install created it and nothing else is left in it. nil means remove env.
func removeIdleTimeout(env json.RawMessage, added string) (json.RawMessage, error) {
	if env == nil || added == "" {
		return env, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(env, &members); err != nil || members == nil {
		return env, nil // not ours to repair
	}
	var v string
	if raw, ok := members[idleTimeoutKey]; !ok || json.Unmarshal(raw, &v) != nil || v != idleTimeoutValue {
		return env, nil // gone, or the owner changed it
	}
	if added == idleAddedEnv && len(members) == 1 {
		return nil, nil
	}
	return jsonedit.SetMember(env, idleTimeoutKey, nil)
}

// ensureIdleTimeout sets the idle limit in i.Settings and records it in m.
// A failure is a note, not an install failure: the servers are moved
// either way, and a slow tool only times out sooner.
func (i Installer) ensureIdleTimeout(m *Manifest) {
	if i.Settings == "" || m == nil || m.IdleTimeout != "" {
		return
	}
	if !fileExists(i.Settings) {
		i.printf("Note: %s does not exist, so %s was not set; tool calls through the proxy that stay silent for 5 minutes will time out (30 for direct servers).\n", i.Settings, idleTimeoutKey)
		return
	}
	var did string
	err := i.editKey(i.Settings, "env", func(old json.RawMessage) (json.RawMessage, error) {
		v, d, err := addIdleTimeout(old)
		did = d
		return v, err
	})
	if err != nil {
		i.printf("Note: could not set %s in %s (%v); silent tool calls through the proxy time out after 5 minutes.\n", idleTimeoutKey, i.Settings, err)
		return
	}
	if did == "" {
		return
	}
	m.IdleTimeout = did
	if err := i.saveManifest(m); err != nil {
		i.printf("Note: set %s in %s but could not record it (%v); uninstall will leave it.\n", idleTimeoutKey, i.Settings, err)
		return
	}
	i.printf("Set %s=%s in %s: Claude Code waits as long on a silent call through the proxy (an http server, idle limit 5 minutes) as on a direct stdio one (30 minutes).\n", idleTimeoutKey, idleTimeoutValue, i.Settings)
}

// dropIdleTimeout removes what ensureIdleTimeout added, if anything.
func (i Installer) dropIdleTimeout(m *Manifest) {
	if i.Settings == "" || m == nil || m.IdleTimeout == "" {
		return
	}
	if _, err := os.Stat(i.Settings); err != nil {
		return
	}
	if err := i.editKey(i.Settings, "env", func(old json.RawMessage) (json.RawMessage, error) {
		return removeIdleTimeout(old, m.IdleTimeout)
	}); err != nil {
		i.printf("Note: could not remove %s from %s (%v); remove it by hand if you want Claude Code's default back.\n", idleTimeoutKey, i.Settings, err)
	}
}
