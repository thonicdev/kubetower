package launch

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/terminal"
)

// An idle pod exec is bounded under the server profile, where it holds one of
// the sixteen slots every person shares, and not on the desktop, where it holds
// only its own person's.
func TestAnIdleExecIsBoundedUnderTheServerProfileOnly(t *testing.T) {
	if got := idleExec(true); got != terminal.IdleExecBound {
		t.Errorf("under --incluster an idle exec is bounded at %s, want %s", got, terminal.IdleExecBound)
	}
	if got := idleExec(false); got != 0 {
		t.Errorf("on the desktop an idle exec is bounded at %s, want no bound", got)
	}
}
