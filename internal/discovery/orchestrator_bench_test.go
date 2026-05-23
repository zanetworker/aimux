package discovery

import "testing"

func BenchmarkDiscover_NoProviders(b *testing.B) {
	o := NewOrchestrator()
	for i := 0; i < b.N; i++ {
		_, _ = o.Discover()
	}
}
