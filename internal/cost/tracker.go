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
	// Claude Haiku 5.5 is priced by prompt length; these are the rates for a prompt up to 100,000 tokens.
	// CalculateRequest applies the higher rates in longPromptPricing to a single request above that.
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

// longPromptPricing holds the rates a model charges for one request whose prompt passes its threshold.
// Claude Haiku 5.5 is the only model priced this way today: a prompt over 100,000 tokens, counting input,
// cache reads and cache writes together, is billed at five times the standard rates.
var longPromptPricing = map[string]struct {
	Threshold int64
	Pricing   ModelPricing
}{
	"claude-haiku-5-5": {Threshold: 100_000, Pricing: ModelPricing{
		Input:      0.50,
		Output:     2.50,
		CacheRead:  0.05,
		CacheWrite: 0.625,
	}},
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
// CalculateRequest prices one request, applying any rate a model charges for a long prompt. Use it where the
// token counts belong to a single request; Calculate prices token counts that may be a session's total, where
// a prompt-length threshold would not apply.
func CalculateRequest(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) float64 {
	canonical := normalizeModel(model)
	if long, ok := longPromptPricing[canonical]; ok {
		// The threshold counts every input token of the request, cache reads and writes included.
		if inputTokens+cacheReadTokens+cacheWriteTokens > long.Threshold {
			return price(long.Pricing, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
		}
	}
	return Calculate(model, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
}

// Calculate prices token counts at a model's standard rates. The counts may be one request's or a whole
// session's, so it never applies a long-prompt rate: a session of short requests must not be billed as one
// long one. For a single request, use CalculateRequest.
func Calculate(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) float64 {
	canonical := normalizeModel(model)
	p, ok := pricing[canonical]
	if !ok {
		return 0
	}
	return price(p, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
}

func price(p ModelPricing, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) float64 {
	const perMillion = 1_000_000.0
	cost := float64(inputTokens) / perMillion * p.Input
	cost += float64(outputTokens) / perMillion * p.Output
	cost += float64(cacheReadTokens) / perMillion * p.CacheRead
	cost += float64(cacheWriteTokens) / perMillion * p.CacheWrite
	return cost
}
