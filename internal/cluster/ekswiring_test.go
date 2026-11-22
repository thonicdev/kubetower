package cluster

import (
	"os"
	"strings"
	"testing"
)

// The refusal diagnosis is wired, and not merely written.
//
// This is the same seam `internal/health/identitywiring_test.go` guards and the
// same argument: `internal/ekstoken` can prove that a 401 provokes exactly one
// STS question and that the answer never carries a credential, and every one of
// those tests stays green if this file simply never calls `OnRefusal`. The whole
// mechanism would then be dead code that passes its own suite: built, tested,
// and not reachable. Grepping for callers finds unreachable Go and not a wire
// nobody connected.
//
// Reading the source is what is available: reaching the real branch needs an EKS
// kubeconfig, an AWS profile and a cluster that refuses. It cannot see that the
// sentence is right; it can see that the two ends are joined.
func TestTheEKSRefusalDiagnosisIsWired(t *testing.T) {
	source, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("reading manager.go: %v", err)
	}
	text := string(source)

	// The log line: the reader that needs no browser and no session. It is the
	// only one available where a launch has no window at all, a macOS bundle
	// for one — so it is the one asserted.
	if !strings.Contains(text, "minter.OnRefusal(") {
		t.Error("nothing subscribes to a token refusal, so a 401 from EKS goes back to being " +
			"a word with no identity behind it in the log")
	}
	// The health page's half. A row that names a profile and not an ARN is the
	// state to avoid: two machines whose one profile resolves to two
	// different roles render identically.
	if !strings.Contains(text, "Identity: minter.KnownIdentity") {
		t.Error("EKSSigning.Identity is not filled in, so the health row falls back to the " +
			"profile name for the life of the process")
	}
	// And the non-asking half is the one that must be there: KnownIdentity is
	// read by a page render, and `Identity` in its place would put an STS round
	// trip inside a template.
	if strings.Contains(text, "Identity: minter.Identity") {
		t.Error("EKSSigning.Identity is wired to the asking half of the pair, which puts a " +
			"network call inside a health row")
	}
}
