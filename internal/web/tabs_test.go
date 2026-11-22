package web

import (
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/detail"
)

// The keys are a member and only on the two kinds that have them, and no tab
// carries them.
//
// The keys are drawn inside the details panel, so what declares them is
// `DetailPage.Data` - an address, exactly as the charts are - and a `Tab` would
// be a second way to reach a section already on the page.
//
// What is still the thing most likely to be got wrong is asserted here:
// `HasData` compares against **Kinds**, and `detail.Object` fills `Kind` with
// the discovery plural and `Title` with the Kind (`detail.go:362-363`) - the two
// fields named the opposite way round from what a reader expects.
func TestTheKeysAreAMemberAndOnlyWhereThereIsADataBlock(t *testing.T) {
	for _, one := range []struct {
		kind string
		want bool
	}{
		{kind: "ConfigMap", want: true},
		{kind: "Secret", want: true},
		{kind: "Pod", want: false},
		{kind: "Node", want: false},
	} {
		ref := api.ObjectRef{Cluster: "prod", Kind: discoveryPlural(one.kind), Namespace: "argo", Name: "creds"}
		// **Filled the way `internal/detail` fills it**, which is the whole point
		// of this line: putting the Kind in `Kind`, because that is what the
		// name suggests, would pass and ship a server that declared nothing at
		// all for any Secret.
		page := detailPageOf(ref, detail.Object{Kind: discoveryPlural(one.kind), Title: one.kind})

		if (page.Data != "") != one.want {
			t.Errorf("%s: data address = %q, want one = %v", one.kind, page.Data, one.want)
		}
		if one.want {
			// The address is the operation with the object on it. A member that
			// carries a bare path is a member a client cannot use - and the kind
			// on it is the **plural**, which is what the route takes.
			for _, part := range []string{
				"/api/c/prod/data", "kind=" + discoveryPlural(one.kind), "namespace=argo", "name=creds",
			} {
				if !strings.Contains(page.Data, part) {
					t.Errorf("%s: data address %q carries no %q", one.kind, page.Data, part)
				}
			}
		}
		// The other half, and the half a reader of the diff would not think to
		// check: no tab alongside the member. Two ways to reach
		// one section is the state to avoid.
		for _, tab := range page.Tabs {
			if tab.Key == "data" {
				t.Errorf("%s: the keys are declared as a tab; they are drawn in the details panel", one.kind)
			}
		}
	}
}

// The plural is what a route carries and it is *not* what decides the member. A
// custom resource whose plural is `configmaps` in some other group is not the
// core ConfigMap, and parsing the plural back into a Kind here would be a second
// and weaker copy of discovery.
func TestTheKeysIgnoreThePluralInTheRoute(t *testing.T) {
	ref := api.ObjectRef{Cluster: "prod", Kind: "configmaps.example.com", Namespace: "argo", Name: "x"}
	page := detailPageOf(ref, detail.Object{Kind: "configmaps.example.com", Title: "Widget"})
	if page.Data != "" {
		t.Errorf("a Widget was given a data address (%q) because its plural looked like a ConfigMap's", page.Data)
	}
}

// The charts are not a tab, and they are a pod's and a node's.
//
// **Two mistakes to avoid.** First, `apiReads.Metrics` reads a node's
// history for a node and a **pod's** for everything else, so a panel on a
// ConfigMap would ask for the pod history of an object that is not a pod and
// answer `absent` - a panel whose only possible answer is that the question
// was meaningless. Second, the metrics block is drawn directly above the
// fact table, not as a separate tab.
//
// So the address is a member of the page, and this asserts both halves at once -
// which kinds carry one, and that no tab carries it.
func TestTheChartsAreAMemberAndOnlyWhereTheyCanAnswer(t *testing.T) {
	for _, one := range []struct {
		kind string
		want bool
	}{
		{kind: "pods", want: true},
		{kind: "nodes", want: true},
		{kind: "configmaps", want: false},
		{kind: "deployments", want: false},
	} {
		ref := api.ObjectRef{Cluster: "prod", Kind: one.kind, Namespace: "argo", Name: "x"}
		page := detailPageOf(ref, detail.Object{Kind: one.kind, Title: "X"})

		if (page.Metrics != "") != one.want {
			t.Errorf("%s: metrics address = %q, want one = %v", one.kind, page.Metrics, one.want)
		}
		if one.want {
			// The address is the operation with the object on it. A member that
			// carries a bare path is a member a client cannot use.
			for _, part := range []string{"/api/c/prod/metrics", "kind=" + one.kind, "namespace=argo", "name=x"} {
				if !strings.Contains(page.Metrics, part) {
					t.Errorf("%s: metrics address %q carries no %q", one.kind, page.Metrics, part)
				}
			}
		}
		for _, tab := range page.Tabs {
			if tab.Key == "metrics" {
				t.Errorf("%s: the charts are declared as a tab; they are drawn in the details panel", one.kind)
			}
		}
	}
}

// Changes and Manifest stay on every object: the first is the field managers'
// record, which any object has, and the second is the document itself.
//
// `changes` is the operation's name, after what it returns - the archive it
// could be mistaken for is a different panel that no operation serves.
func TestChangesAndManifestAreAlwaysDeclared(t *testing.T) {
	tabs := tabsFor(api.ObjectRef{Cluster: "prod", Kind: "configmaps", Name: "creds"}, detail.Object{Kind: "configmaps", Title: "ConfigMap"})
	want := map[string]bool{"manifest": false, "changes": false, "events": false}
	// Events is the one inline tab: its rows ride with the object, so it carries
	// no address and must not - `Tab.endpoint` is documented as *absent means the
	// tab is inline*, and an endpoint here would be a second read for bytes that
	// are already in the payload.
	inline := map[string]bool{"events": true}
	for _, tab := range tabs {
		if _, ok := want[tab.Key]; ok {
			want[tab.Key] = true
		}
		if (tab.Endpoint == "") != inline[tab.Key] {
			t.Errorf("tab %q: endpoint=%q, inline=%v", tab.Key, tab.Endpoint, inline[tab.Key])
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("tab %q was not declared", key)
		}
	}
}

// discoveryPlural is this file's own toy pluralizer, used only to fill the field
// the way discovery fills it. It is not a rule the product relies on anywhere -
// and it is named the long way because `plural` is already taken in this package.
func discoveryPlural(kind string) string {
	return strings.ToLower(kind) + "s"
}
