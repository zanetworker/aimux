package cost

import "strings"

// pricingLastVerified records when the pricing data was last checked against
// provider pricing pages. Update this date whenever you verify pricing.
// Claude: https://docs.anthropic.com/en/docs/about-claude/models
// OpenAI: https://openai.com/api/pricing/
const pricingLastVerified = "2026-02-28"

// ModelPricing holds per-million-token pricing in USD.
type ModelPricing struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// pricing stores per-million-token costs keyed by canonical model name.
var pricing = map[string]ModelPricing{
	// Anthropic Claude models
	"claude-opus-4-6": {
		Input:      15.00,
		Output:     75.00,
		CacheRead:  1.50,
		CacheWrite: 18.75,
	},
	"claude-sonnet-4-5": {
		Input:      3.00,
		Output:     15.00,
		CacheRead:  0.30,
		CacheWrite: 3.75,
	},
	"claude-haiku-3-5": {
		Input:      0.80,
		Output:     4.00,
		CacheRead:  0.08,
		CacheWrite: 1.00,
	},
	// OpenAI models (used by Codex CLI)
	"o3": {
		Input:  2.00,
		Output: 8.00,
	},
	"o4-mini": {
		Input:  1.10,
		Output: 4.40,
	},
	"gpt-5.3-codex": {
		Input:  2.00,
		Output: 8.00,
	},
	// Google Gemini models
	"gemini-2.5-pro": {
		Input:  1.25,
		Output: 10.00,
	},
	"gemini-2.5-flash": {
		Input:  0.15,
		Output: 0.60,
	},
	"gemini-3-pro": {
		Input:  1.25,
		Output: 10.00,
	},
	"gemini-3.1-flash": {
		Input:  0.15,
		Output: 0.60,
	},
}

// aliases maps short names to canonical model names.
var aliases = map[string]string{
	"opus":   "claude-opus-4-6",
	"sonnet": "claude-sonnet-4-5",
	"haiku":  "claude-haiku-3-5",
}

// normalizeModel strips version/context suffixes (e.g. [1m], @20250929)
// and resolves short aliases to canonical model names.
func normalizeModel(m string) string {
	// Strip [suffix] like [1m]
	if idx := strings.Index(m, "["); idx != -1 {
		m = m[:idx]
	}
	// Strip @suffix like @20250929
	if idx := strings.Index(m, "@"); idx != -1 {
		m = m[:idx]
	}
	m = strings.TrimSpace(m)
	m = strings.ToLower(m)

	if canonical, ok := aliases[m]; ok {
		return canonical
	}
	return m
}

// Calculate returns the estimated cost in USD for the given token usage.
// Returns 0 if the model is unknown.
func Calculate(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) float64 {
	canonical := normalizeModel(model)
	p, ok := pricing[canonical]
	if !ok {
		return 0
	}

	const perMillion = 1_000_000.0
	cost := float64(inputTokens) / perMillion * p.Input
	cost += float64(outputTokens) / perMillion * p.Output
	cost += float64(cacheReadTokens) / perMillion * p.CacheRead
	cost += float64(cacheWriteTokens) / perMillion * p.CacheWrite
	return cost
}
