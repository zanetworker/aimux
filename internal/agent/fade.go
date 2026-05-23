package agent

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// idleFadeDuration is how long it takes for an idle agent's icon to
	// fade from bright grey to dark grey.
	idleFadeDuration = time.Hour

	// idleFadeStart is the bright-grey hex color at zero elapsed time.
	idleFadeStart = "#9CA3AF"

	// idleFadeEnd is the dark-grey hex color at or past idleFadeDuration.
	idleFadeEnd = "#374151"
)

// FadeHex interpolates between two hex colors (#RRGGBB) using a square-root
// curve for perceptual smoothness. elapsed/duration controls the progress:
// 0 returns startHex, >= duration returns endHex.
func FadeHex(startHex, endHex string, elapsed, duration time.Duration) string {
	if elapsed <= 0 {
		return startHex
	}
	if elapsed >= duration {
		return endHex
	}

	t := float64(elapsed) / float64(duration)
	t = math.Sqrt(t) // perceptual smoothing

	sr, sg, sb := parseHex(startHex)
	er, eg, eb := parseHex(endHex)

	r := clamp(sr + int(math.Round(float64(er-sr)*t)))
	g := clamp(sg + int(math.Round(float64(eg-sg)*t)))
	b := clamp(sb + int(math.Round(float64(eb-sb)*t)))

	return fmt.Sprintf("#%02X%02X%02X", r, g, b)
}

// StatusFadeColor returns a foreground hex color string for statuses that
// should fade over time. Currently only StatusIdle fades (bright grey to
// dark grey over 1 hour). Returns "" for statuses that don't fade.
func StatusFadeColor(s Status, lastActivity time.Time) string {
	if s != StatusIdle {
		return ""
	}
	if lastActivity.IsZero() {
		return idleFadeEnd
	}
	elapsed := time.Since(lastActivity)
	return FadeHex(idleFadeStart, idleFadeEnd, elapsed, idleFadeDuration)
}

// parseHex parses a "#RRGGBB" string into separate R, G, B components.
// Returns (0, 0, 0) if the string is malformed.
func parseHex(hex string) (int, int, int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0
	}
	var r, g, b int
	_, _ = fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

// clamp restricts v to the [0, 255] range.
func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
