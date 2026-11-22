package textutil

import (
	"strings"
	"testing"
)

// The shape of a real token-signing failure behind a long EKS address. The
// ending is one plausible cause (an expired SSO session), chosen because it
// exercises the clip.
const eksTokenFailure = `Get "https://0123456789ABCDEF0123456789ABCDEF.gr7.eu-west-3.eks.amazonaws.com/apis/metrics.k8s.io/v1beta1?timeout=30s": signing a token for "example-prod-eks-1": operation error STS: GetCallerIdentity, get identity: get credentials: failed to refresh cached credentials, the SSO session has expired or is invalid`

func TestTheDestinationGivesWayAndTheReasonSurvives(t *testing.T) {
	got := ReasonN(eksTokenFailure, 200)

	if len([]rune(got)) > 200 {
		t.Fatalf("%d runes, over the budget: %q", len([]rune(got)), got)
	}
	// What the reader came for.
	if !strings.Contains(got, "SSO session has expired") {
		t.Errorf("the cause did not survive the clip, which is the whole point: %q", got)
	}
	// And what they already knew, which is what paid for it.
	if strings.Contains(got, "eks.amazonaws.com") {
		t.Errorf("the address is still spending the budget: %q", got)
	}
	// The seam left behind when a quoted URL is cut out of the middle. The URL
	// pattern stops at the closing quote, so a bare strip leaves `Get "":` — an
	// empty pair of quotes and a verb with no object.
	if strings.HasPrefix(got, "Get") || strings.Contains(got, `""`) || strings.Contains(got, "  ") {
		t.Errorf("the strip left a message that reads as its own truncation: %q", got)
	}
	// It begins at the reason, which is what a reader's eye lands on first.
	if !strings.HasPrefix(got, "signing a token") {
		t.Errorf("the line does not open on the cause: %q", got)
	}
}

// The address is kept whenever it fits, because it is the useful half of a name
// that does not resolve or a connection that was refused.
func TestAnAddressThatFitsIsKept(t *testing.T) {
	short := `Get "https://10.0.0.1:6443/version": dial tcp 10.0.0.1:6443: connect: connection refused`

	got := ReasonN(short, 200)
	if got != short {
		t.Errorf("a message that fits was rewritten:\n got  %q\n want %q", got, short)
	}
}

// A message that is nothing but an address still has to say something: the
// address is then the message, and returning empty would be a row with no
// content at all.
func TestAMessageThatIsOnlyAnAddressKeepsIt(t *testing.T) {
	only := "https://" + strings.Repeat("a", 400) + ".example.com/very/long/path"

	got := ReasonN(only, 60)
	if got == "" {
		t.Fatal("a message that is only an address came back empty")
	}
	if len([]rune(got)) > 60 {
		t.Fatalf("%d runes, over the budget: %q", len([]rune(got)), got)
	}
}

// It is still FirstLine's job first: a client-go error runs to several lines and
// the first carries the cause.
func TestReasonKeepsOnlyTheFirstLine(t *testing.T) {
	got := ReasonN("denied by webhook\nsecond line", 200)
	if strings.Contains(got, "second line") {
		t.Errorf("a second line reached a single-line budget: %q", got)
	}
}

// The frame is removed as a prefix and never as a substring.
//
// A message can contain something shaped like a client-go frame in the middle of
// it — a webhook quoting the request it refused, an operator's own error text —
// and that is the reader's data, not our leftover. The comment on clientGoFrame
// claims the anchor; without this, unanchoring it stays green, which is a
// comment doing a test's job.
func TestTheFrameIsOnlyEverRemovedFromTheFront(t *testing.T) {
	long := "admission webhook denied the request: the payload said " +
		`Get "": nothing and that phrase is the operator's own text, ` +
		strings.Repeat("padding so that this message cannot possibly fit. ", 6)

	got := ReasonN(long, 200)

	if !strings.HasPrefix(got, "admission webhook denied") {
		t.Fatalf("the front of the message was rewritten: %q", got)
	}
	if !strings.Contains(got, `Get "": nothing`) {
		t.Errorf("a frame-shaped phrase inside the reader's own text was eaten: %q", got)
	}
}
