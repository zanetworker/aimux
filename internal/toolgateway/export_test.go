package toolgateway

import (
	"testing"
	"time"
)

// SetDownLogEvery sets how often a down backend's failed retry is logged,
// restoring it when t ends.
func SetDownLogEvery(t *testing.T, d time.Duration) {
	t.Helper()
	old := downLogEvery.Load()
	downLogEvery.Store(int64(d))
	t.Cleanup(func() { downLogEvery.Store(old) })
}
