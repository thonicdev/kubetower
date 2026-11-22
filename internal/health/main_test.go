package health

import (
	"testing"

	"go.uber.org/goleak"
)

// No goroutine this package starts outlives the tests that started it: the
// probes, the asks behind them, the background rounds and the kubeconfig
// reads all end once what they wait on has answered or been released.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
