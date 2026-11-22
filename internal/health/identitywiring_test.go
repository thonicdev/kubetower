package health

import (
	"os"
	"strings"
	"testing"
)

// The wiring, and not just the helper.
//
// `withIdentity` asserted directly leaves **deleting its call site** green: a
// test handed a value can only say what the value produces, never that the
// layer above produced it — and the seam here is awkward: `probe` needs a live
// `*cluster.Manager` and a cluster to fail against.
//
// So this reads the source, which is what `TestTheSettingsPageIsHandedTheSameLine
// TheFlagPrints` does for the same reason. It cannot see that the note is
// correct; it can see that the only path from a failed probe to a row goes
// through the thing that adds it.
func TestTheFailedProbeGoesThroughTheIdentityNote(t *testing.T) {
	source, err := os.ReadFile("health.go")
	if err != nil {
		t.Fatalf("reading health.go: %v", err)
	}
	text := string(source)

	body := func(name string) string {
		start := strings.Index(text, "func (h *Health) "+name+"(")
		if start < 0 {
			t.Fatalf("%s is gone", name)
		}
		end := strings.Index(text[start:], "\n}\n")
		if end < 0 {
			t.Fatalf("%s is unterminated", name)
		}
		return text[start : start+end]
	}

	// Both returns of a classified failure. The `select` arm is the one that
	// carries an API server's refusal, which is the whole subject; the early one
	// is a client that could not be built, where there is no identity to name.
	// That arm hands the answer to `answered`, so the path has two hops: probe
	// to answered, and answered to withIdentity.
	if probe := body("probe"); !strings.Contains(probe, "h.answered(ctx, a, client)") {
		t.Errorf("probe's answer arm no longer goes through answered:\n%s", probe)
	}
	if answered := body("answered"); !strings.Contains(answered, "withIdentity(heard(ctx, classified(a.err), execOf(client)), client)") {
		t.Errorf("a failed probe no longer passes through withIdentity, so the row goes out "+
			"without saying which identity it authenticated as:\n%s", answered)
	}
}
