package jvm

import (
	"testing"

	"go.uber.org/goleak"
)

// No goroutine this package starts outlives the tests that started it: the
// inventory's metrics read is always waited for, answered or abandoned.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
