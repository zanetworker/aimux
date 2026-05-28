package cost

import (
	"math"
	"testing"
	"time"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}

func TestCalculateOpus(t *testing.T) {
	// 1M input * $5/M + 100K output * $25/M = $5.00 + $2.50 = $7.50
	got := Calculate("claude-opus-4-6", 1_000_000, 100_000, 0, 0)
	if !almostEqual(got, 7.50) {
		t.Errorf("Opus 4.6 1M input + 100K output: got $%.4f, want $7.50", got)
	}
}

func TestCalculateOpus47(t *testing.T) {
	// Same pricing as 4.6: 1M input * $5/M + 100K output * $25/M = $7.50
	got := Calculate("claude-opus-4-7", 1_000_000, 100_000, 0, 0)
	if !almostEqual(got, 7.50) {
		t.Errorf("Opus 4.7 1M input + 100K output: got $%.4f, want $7.50", got)
	}
}

func TestCalculateSonnet(t *testing.T) {
	// 1M input * $3/M + 100K output * $15/M = $3.00 + $1.50 = $4.50
	got := Calculate("claude-sonnet-4-6", 1_000_000, 100_000, 0, 0)
	if !almostEqual(got, 4.50) {
		t.Errorf("Sonnet 4.6 1M input + 100K output: got $%.4f, want $4.50", got)
	}
}

func TestCalculateSonnet45(t *testing.T) {
	got := Calculate("claude-sonnet-4-5", 1_000_000, 100_000, 0, 0)
	if !almostEqual(got, 4.50) {
		t.Errorf("Sonnet 4.5 1M input + 100K output: got $%.4f, want $4.50", got)
	}
}

func TestCalculateHaiku(t *testing.T) {
	// 1M input * $0.80/M + 100K output * $4/M = $0.80 + $0.40 = $1.20
	got := Calculate("claude-haiku-3-5", 1_000_000, 100_000, 0, 0)
	if !almostEqual(got, 1.20) {
		t.Errorf("Haiku 3.5 1M input + 100K output: got $%.4f, want $1.20", got)
	}
}

func TestCalculateHaiku45(t *testing.T) {
	// 1M input * $1/M + 100K output * $5/M = $1.00 + $0.50 = $1.50
	got := Calculate("claude-haiku-4-5", 1_000_000, 100_000, 0, 0)
	if !almostEqual(got, 1.50) {
		t.Errorf("Haiku 4.5 1M input + 100K output: got $%.4f, want $1.50", got)
	}
}

func TestCalculateZeroTokens(t *testing.T) {
	got := Calculate("claude-opus-4-6", 0, 0, 0, 0)
	if got != 0 {
		t.Errorf("Zero tokens: got $%.4f, want $0.00", got)
	}
}

func TestCalculateWithCache(t *testing.T) {
	// 500K input * $5/M + 50K output * $25/M + 200K cache_read * $0.50/M + 100K cache_write * $6.25/M
	// = $2.50 + $1.25 + $0.10 + $0.625 = $4.475
	got := Calculate("claude-opus-4-6", 500_000, 50_000, 200_000, 100_000)
	if !almostEqual(got, 4.475) {
		t.Errorf("Opus with cache: got $%.4f, want $4.475", got)
	}
}

func TestCalculateUnknownModel(t *testing.T) {
	got := Calculate("unknown-model", 1_000_000, 100_000, 0, 0)
	if got != 0 {
		t.Errorf("Unknown model: got $%.4f, want $0.00", got)
	}
}

func TestNormalizeModelSuffix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-opus-4-6[1m]", "claude-opus-4-6"},
		{"claude-sonnet-4-5@20250929", "claude-sonnet-4-5"},
		{"claude-haiku-3-5[1m]", "claude-haiku-3-5"},
		{"Claude-Opus-4-6", "claude-opus-4-6"},
	}
	for _, tt := range tests {
		got := normalizeModel(tt.input)
		if got != tt.want {
			t.Errorf("normalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeModelAliases(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"opus", "claude-opus-4-7"},
		{"sonnet", "claude-sonnet-4-6"},
		{"haiku", "claude-haiku-4-5"},
		{"Opus", "claude-opus-4-7"},
	}
	for _, tt := range tests {
		got := normalizeModel(tt.input)
		if got != tt.want {
			t.Errorf("normalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCalculateWithAlias(t *testing.T) {
	// "opus" alias -> claude-opus-4-7 -> $5/$25
	got := Calculate("opus", 1_000_000, 100_000, 0, 0)
	if !almostEqual(got, 7.50) {
		t.Errorf("Opus alias 1M input + 100K output: got $%.4f, want $7.50", got)
	}
}

func TestCalculateWithSuffixedModel(t *testing.T) {
	got := Calculate("claude-opus-4-6[1m]", 1_000_000, 100_000, 0, 0)
	if !almostEqual(got, 7.50) {
		t.Errorf("Opus with [1m] suffix: got $%.4f, want $7.50", got)
	}
}

func TestPricingNotStale(t *testing.T) {
	verified, err := time.Parse("2006-01-02", pricingLastVerified)
	if err != nil {
		t.Fatalf("pricingLastVerified %q is not a valid date: %v", pricingLastVerified, err)
	}
	age := time.Since(verified)
	if age > 90*24*time.Hour {
		t.Errorf("pricing data is %d days old (last verified: %s); "+
			"check provider pricing pages and update pricingLastVerified in tracker.go",
			int(age.Hours()/24), pricingLastVerified)
	}
}

func TestAllModelsHavePricing(t *testing.T) {
	for model, p := range pricing {
		if p.Input <= 0 {
			t.Errorf("model %q has zero/negative Input price", model)
		}
		if p.Output <= 0 {
			t.Errorf("model %q has zero/negative Output price", model)
		}
	}
}
