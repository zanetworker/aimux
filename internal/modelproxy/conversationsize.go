package modelproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// Codex keeps its own copy of a conversation and shortens it as it goes, so the prompt size it reports is the
// size of that copy. Claude Code holds the whole conversation and decides when to compact from the size it is
// told. Told Codex's figure, it never compacts: a session at over a million tokens was shown as 160,000, and
// could no longer be rebuilt once its Codex conversation was closed. So a Codex answer carries the size of the
// conversation the session sent, when that is the larger. The ledger keeps what Codex used.

// imageTokens is what one image is counted as. Its size in a request is its encoding, not its cost.
const imageTokens = 1500

// conversationMargin is how far the conversation has to exceed what Codex reports before Codex's copy is taken
// to have been shortened.
const conversationMargin = 20000

// charsPerToken is a rough rate for prose, code and JSON together. It errs toward more tokens, which errs
// toward compacting a little early.
const charsPerToken = 4

// conversationTokens estimates the size of the conversation a request holds: its system prompt, the tools it
// offers and every message, with each image counted as an image.
func conversationTokens(request messageRequest) int {
	chars, images := 0, 0
	add := func(c, i int) { chars, images = chars+c, images+i }
	add(blockSize(request.System))
	for _, tool := range request.Tools {
		chars += len(tool.Name) + len(tool.Description) + len(tool.InputSchema)
	}
	for _, message := range request.Messages {
		add(blockSize(message.Content))
	}
	return chars/charsPerToken + images*imageTokens
}

// blockDepth is how deep content is read: a message, its blocks, a tool result's blocks, and one more. What is
// nested deeper is counted by its length, which keeps the work in step with the size of the request.
const blockDepth = 4

// blockSize is the characters and images in a content value: a string, a block, or a list of either, read deep
// enough that a tool's result is counted with what it holds.
func blockSize(raw json.RawMessage) (chars, images int) { return blockSizeAt(raw, 0) }

func blockSizeAt(raw json.RawMessage, depth int) (chars, images int) {
	if len(raw) == 0 {
		return 0, 0
	}
	if depth > blockDepth {
		return len(raw), 0
	}
	switch raw[0] {
	case '"':
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return len(text), 0
		}
	case '[':
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) == nil {
			for _, item := range list {
				c, i := blockSizeAt(item, depth+1)
				chars, images = chars+c, images+i
			}
			return chars, images
		}
	case '{':
		var block struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			Input    json.RawMessage `json:"input"`
			Content  json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &block) == nil {
			if block.Type == "image" || block.Type == "document" {
				return 0, 1
			}
			c, i := blockSizeAt(block.Content, depth+1)
			return len(block.Text) + len(block.Thinking) + len(block.Input) + c, i
		}
	}
	// Something this does not know the shape of: counted by its length, which is never too little.
	return len(raw), 0
}

// shownUsage is the usage a Codex answer carries to the session: Codex's own figures, with the prompt raised to
// the size of the conversation when the conversation is the larger. What Codex read from its cache stays as it
// reported it; the rest is added as uncached input, and the figure is marked as not Codex's own.
//
// The estimate is rough, so far from the limit it replaces Codex's own count only when it is clearly the larger:
// by a quarter and by conversationMargin tokens. Near the limit it is taken as soon as it is the larger at all:
// from nearWindow of the answering model's window on, telling the session too little is what leaves it with a
// conversation that can no longer be rebuilt. window is 0 when the model's window is not known.
func shownUsage(codex Usage, conversation, window int) Usage {
	reported := codex.InputTokens + codex.CacheReadInputTokens
	if conversation <= reported {
		return codex
	}
	near := window > 0 && conversation >= window/100*nearWindow
	if !near && (conversation <= reported+reported/4 || conversation-reported <= conversationMargin) {
		return codex
	}
	shown := codex
	shown.InputTokens = conversation - codex.CacheReadInputTokens
	shown.estimated = true
	return shown
}

// nearWindow is the share of a model's window, in percent, from which the conversation's estimated size is told
// to the session whenever it exceeds what Codex reports.
const nearWindow = 60

// The variable Claude Code reads for the context window of a model whose name it does not know, and a marker
// holding what aimux set there, by the same rule as the picker entry.
const (
	ContextWindowVar    = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"
	ContextWindowMarker = "AIMUX_MODEL_PROXY_WINDOW"
)

// CodexContextWindow is how many tokens of conversation a Codex model takes, from the file Codex keeps of its
// models: the model's window, less the share Codex itself holds back, or a tenth where the file names none. ok is
// false when the file or the model is not there, or the window is not a size: then nothing is assumed.
func CodexContextWindow(cachePath, model string) (tokens int, ok bool) {
	data, err := os.ReadFile(cachePath) // #nosec G304 -- Codex's own list of its models, in the owner's home
	if err != nil {
		return 0, false
	}
	var cache struct {
		Models []struct {
			Slug    string `json:"slug"`
			Window  int    `json:"context_window"`
			Percent int    `json:"effective_context_window_percent"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return 0, false
	}
	for _, m := range cache.Models {
		if m.Slug != model || model == "" {
			continue
		}
		if m.Window < 1000 || m.Window > 100_000_000 {
			return 0, false
		}
		percent := m.Percent
		if percent < 1 || percent > 100 {
			percent = 90
		}
		return m.Window / 100 * percent, true
	}
	return 0, false
}

// SessionEnvWindow is the shell lines that tell Claude Code the context window of the Codex models, which it
// would otherwise take to be a million tokens. Claude Code applies the variable only to a model it does not know,
// so a Claude model in the same shell keeps its own window.
func SessionEnvWindow(tokens int) []string {
	if tokens <= 0 {
		return nil
	}
	value := strconv.Itoa(tokens)
	return []string{
		"export " + ContextWindowVar + "='" + value + "'",
		"export " + ContextWindowMarker + "='" + value + "'",
	}
}

// CodexSmallestWindow is the smallest window among the models in Codex's list: the one that is safe to tell a
// session that may be moved to any of them. ok is false when the list is not there or holds no usable window.
func CodexSmallestWindow(cachePath string) (tokens int, ok bool) {
	data, err := os.ReadFile(cachePath) // #nosec G304 -- Codex's own list of its models, in the owner's home
	if err != nil {
		return 0, false
	}
	var cache struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return 0, false
	}
	for _, m := range cache.Models {
		if window, known := CodexContextWindow(cachePath, m.Slug); known && (!ok || window < tokens) {
			tokens, ok = window, true
		}
	}
	return tokens, ok
}

// CodexModelsCache is where Codex keeps the list of its models: under CODEX_HOME when that is set, as Codex
// itself reads it, and under ~/.codex otherwise.
func CodexModelsCache(home string, getenv func(string) string) string {
	if dir := getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "models_cache.json")
	}
	return filepath.Join(home, ".codex", "models_cache.json")
}

// WithCodexWindows tells the handler how to find a Codex model's context window, so the size it tells a session
// can be weighed against the model that answers.
func WithCodexWindows(window func(model string) int) HandlerOption {
	return func(c *handlerConfig) { c.codexWindow = window }
}
