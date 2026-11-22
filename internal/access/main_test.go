package access

import (
	"testing"

	"go.uber.org/goleak"
)

// No goroutine this package starts - a review of a batch - outlives the
// tests that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
