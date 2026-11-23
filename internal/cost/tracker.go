package cost

import "strings"

// pricingLastVerified records when the pricing data was last checked against
// provider pricing pages. Update this date whenever you verify pricing.
// Claude: https://platform.claude.com/docs/en/about-claude/pricing
// OpenAI: https://openai.com/api/pricing/
const pricingLastVerified = "2026-11-23"

// ModelPricing holds per-million-token pricing in USD.
type ModelPricing struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// pricing stores per-million-token costs keyed by canonical model name.
var pricing = map[string]ModelPricing{
	// Anthropic Claude models (verified 2026-11-23)
	"claude-fable-5-2": {
		Input:      8.00,
		Output:     40.00,
		CacheRead:  0.20,
		CacheWrite: 10.00,
	},
	"claude-opus-5-5": {
		Input:      4.00,
		Output:     20.00,
		CacheRead:  0.20,
		CacheWrite: 5.00,
	},
	"claude-opus-5": {
		Input:      5.00,
		Output:     25.00,
		CacheRead:  0.50,
		CacheWrite: 6.25,
	},
	"claude-sonnet-5-5": {
		Input:      2.00,
		Output:     10.00,
		CacheRead:  0.10,
		CacheWrite: 2.50,
	},
	"claude-sonnet-5": {
		Input:      2.00,
		Output:     10.00,
		CacheRead:  0.20,
		CacheWrite: 2.50,
	},
	// Claude Haiku 5.5 is priced by prompt length: a prompt over 100,000 tokens costs five times these
	// rates. This table prices one model one way, so it holds the common case and under-reports the rest.
	"claude-haiku-5-5": {
		Input:      0.10,
		Output:     0.50,
		CacheRead:  0.01,
		CacheWrite: 0.125,
	},
	"claude-opus-4-7": {
		Input:      5.00,
		Output:     25.00,
		CacheRead:  0.50,
		CacheWrite: 6.25,
	},
	"claude-opus-4-6": {
		Input:      5.00,
		Output:     25.00,
		CacheRead:  0.50,
		CacheWrite: 6.25,
	},
	"claude-sonnet-4-6": {
		Input:      3.00,
		Output:     15.00,
		CacheRead:  0.30,
		CacheWrite: 3.75,
	},
	"claude-sonnet-4-5": {
		Input:      3.00,
		Output:     15.00,
		CacheRead:  0.30,
		CacheWrite: 3.75,
	},
	"claude-haiku-4-5": {
		Input:      1.00,
		Output:     5.00,
		CacheRead:  0.10,
		CacheWrite: 1.25,
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
}

// aliases maps short names to canonical model names.
var aliases = map[string]string{
	"opus":   "claude-opus-5-5",
	"sonnet": "claude-sonnet-5-5",
	"haiku":  "claude-haiku-5-5",
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
