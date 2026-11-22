package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `ManifestPanel.editableNote` is declared in `api/openapi.yaml` and must be
// carried by `api.ManifestPanel`, or nothing could ever send it, and
// nothing would notice, because an **optional member is satisfied by being absent**
// and `TestRealResponsesMatchTheSchemasInTheContract` therefore cannot see it.
// `DataPanel.editableNote` is in the same position for the same reason.
//
// Both directions, as in `listmembers_test.go`, because the two failures are
// different: a member the contract declares and nothing carries, and a member a
// response carries that the contract does not declare.
//
// The expectation is read out of the contract **at run time**. A test that
// listed the names it had just read off `types.go` would agree with the struct
// by construction and could fail on nothing.
//
// What it cannot check: this drives the real handlers over `fakeReads`, so it
// proves the contract types can carry every declared member. That
// `internal/web` actually fills them is the other half, and it is
// `TestTheManifestPanelSaysWhichSettingRefusedTheWrite` over there.
func TestTheEditPanelsCarryEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	// Two clusters, because `editable: true` and `editableNote` cannot appear on
	// one answer — the note is what says why editing is refused. Measuring a
	// single response would mean excepting one of them, which is this test
	// switched off on the member it was written for.
	panels := func(path string) (map[string]any, map[string]any) {
		t.Helper()
		read := func(cluster string) map[string]any {
			t.Helper()
			recorder := httptest.NewRecorder()
			url := "/api/c/" + cluster + path + "?kind=configmaps&namespace=default&name=app"
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET %s answered %d: %s", url, recorder.Code, recorder.Body.String())
			}
			var envelope struct {
				State string         `json:"state"`
				Data  map[string]any `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("%s did not answer an envelope: %v", url, err)
			}
			// An empty answer and a failed answer are different answers, and so
			// is one that never reached the handler: without this the loops
			// below would pass over nothing and report success.
			if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
				t.Fatalf("%s answered state=%q with %d members; this test measures a panel that has something in it",
					url, envelope.State, len(envelope.Data))
			}
			return envelope.Data
		}
		return read("prod"), read(guardedCluster)
	}

	manifest, guardedManifest := panels("/manifest")
	data, guardedData := panels("/data")

	lines, ok := manifest["lines"].([]any)
	if !ok || len(lines) == 0 {
		t.Fatal("the manifest panel carries no lines; there is nothing to measure")
	}
	tokens := map[string]bool{}
	for _, line := range lines {
		object, ok := line.(map[string]any)
		if !ok {
			t.Fatalf("a manifest line is not an object: %T", line)
		}
		carried, ok := object["tokens"].([]any)
		if !ok || len(carried) == 0 {
			t.Fatal("a manifest line carries no tokens")
		}
		for member := range unionOf(t, carried) {
			tokens[member] = true
		}
	}

	entries, ok := data["entries"].([]any)
	if !ok || len(entries) == 0 {
		t.Fatal("the data panel carries no entries; there is nothing to measure")
	}

	for _, subject := range []struct {
		schema  string
		carried map[string]bool
	}{
		{"ManifestPanel", both(membersOf(manifest), membersOf(guardedManifest))},
		{"ManifestLine", unionOf(t, lines)},
		{"ManifestToken", tokens},
		{"DataPanel", both(membersOf(data), membersOf(guardedData))},
		{"DataEntry", unionOf(t, entries)},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and no response carries it; "+
					"either fill it from the producer that holds it, or record here why it has no source",
					subject.schema, member)
			}
		}
		for _, member := range sorted(subject.carried) {
			if !declared[member] {
				t.Errorf("the response carries %s.%s and the contract does not declare it; "+
					"this is how `Cluster.group` survived a contract that had removed it",
					subject.schema, member)
			}
		}
	}
}

// A read-only cluster is offered no editor **and** is told which setting refused
// it. `internal/web` pins both halves in one test
// (`TestTheManifestPanelOffersEditingOnlyWhereItIsAllowed`), and they are one
// requirement rather than two: `editable: false` with no note leaves an operator
// looking at a disabled control with nothing to act on.
func TestAGuardedClusterIsToldWhichSettingRefusedTheWrite(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	for _, path := range []string{"/manifest", "/data"} {
		recorder := httptest.NewRecorder()
		url := "/api/c/" + guardedCluster + path + "?kind=configmaps&namespace=default&name=app"
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))

		var envelope struct {
			Data struct {
				Editable     *bool  `json:"editable"`
				EditableNote string `json:"editableNote"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s did not answer an envelope: %v", url, err)
		}

		// A pointer, deliberately. `editable` must be *stated* as false rather
		// than omitted, or "this cannot be edited" and "this server never said"
		// reach the client as the same absence — which is the collapse this
		// whole product exists to refuse, on the member that decides whether a
		// write control is drawn.
		if envelope.Data.Editable == nil {
			t.Fatalf("%s omitted `editable` entirely; absent is not the same answer as false", url)
		}
		if *envelope.Data.Editable {
			t.Fatalf("%s offered an editor on a guarded cluster", url)
		}
		if envelope.Data.EditableNote == "" {
			t.Fatalf("%s refused the write and did not say which setting refused it", url)
		}
	}
}

// both is the union of two responses' member sets.
func both(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for member := range a {
		out[member] = true
	}
	for member := range b {
		out[member] = true
	}
	return out
}

