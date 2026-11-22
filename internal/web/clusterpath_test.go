package web

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The cases both languages answer, read from the front end's own file.
//
// One cluster must be spelt one way in console addresses: React Router
// compares paths as strings, so a page reached one way would have no menu row
// marked current. There is one function in each
// language; this file and `clusterPath.test.tsx` hold each of them to the same
// table, which is what makes it one spelling rather than two that agree today.
//
// Read as a file, for `basename_test.go`'s reason: neither language can import
// the other's code, and a copy of the table here would assert that this package
// agrees with itself.
const clusterPathCases = "../../web/ui/src/clusters/clusterPath.cases.json"

type clusterPathCase struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func readClusterPathCases(t *testing.T) []clusterPathCase {
	t.Helper()
	raw, err := os.ReadFile(clusterPathCases)
	if err != nil {
		t.Fatalf("reading %s: %v", clusterPathCases, err)
	}
	var cases []clusterPathCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("decoding %s: %v", clusterPathCases, err)
	}
	// An empty table would pass every loop below, and say nothing.
	if len(cases) == 0 {
		t.Fatalf("%s holds no cases", clusterPathCases)
	}
	return cases
}

func TestClusterPathSpellsAClusterAsTheFrontEndDoes(t *testing.T) {
	for _, c := range readClusterPathCases(t) {
		if got := clusterPath(c.Name); got != c.Path {
			t.Errorf("clusterPath(%q) = %q, and the front end writes %q", c.Name, got, c.Path)
		}
	}
}

// The menu's own links, read through the API the front end reads, for a context
// carrying `@`, `:` and `/` at once: every item is under the shared spelling, and
// the Pods row is exactly the address the rail's remembered page and a node's
// Pods link write. `clusterPath.test.tsx` reads those two links off the rendered
// rail and node panel and holds them to the same table; it is the only
// front-end file that reads it.
func TestTheMenuSpellsAClusterAsTheRailDoes(t *testing.T) {
	const name = "team@arn:aws:eks:eu-west-1:1:cluster/prod"
	var want string
	for _, c := range readClusterPathCases(t) {
		if c.Name == name {
			want = c.Path
		}
	}
	if want == "" {
		t.Fatalf("%s has no case for %q; this test asserts against the shared table, not a copy", clusterPathCases, name)
	}

	server := navFixture(t, name)
	menu, fail := apiClusters{server}.Nav(context.Background(), name)
	if fail != nil {
		t.Fatalf("the menu failed: %+v", fail.Reason)
	}

	var pods string
	seen := 0
	for _, group := range menu.Groups {
		for _, item := range group.Items {
			seen++
			if item.Href != want && !strings.HasPrefix(item.Href, want+"/") {
				t.Errorf("%q links to %q, which is not under %q", item.Text, item.Href, want)
			}
			if path, _, _ := strings.Cut(item.Href, "?"); path == want+"/pods" {
				pods = item.Href
			}
		}
	}
	if seen == 0 {
		t.Fatal("the menu has no items, so nothing was compared")
	}
	if pods == "" {
		t.Fatalf("no menu item links to %q", want+"/pods")
	}
}

// Every builder of a console address, held to the shared table one by one.
//
// The menu test above walks only what `Nav` returns, so a builder that feeds
// something else could go back to another spelling with this package green:
// `detailURL` (a custom object's page) and `pagePath` (the deferred Custom
// Resources group, the overview's links) are such builders. Compared with the table's own path rather than
// with `clusterPath`, so a builder and `clusterPath` drifting together is caught
// too.
func TestEveryConsoleAddressBuilderSpellsTheClusterOneWay(t *testing.T) {
	for _, c := range readClusterPathCases(t) {
		builders := map[string]struct{ got, want string }{
			"pagePath":           {pagePath(c.Name, "pods"), c.Path + "/pods"},
			"pageURL":            {pageURL(c.Name, "pods", "ns", "", listOrder{}), c.Path + "/pods?namespace=ns"},
			"detailURL":          {detailURL(c.Name, "widgets.example.com", "ns", "w"), c.Path + "/widgets.example.com/ns/w"},
			"detailURL, cluster": {detailURL(c.Name, "widgets.example.com", "", "w"), c.Path + "/widgets.example.com/w"},
			"helmPageURL":        {helmPageURL(c.Name), c.Path + "/" + helmNavKey},
			"extraPageURL":       {extraPageURL(c.Name, fixtureKey), c.Path + "/x/" + fixtureKey},
			"overviewPageURL":    {overviewPageURL(c.Name), c.Path},
		}
		for builder, pair := range builders {
			if pair.got != pair.want {
				t.Errorf("%s(%q) = %q, want %q", builder, c.Name, pair.got, pair.want)
			}
		}
	}
}
