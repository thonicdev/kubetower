package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Helm payloads: every member the contract declares for a release.
//
// # An absent optional member conforms
//
// `HelmRelease` declares fourteen members, `HelmReleaseDetail` seven, and there
// is a `HelmRevision` type. Every one of them is optional on the wire, so a
// response carrying fewer members than the schema declares validates against it
// and goes on validating. Four of the list's nine columns and the whole of a
// release's own page would have nothing to draw, and a component written against
// the contract's types would compile and render blanks. That is the same class
// of defect as the dashboard's and the health page's, and it is caught the same
// way: both directions, read out of `api/openapi.yaml` at run time.
//
// The members at stake, named rather than counted:
//
//	chartVersion      what an upgrade is read off
//	deprecated        the chart is marked deprecated upstream
//	description       Helm's own sentence about the last operation, which is
//	                  **where a failed upgrade says what went wrong**
//	deployedRevision  the revision actually running
//	note              the line that says so, drawn under the status it contradicts
//	revisions         the release's history - the half a user opens it for
//	historyMax        how far back it is possible to look
//
// # Proving it fails
//
// Removing `ChartVersion` from `helmReleaseOf`'s copy and from the fixture:
//
//	helmmembers_test.go: the contract declares HelmRelease.chartVersion and no
//	release in the response carries it
//
// Removing `Revisions` from `fakeReads.HelmRelease`:
//
//	helmmembers_test.go: the release page carries no revisions; there is nothing
//	to measure
//
// # What this test cannot see
//
// It drives the real handlers over `fakeReads`, so what it proves is that the
// contract types *can* carry every declared member. It says nothing about
// whether any producer fills one: a member assigned only in the fixture would
// leave a panel permanently and silently empty, which is a test asserting its
// own fixture.
//
// The other half is `TestTheHelmReleasePageIsFilledFromTheProducersThatHoldIt`
// in **internal/web**, which cannot live here - internal/web imports internal/api
// and the dependency only runs one way. It drives a real `*helm.Reader` over a
// fake API server holding release Secrets in Helm's own encoding, so nothing in
// it can be satisfied by anything in this package.
func TestTheHelmPayloadsCarryEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	// --- the list ------------------------------------------------------------

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/c/prod/helm", nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("GET /api/c/prod/helm answered %d: %s", listed.Code, listed.Body.String())
	}
	var list struct {
		State string `json:"state"`
		Items []any  `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatalf("the list is not an envelope: %v", err)
	}
	if list.State != string(StateOK) || len(list.Items) == 0 {
		t.Fatalf("state=%q with %d releases; there is nothing to measure", list.State, len(list.Items))
	}

	// --- one release's page --------------------------------------------------

	page := func(release string) map[string]any {
		t.Helper()
		opened := httptest.NewRecorder()
		url := "/api/c/prod/helm/default/" + release
		mux.ServeHTTP(opened, httptest.NewRequest(http.MethodGet, url, nil))
		if opened.Code != http.StatusOK {
			t.Fatalf("GET %s answered %d: %s", url, opened.Code, opened.Body.String())
		}
		var envelope struct {
			State string         `json:"state"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(opened.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s is not an envelope: %v", url, err)
		}
		if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
			t.Fatalf("%s answered state=%q with a %d-member page; there is nothing to measure",
				url, envelope.State, len(envelope.Data))
		}
		return envelope.Data
	}

	// Two pages, for `editmembers_test.go`'s reason. `payloadNote` says that
	// `notes` and `values` could **not be read**, so it cannot appear beside them
	// on one answer: measuring a single response would mean excepting one of the
	// three, which is this test switched off on a member it was written for.
	var detail struct{ Data map[string]any }
	detail.Data = page("ingress")
	unread := page(undecodableRelease)

	revisions, ok := detail.Data["revisions"].([]any)
	if !ok || len(revisions) == 0 {
		t.Fatal("the release page carries no revisions; there is nothing to measure — and " +
			"a release showing its values and its manifest and no history is the list row " +
			"again with more text")
	}

	// The release schema is measured over **both** surfaces, unioned: the list's
	// items and the page's fold are the same shape, and neither carries every
	// member on its own — an undecodable release has a reason and no chart.
	releases := unionOf(t, list.Items)
	if fold, ok := detail.Data["release"].(map[string]any); ok {
		for member := range fold {
			releases[member] = true
		}
	}

	// --- the values, behind their own door ------------------------------------

	revealed := httptest.NewRecorder()
	mux.ServeHTTP(revealed, httptest.NewRequest(http.MethodGet, "/api/c/prod/helm/default/ingress/values?revision=41", nil))
	if revealed.Code != http.StatusOK {
		t.Fatalf("GET .../values answered %d: %s", revealed.Code, revealed.Body.String())
	}
	if got := revealed.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("the values answer says Cache-Control %q, want no-store", got)
	}
	var values struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(revealed.Body.Bytes(), &values); err != nil || values.State != string(StateOK) {
		t.Fatalf("the values answer is not an ok envelope (%v): %s", err, revealed.Body.String())
	}

	// --- the notes, behind the same door --------------------------------------

	noted := httptest.NewRecorder()
	mux.ServeHTTP(noted, httptest.NewRequest(http.MethodGet, "/api/c/prod/helm/default/ingress/notes?revision=41", nil))
	if noted.Code != http.StatusOK {
		t.Fatalf("GET .../notes answered %d: %s", noted.Code, noted.Body.String())
	}
	if got := noted.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("the notes answer says Cache-Control %q, want no-store", got)
	}
	var notes struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(noted.Body.Bytes(), &notes); err != nil || notes.State != string(StateOK) {
		t.Fatalf("the notes answer is not an ok envelope (%v): %s", err, noted.Body.String())
	}

	for _, subject := range []struct {
		schema  string
		carried map[string]bool
	}{
		{"HelmRelease", releases},
		{"HelmReleaseDetail", both(membersOf(detail.Data), membersOf(unread))},
		{"HelmRevision", unionOf(t, revisions)},
		{"HelmValues", membersOf(values.Data)},
		{"HelmNotes", membersOf(notes.Data)},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if why, allowed := helmMembersWithNoSource[subject.schema+"."+member]; allowed {
				t.Logf("%s.%s is declared and deliberately not carried: %s", subject.schema, member, why)
				continue
			}
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and nothing in the response carries it; "+
					"either fill it from the producer that holds it, or record here why it has no source",
					subject.schema, member)
			}
		}
		for _, member := range sorted(subject.carried) {
			if !declared[member] {
				t.Errorf("the response carries %s.%s and the contract does not declare it",
					subject.schema, member)
			}
		}
	}
}

