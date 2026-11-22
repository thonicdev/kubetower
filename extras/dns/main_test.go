package dns

import (
	"testing"

	"go.uber.org/goleak"
)

// Nothing this extra does outlives a request: no goroutine, no cache. A test
// that leaves one behind fails here.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
