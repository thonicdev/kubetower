package launch

import "testing"

// What's new (#80) compares the bare release; a development build has none.
func TestTheReleaseIsTheStampedVersionOnly(t *testing.T) {
	was := version
	t.Cleanup(func() { version = was })
	version = devVersion
	if got := releaseOf(); got != "" {
		t.Fatalf("a development build answers %q", got)
	}
	version = "0.2.0"
	if got := releaseOf(); got != "0.2.0" {
		t.Fatalf("%q, want 0.2.0", got)
	}
	// A stamp the seen store would refuse is no release to compare.
	version = "0.2.0 rc"
	if got := releaseOf(); got != "" {
		t.Fatalf("an unkeepable stamp answers %q", got)
	}
}
