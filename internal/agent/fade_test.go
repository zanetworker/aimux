package agent

import (
	"strings"
	"testing"
	"time"
)

func TestFadeHex_ZeroElapsed(t *testing.T) {
	got := FadeHex("#9CA3AF", "#374151", 0, time.Hour)
	if got != "#9CA3AF" {
		t.Errorf("FadeHex at 0 elapsed = %q, want %q", got, "#9CA3AF")
	}
}

func TestFadeHex_FullyElapsed(t *testing.T) {
	got := FadeHex("#9CA3AF", "#374151", time.Hour, time.Hour)
	if got != "#374151" {
		t.Errorf("FadeHex at full duration = %q, want %q", got, "#374151")
	}
}

func TestFadeHex_OverElapsed(t *testing.T) {
	got := FadeHex("#9CA3AF", "#374151", 2*time.Hour, time.Hour)
	if got != "#374151" {
		t.Errorf("FadeHex over duration = %q, want %q", got, "#374151")
	}
}

func TestFadeHex_NegativeElapsed(t *testing.T) {
	got := FadeHex("#9CA3AF", "#374151", -5*time.Minute, time.Hour)
	if got != "#9CA3AF" {
		t.Errorf("FadeHex with negative elapsed = %q, want %q", got, "#9CA3AF")
	}
}

func TestFadeHex_Midpoint(t *testing.T) {
	got := FadeHex("#9CA3AF", "#374151", 30*time.Minute, time.Hour)
	if len(got) != 7 {
		t.Errorf("FadeHex midpoint length = %d, want 7", len(got))
	}
	if !strings.HasPrefix(got, "#") {
		t.Errorf("FadeHex midpoint = %q, want '#' prefix", got)
	}
	// The midpoint should be between start and end, not equal to either.
	if got == "#9CA3AF" || got == "#374151" {
		t.Errorf("FadeHex midpoint = %q, should be between start and end", got)
	}
}

func TestFadeHex_IdenticalColors(t *testing.T) {
	got := FadeHex("#AABBCC", "#AABBCC", 30*time.Minute, time.Hour)
	if got != "#AABBCC" {
		t.Errorf("FadeHex with identical colors = %q, want %q", got, "#AABBCC")
	}
}

func TestFadeHex_BlackToWhite(t *testing.T) {
	start := FadeHex("#000000", "#FFFFFF", 0, time.Hour)
	end := FadeHex("#000000", "#FFFFFF", time.Hour, time.Hour)
	if start != "#000000" {
		t.Errorf("FadeHex black->white at 0 = %q, want %q", start, "#000000")
	}
	if end != "#FFFFFF" {
		t.Errorf("FadeHex black->white at 1h = %q, want %q", end, "#FFFFFF")
	}
}

func TestStatusFade_Active(t *testing.T) {
	got := StatusFadeColor(StatusActive, time.Now())
	if got != "" {
		t.Errorf("StatusFadeColor(Active) = %q, want empty string", got)
	}
}

func TestStatusFade_WaitingPermission(t *testing.T) {
	got := StatusFadeColor(StatusWaitingPermission, time.Now())
	if got != "" {
		t.Errorf("StatusFadeColor(WaitingPermission) = %q, want empty string", got)
	}
}

func TestStatusFade_Error(t *testing.T) {
	got := StatusFadeColor(StatusError, time.Now())
	if got != "" {
		t.Errorf("StatusFadeColor(Error) = %q, want empty string", got)
	}
}

func TestStatusFade_Unknown(t *testing.T) {
	got := StatusFadeColor(StatusUnknown, time.Now())
	if got != "" {
		t.Errorf("StatusFadeColor(Unknown) = %q, want empty string", got)
	}
}

func TestStatusFade_Idle_Fresh(t *testing.T) {
	got := StatusFadeColor(StatusIdle, time.Now())
	if got != "#9CA3AF" {
		t.Errorf("StatusFadeColor(Idle, now) = %q, want %q", got, "#9CA3AF")
	}
}

func TestStatusFade_Idle_Stale(t *testing.T) {
	got := StatusFadeColor(StatusIdle, time.Now().Add(-2*time.Hour))
	if got != "#374151" {
		t.Errorf("StatusFadeColor(Idle, 2h ago) = %q, want %q", got, "#374151")
	}
}

func TestStatusFade_Idle_ZeroTime(t *testing.T) {
	got := StatusFadeColor(StatusIdle, time.Time{})
	if got != "#374151" {
		t.Errorf("StatusFadeColor(Idle, zero time) = %q, want %q", got, "#374151")
	}
}

func TestStatusFade_Idle_HalfHour(t *testing.T) {
	got := StatusFadeColor(StatusIdle, time.Now().Add(-30*time.Minute))
	if len(got) != 7 || !strings.HasPrefix(got, "#") {
		t.Errorf("StatusFadeColor(Idle, 30m ago) = %q, want valid hex color", got)
	}
	if got == "#9CA3AF" || got == "#374151" {
		t.Errorf("StatusFadeColor(Idle, 30m ago) = %q, should be intermediate", got)
	}
}

func TestParseHex_Valid(t *testing.T) {
	r, g, b := parseHex("#FF8000")
	if r != 255 || g != 128 || b != 0 {
		t.Errorf("parseHex(#FF8000) = (%d, %d, %d), want (255, 128, 0)", r, g, b)
	}
}

func TestParseHex_NoHash(t *testing.T) {
	r, g, b := parseHex("FF8000")
	if r != 255 || g != 128 || b != 0 {
		t.Errorf("parseHex(FF8000) = (%d, %d, %d), want (255, 128, 0)", r, g, b)
	}
}

func TestParseHex_Malformed(t *testing.T) {
	r, g, b := parseHex("bad")
	if r != 0 || g != 0 || b != 0 {
		t.Errorf("parseHex(bad) = (%d, %d, %d), want (0, 0, 0)", r, g, b)
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{-10, 0},
		{0, 0},
		{128, 128},
		{255, 255},
		{300, 255},
	}
	for _, tt := range tests {
		if got := clamp(tt.in); got != tt.want {
			t.Errorf("clamp(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
