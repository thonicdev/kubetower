package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The menu's member set, in both directions, against `api/openapi.yaml` read at
// run time.
//
// # The defect this guards against
//
// `internal/web/apimount.go`'s `Nav` must map every group's full view model, not
// only `Items: refsOf(group.Links)`. Two of the other fields are the whole of the
// custom resource heading:
//
//   - `Lazy`, which says "this group's contents are fetched separately". Without
//     it the client has no reason to ask for them, so it never does.
//   - `Sub`, which is where every custom kind actually lives — grouped by API
//     group, one level down. The heading carries no `Links` of its own, so
//     `refsOf(group.Links)` on it is `[]` even once it *has* been filled.
//
// Dropping them, the console receives *Custom Resources* with zero items on every
// page, and the one condition that would fill it inline — being on a custom-kind
// page — is unreachable, because the menu never offers a link to one. A
// deadlock that looks exactly like a cluster with no CRDs.
// # What the response-conformance test can and cannot see
//
// `TestRealResponsesMatchTheSchemasInTheContract` validates real answers against
// the schemas, and **every optional member is satisfied by being absent**. Both
// halves are shown by deleting the producer and running the two tests against
// each other:
//
//   - `groups` is optional. Declared in the contract and filled by nothing, the
//     conformance test is **green** and this one fails. That is the whole class,
//     and it is the class a dropped menu member belongs to.
//   - `deferred` carries a `default`, which puts it in `required`, so the
//     conformance test does catch an answer that omits it — `getNav:
//     body.data.groups[0]: is missing the required member "deferred"`. It cannot
//     catch a `deferred` that is present and always false, which is what dropping
//     the field in the adapter produces; `internal/web`'s half is what sees that.
//
// `internal/api/listmembers_test.go` is the shape: a member the contract
// declares and nothing fills is dead code at run time. This is the same test on
// the menu.
//
// # Both directions, because the two failures are different
//
//   - a member the contract declares and no answer carries — `deferred` and
//     `groups`, and the next one somebody adds;
//   - a member an answer carries and the contract does not declare — how
//     a removed `Cluster.group` could survive.
//
// The expectation is read out of the YAML rather than restated here. A test
// listing the names it had just seen in `types.go` would agree with the struct by
// construction and fail on nothing.
//
// # The union spans both operations, and that is the point rather than a detail
//
// `getNav` cannot carry a filled group and `getNavGroup` cannot carry a deferred
// one: `deferred: true` means the items are not in that response. Measuring
// either alone would leave half the schema unaccounted for and would pass while
// the menu was empty.
//
// # What it does not check
//
// This drives the real handlers over `fakeClusters`, so it proves the contract
// types can carry every declared member and that the handlers emit what they are
// given. It cannot prove that `internal/web` fills them from the view model that
// holds them — that package imports this one.
// `TestTheDeferredHeadingCrossesIntoTheContractAsDeferred`,
// `TestTheNestedApiGroupsCrossIntoTheContract` and
// `TestTheDeferredHeadingIsFilledFromTheSameFunctionAndSaysWhyItIsShort` in
// `internal/web` are the other half, and neither half is sufficient alone.
func TestTheMenuCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	carried := map[string]bool{}
	refs := map[string]bool{}

	// The menu as a whole: every top-level group, including the deferred heading.
	menu := dataOf(t, mux, "/api/c/prod/nav")
	groups, ok := menu["groups"].([]any)
	if !ok || len(groups) == 0 {
		t.Fatalf("the menu carries no groups; there is nothing to measure")
	}
	for _, group := range groups {
		collectNavGroup(t, group, carried, refs)
	}

	// And the deferred heading, filled. Its own answer is one group rather than a
	// menu, and it is the only place `groups` and a filled `items` can appear.
	filled := dataOf(t, mux, "/api/c/prod/nav/custom-resources")
	collectNavGroup(t, filled, carried, refs)

	// An empty answer and a failed answer are different answers, and so is one
	// that never reached a handler. Without this the loops below would pass over
	// nothing and report success.
	if !carried["deferred"] || !carried["groups"] {
		t.Fatalf("neither answer carried a deferred heading or a nested group; "+
			"this test measures a menu that has both in it: %v", sorted(carried))
	}

	for _, subject := range []struct {
		schema  string
		members map[string]bool
	}{
		{"NavGroup", carried},
		{"Ref", refs},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if why, allowed := navMembersWithNoSource[subject.schema+"."+member]; allowed {
				t.Logf("%s.%s is declared and deliberately not carried: %s", subject.schema, member, why)
				continue
			}
			if !subject.members[member] {
				t.Errorf("the contract declares %s.%s and no menu group in either answer carries it; "+
					"either fill it from the producer that holds it, or record here why it has no source",
					subject.schema, member)
			}
		}
		for _, member := range sorted(subject.members) {
			if !declared[member] {
				t.Errorf("an answer carries %s.%s and the contract does not declare it; "+
					"this is how `Cluster.group` survived a contract that had removed it",
					subject.schema, member)
			}
		}
	}
}

// navMembersWithNoSource is the deliberate exceptions, each with the evidence
// that there is nothing to fill it from.
//
//	NavGroup.key       apimount.go's navKey, derived from the label
//	NavGroup.label     the descriptor's Group, or customNavLabel
//	NavGroup.items     refsOf(group.Links)
//	NavGroup.groups    one level per API group, customresources.go's customNavBody
//	NavGroup.note      the same function's three answers, two of which land here
//	NavGroup.deferred  customNav, set on the one heading that costs a cluster read
//	Ref.text, href     navLink.Label and navLink.Href
//
// `Ref.hint` is the one entry, and it is an exception about the *menu* rather
// than about `Ref`: `navLink` has no hint, so no menu entry has one to carry.
// `Ref` is the most reused shape in the view layer and carries hints elsewhere —
// the drawer's tabs and the relations panel — which is why the absence is
// recorded here rather than the member being removed from the contract.
//
// An entry belongs here only with the search that established the absence. A name
// added to get past a failure is this test switched off one member at a time.
var navMembersWithNoSource = map[string]string{
	"Ref.hint": "navLink carries no hint: `grep -n 'type navLink struct' -A 13 internal/web/server.go` " +
		"is Label, Href, Current, Namespaced, Aliases and nothing else",
}

// collectNavGroup records one group's members and recurses into its nested ones,
// because a member that only ever appears one level down is still carried.
func collectNavGroup(t *testing.T, value any, into, refs map[string]bool) {
	t.Helper()
	group, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("a menu group is not an object: %T", value)
	}
	for member := range group {
		into[member] = true
	}
	for _, item := range asArray(t, group["items"]) {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("a menu entry is not an object: %T", item)
		}
		for member := range entry {
			refs[member] = true
		}
	}
	for _, sub := range asArray(t, group["groups"]) {
		collectNavGroup(t, sub, into, refs)
	}
}

func asArray(t *testing.T, value any) []any {
	t.Helper()
	if value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("expected an array, got %T", value)
	}
	return items
}

// dataOf drives one GET and returns the `ok` arm's payload.
//
// It insists on `ok` with something in it, because a failure arm carries no data
// at all and every assertion above it would then pass over an empty map.
func dataOf(t *testing.T, mux *http.ServeMux, target string) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s answered %d: %s", target, recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("GET %s did not answer an envelope: %v", target, err)
	}
	if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
		t.Fatalf("GET %s answered state=%q with %d members; this test measures an answer that has "+
			"something in it", target, envelope.State, len(envelope.Data))
	}
	return envelope.Data
}