// helmMembersWithNoSource is the deliberate exceptions, each with the evidence.
//
// One, and it is a **decision in the producer** rather than an omission — which
// is exactly the distinction an entry here is supposed to record:
//
//	HelmReleaseDetail.manifest
//	    `internal/helm` deliberately does not decode it.
//
//	    $ grep -rn 'Manifest' internal/helm/*.go
//	    internal/helm/decode.go:63:// struct that grew a `Manifest string` would
//	                                quietly turn a list of forty
//
//	    That one hit is the argument: `storedRelease` is narrowed on purpose, and
//	    the narrowing is what makes the list affordable. The measured document is
//	    5 MB of which `manifest` is 1.9 MB, and fields absent from the struct are
//	    skipped by encoding/json without being allocated - so a `Manifest string`
//	    would turn a list of forty releases into 200 MB of retained text.
//
//	    Filling this member therefore means reading it where it is drawn rather
//	    than widening the list decode, and that is a change to internal/helm.
//
//	    `notes` and `values` are not in this table: `decodeCurrent`
//	    reads them out of the document `decodeRelease` had already decompressed,
//	    for the current revision alone - no second request, no second
//	    decompression, and nothing added to the list's decode. Manifest is
//	    different in the one way that matters: at 1.9 MB it is a *size* problem on
//	    the wire and in the browser rather than a decoding one, and no narrowing
//	    of where it is read makes it smaller.
//
// An entry belongs here only with the search that established the absence. A name
// added to get past a failure is this test switched off one member at a time.
var helmMembersWithNoSource = map[string]string{
	"HelmReleaseDetail.manifest": "internal/helm narrows storedRelease on purpose - " +
		"manifest is 1.9 MB of a 5 MB document and decoding it into a list of forty " +
		"releases is 200 MB of retained text (decode.go:56-65)",
}

// **`?namespace=` is repeatable, and the handler must read every value.**
//
// The contract makes the parameter repeatable — *"?namespace=kube-system&namespace=ingress
// narrows to both"* — because the control it feeds is a multiple selection, and
// narrowing to the four namespaces a thirty-namespace cluster is actually read
// through is how that page is used. A handler calling `Query().Get("namespace")`
// would return the **first** value, so a two-namespace selection would silently
// become a one-namespace answer: a list that is wrong without saying so.
//
// Proving it fails: reading `Get("namespace")` instead prints
//
//	helmmembers_test.go: the handler passed [kube-system] for
//	?namespace=kube-system&namespace=ingress
func TestTheHelmListHonoursEveryNamespaceItWasGiven(t *testing.T) {
	seen := &namespaceRecorder{}
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: seen,
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/c/prod/helm?namespace=kube-system&namespace=ingress", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", recorder.Code, recorder.Body.String())
	}

	if len(seen.namespaces) != 2 ||
		seen.namespaces[0] != "kube-system" || seen.namespaces[1] != "ingress" {
		t.Errorf("the handler passed %v for ?namespace=kube-system&namespace=ingress; "+
			"a selection that arrives truncated is a list that is wrong without saying so",
			seen.namespaces)
	}

	// And no parameter at all is every namespace the caller may read, which is
	// **not** `default` — a client that substitutes one for the other shows a
	// fraction of a cluster with no sign that it did.
	seen.namespaces = nil
	seen.called = false
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/c/prod/helm", nil))
	if !seen.called {
		t.Fatal("the reader was not called at all")
	}
	if len(seen.namespaces) != 0 {
		t.Errorf("an unnarrowed request passed %v", seen.namespaces)
	}
}

// namespaceRecorder is fakeReads with the one method under test replaced, so
// everything else on the interface keeps working without being restated.
type namespaceRecorder struct {
	fakeReads
	called     bool
	namespaces []string
}

func (n *namespaceRecorder) HelmReleases(_ context.Context, _ string, namespaces []string) (HelmReleaseReading, *Failure) {
	n.called = true
	n.namespaces = namespaces
	return HelmReleaseReading{Releases: []HelmRelease{{Name: "ingress", Namespace: "default", State: StateOK}}}, nil
}
