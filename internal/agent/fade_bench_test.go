package agent

import (
	"testing"
	"time"
)

func BenchmarkFadeHex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		FadeHex("#9CA3AF", "#374151", 30*time.Minute, 1*time.Hour)
	}
}

func BenchmarkStatusFadeColor_Idle(b *testing.B) {
	activity := time.Now().Add(-30 * time.Minute)
	for i := 0; i < b.N; i++ {
		StatusFadeColor(StatusIdle, activity)
	}
}

func BenchmarkStatusFadeColor_Active(b *testing.B) {
	for i := 0; i < b.N; i++ {
		StatusFadeColor(StatusActive, time.Time{})
	}
}
