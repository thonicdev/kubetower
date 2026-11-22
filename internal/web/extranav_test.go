package web

import (
	"strings"
	"testing"
)

// **An enabled extra's menu entry opens its page, not its API.**
//
// The page is the SPA's route at `/c/{cluster}/x/{key}`; `/x/{cluster}/{key}/`
// is where `mountExtensions` puts the extension's HTTP routes. The two differ by
// the order of two segments, and an entry pointing at the second would not
// error, because both addresses answer something.
//
// The cluster names are the three shapes the segment has to survive: a plain
// one, one with a slash and a space (as an EKS ARN or a hand-named context
// carries) - which must be escaped as every other `/c/{cluster}` link is - and one only the
// escape can save, where an unescaped `#` or `?` would end the path and send the
// operator to the cluster list with the rest of the name in a fragment.
func TestAnEnabledExtraLinksToItsPageUnderTheCluster(t *testing.T) {
	cases := map[string]string{
		"mi-k3s": "/c/mi-k3s/x/" + fixtureKey,
		"arn:aws:eks:eu-west-1:1:cluster/prod one": "/c/arn:aws:eks:eu-west-1:1:cluster%2Fprod%20one/x/" + fixtureKey,
		"team#1?prod%": "/c/team%231%3Fprod%25/x/" + fixtureKey,
	}
	for cluster, want := range cases {
		group := extraNav(cluster, "", func(key string) bool { return key == fixtureKey })

		if len(group.Links) != 1 {
			t.Fatalf("%q: %d links under %q, want exactly the one enabled extra: %+v",
				cluster, len(group.Links), extraNavLabel, group.Links)
		}
		got := group.Links[0].Href
		if got != want {
			t.Errorf("%q: the entry links to %q, want %q", cluster, got, want)
		}
		if strings.HasPrefix(got, "/x/") {
			t.Errorf("%q: %q is the extension's route mount, not its page", cluster, got)
		}
		// The same answer as the function every caller goes through, so a
		// second spelling of the address cannot appear beside this one.
		if helper := extraPageURL(cluster, fixtureKey); helper != got {
			t.Errorf("%q: extraNav built %q and extraPageURL builds %q", cluster, got, helper)
		}
	}
}
