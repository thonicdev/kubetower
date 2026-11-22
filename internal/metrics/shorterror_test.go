package metrics

import (
	"errors"
	"strings"
	"testing"
)

// The metrics package's own errors reach a log, and the log is the one artifact
// somebody on another machine can send.
//
// Cut at the first line, a discovery line comes out as `example-nonprod:
// unreachable (Get "https://0123….eks.amazonaws.com/apis/metrics.k8s.io/
// v1beta1?timeout=30s": signing a token for "example-prod-eks-1": operation
// error STS: GetCallerIdentity, get i…)` — 199 runes to the cut and **120 of
// them the address**. The reader already knows which cluster the line is about;
// it is titled with it. What they cannot recover is why.
//
// This asserts the property at the site rather than only in textutil, because
// a mutation putting `FirstLineN` back here would survive every textutil test.
func TestADiscoveryFailureSpendsItsBudgetOnTheReason(t *testing.T) {
	err := errors.New(`Get "https://0123456789ABCDEF0123456789ABCDEF.gr7.eu-west-3.eks.amazonaws.com/apis/metrics.k8s.io/v1beta1?timeout=30s": ` +
		`signing a token for "example-prod-eks-1": operation error STS: GetCallerIdentity, get identity: ` +
		`get credentials: failed to refresh cached credentials, the SSO session has expired or is invalid`)

	got := shortError(err)

	if len([]rune(got)) > detailLine {
		t.Fatalf("%d runes, over this package's own budget: %q", len([]rune(got)), got)
	}
	if !strings.Contains(got, "SSO session has expired") {
		t.Errorf("the cause did not survive, so the log names a failure and not its reason: %q", got)
	}
	if strings.Contains(got, "eks.amazonaws.com") {
		t.Errorf("the address is still spending the budget: %q", got)
	}
}

// And an error short enough to keep its address keeps it: on a name that does
// not resolve, the address is the useful half.
func TestAShortFailureKeepsItsAddress(t *testing.T) {
	err := errors.New(`Get "https://10.0.0.1:6443/version": dial tcp 10.0.0.1:6443: connect: connection refused`)

	if got := shortError(err); !strings.Contains(got, "10.0.0.1:6443") {
		t.Errorf("an address that fits was thrown away: %q", got)
	}
}