// The act half of the same question, on the one confirm-then-act pair a client
// exists for.
//
// `PlanFacts` is the structured truth a destructive action is agreed against, so
// a member the contract declares and the server never sends is a fact the
// user was promised and does not get. `fields`, `request` and `precondition`
// are computed by `internal/edit` while producing the patch, and are lost if the
// mapping drops them, which would leave the confirmation's facts with no way to
// reach a client at all.
func TestThePlanCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	body := strings.NewReader(`{"document":"kind: ConfigMap\n","resourceVersion":"310"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/c/prod/manifest/preview", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-KubeTower-Request", "1")
	request.Header.Set("Origin", "http://localhost")

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("the preview answered %d: %s", recorder.Code, recorder.Body.String())
	}

	var plan struct {
		Facts map[string]any `json:"facts"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &plan); err != nil {
		t.Fatalf("the preview did not answer a plan: %v", err)
	}
	if len(plan.Facts) == 0 {
		t.Fatal("the plan carries no facts; there is nothing to measure")
	}

	carried := membersOf(plan.Facts)
	declared := schemaMembersInContract(t, "PlanFacts")
	for _, member := range sorted(declared) {
		if why, allowed := planMembersWithNoSource[member]; allowed {
			t.Logf("PlanFacts.%s is declared and deliberately not carried: %s", member, why)
			continue
		}
		if !carried[member] {
			t.Errorf("the contract declares PlanFacts.%s and the plan does not carry it; "+
				"either fill it from the producer that holds it, or record here why it has no source",
				member)
		}
	}
	for _, member := range sorted(carried) {
		if !declared[member] {
			t.Errorf("the plan carries PlanFacts.%s and the contract does not declare it", member)
		}
	}
}

// planMembersWithNoSource is the deliberate exceptions, each with what was
// searched to establish the absence. **An entry belongs here only with that
// evidence.** A name added to get past a failure is this test switched off one
// member at a time.
var planMembersWithNoSource = map[string]string{
	"recap": "filled by the **verbs** and not by this plan: " +
		"`apireads.go`'s `recapOf` carries `actions.Recap` - field, before, after, " +
		"`absent` and `unchanged` - onto every `PreviewAction`. This test drives " +
		"`PreviewManifest`, and a manifest write is a whole document rather than one " +
		"field: its answer is `diff`, which the plan does carry. An entry here because " +
		"the member now has a producer and this particular plan is not it",
	"alternative": "filled by a **drain's eviction stage** and by nothing else: " +
		"`apireads.go`'s `evictionFacts` offers the same drain including the " +
		"pods nothing owns when the plan leaves some on the node. A manifest write has no other " +
		"plan to offer. `TestADrainWalksBothStagesOverTheCluster` in `internal/web` measures it",
	"forceable": "filled by a **single delete's** plan and by nothing else: " +
		"`apireads.go`'s `PreviewAction` says it on a delete, and the act " +
		"may then carry `force`. A manifest write is not a delete. " +
		"`TestOneDeleteIsASimpleForceablePlan` in `internal/web` measures it",
	"preconditionOn": "filled by the **verbs** and not by this plan: " +
		"`apireads.go`'s `PreviewAction` states `actions.PinOf`'s `On` beside its value. " +
		"Absent means `metadata.resourceVersion`, and that is what a manifest write is " +
		"pinned to, so this plan says it by leaving the member out. " +
		"`TestACordonPreviewedAgainstANodeThatThenMovedIsRefused` in `internal/web` " +
		"measures the member on a node's plan",
	"preconditionUid": "filled by the **verbs** and not by this plan: " +
		"`apireads.go`'s `PreviewAction` states `actions.PinOf`'s `UID`, because " +
		"a generation restarts at 1 on a recreate. A manifest write is pinned to the " +
		"resourceVersion, which a recreated object never shares, so it needs no UID beside it. " +
		"`TestACordonPreviewedAgainstANodeThatReRegisteredIsRefused` in `internal/web` " +
		"measures the member on a node's plan",
	"document": "the whole object, `where the change is an object rather than a field - " +
		"a create`. `internal/edit.Create` exists and no route reaches it yet, so there " +
		"is no answer this could be filled from. It becomes fillable with the create path " +
		"and not before",
	"note": "`PreviewManifest` puts the dry run's sentence on `Targets[0].Note` rather " +
		"than here, which is where `internal/edit.Preview.Validated` is carried today. " +
		"Two homes for one sentence is the defect; this records which one is used",
	"explanation": "the verb's own paragraph, from `internal/web`'s `actionSpec`, which is " +
		"keyed on (verb, kind). This test drives `PreviewManifest`, and an apply has no verb " +
		"spec at all - `specFor` answers false for it - so inventing a paragraph here would be " +
		"the client-composed sentence this contract refuses, one layer lower. " +
		"`TestTheVerbsOwnWordsReachThePlan` in `internal/web` measures the member, over every " +
		"(verb, kind) the spec covers",
	"warning": "same producer and same absence as `explanation`, and it is additionally " +
		"absent on most verbs by design: it is the sentence for a verb whose consequence is " +
		"not what its name suggests - restart-on-a-Pod, drain, trigger - and `specFor` leaves " +
		"it empty for the rest",
	"button": "same producer and same absence as `explanation`. A plan with no verb spec is " +
		"committed with the verb as its label, which is what the dialog falls back to",
	"headline": "the one line a single verb's plan leads with, composed by `internal/web`'s " +
		"`lineFor` from what the verb's preview read off the object - its owner, what holds " +
		"its manifest, a CronJob's runs. A manifest apply has no verb " +
		"spec, so its sentence stays the one `sentenceFor` composes from the verb and the " +
		"target. `TestAPodsLineSaysWhatMakesAnotherOrThatNothingWill` in `internal/web` " +
		"measures the member",
}
