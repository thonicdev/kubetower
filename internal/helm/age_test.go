package helm

import (
	"testing"
	"time"
)

// The release carries its age in the server's wording, beside the instant.
//
// With only `updatedAt`, a raw instant, the browser would spell the age itself —
// once, at render, clamped to "0s" on a laptop whose clock is behind the
// cluster's, and on a scale of its own. `EventEntry.age` exists *"so that two
// panels drawing the same event cannot
// spell it two ways"*; this is the same member on a release, aged from the
// report's own collection instant like every other age this server writes.
func TestAReleaseCarriesItsAgeInTheServersWording(t *testing.T) {
	now := time.Now()
	reader, _ := readerOver(
		revisionOf(t, "argocd", "argocd", 41, StatusDeployed, "10.4.2", "v3.5.2",
			"Upgrade complete", now.Add(-72*time.Hour)),
		revisionOf(t, "redis", "cache", 3, StatusDeployed, "19.0.1", "7.2.4",
			"Install complete", now.Add(-4*time.Minute)),
	)

	report := reader.Releases(t.Context(), "test", nil)
	if report.State != StatePresent {
		t.Fatalf("state %v, detail %q", report.State, report.Detail)
	}
	byName := map[string]Release{}
	for _, r := range report.Releases {
		byName[r.Name] = r
	}
	if got := byName["argocd"].Age; got != "3d" {
		t.Errorf("argocd: Age = %q, want %q — the server's scale, which stops at days", got, "3d")
	}
	if got := byName["redis"].Age; got != "4m" {
		t.Errorf("redis: Age = %q, want %q", got, "4m")
	}

	// The release's own page ages the same release the same way: one fold, one
	// clock, so the list and the page cannot disagree about a release.
	history := reader.History(t.Context(), "test", "argocd", "argocd")
	if history.State != StatePresent {
		t.Fatalf("history state %v, detail %q", history.State, history.Detail)
	}
	if history.Release.Age != "3d" {
		t.Errorf("the release page ages argocd %q where the list says %q", history.Release.Age, "3d")
	}
}

// A release Helm recorded no time for has no age either — not "0s", which
// would say it was deployed this second.
func TestAReleaseWithNoRecordedTimeHasNoAge(t *testing.T) {
	// The empty string is the spelling the live document carried and the
	// decoder accepts as the zero time (TestAnEmptyTimestampDoesNotFailTheWholeRelease);
	// helmish's own zero-time path writes the word "null" in quotes, which is
	// not a time at all.
	doc := helmish("argocd", "argocd", 41, StatusDeployed, "argo-cd", "10.4.2", "v3.5.2",
		"Upgrade complete", time.Now())
	doc["info"].(map[string]any)["last_deployed"] = ""
	reader, _ := readerOver(releaseSecret(t, doc, encode(t, doc)))
	report := reader.Releases(t.Context(), "test", nil)
	if len(report.Releases) != 1 {
		t.Fatalf("%d rows, detail %q", len(report.Releases), report.Detail)
	}
	got := report.Releases[0]
	if !got.Updated.IsZero() {
		t.Fatalf("Updated = %v on a release with last_deployed null", got.Updated)
	}
	if got.Age != "" {
		t.Errorf("Age = %q on a release with no recorded time; the member must be empty, "+
			"and a client draws the em dash and says why", got.Age)
	}
}
