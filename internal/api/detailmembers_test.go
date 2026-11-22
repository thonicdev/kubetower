package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The same test as `TestTheListPageCarriesEveryMemberTheContractDeclares`, on the
// object surface, and for the same reason: the surface carries many members and
// an optional member is satisfied by being absent, so a member the contract
// declares and no response carries goes unnoticed.
//
// `detail.Object` has sixteen members and the object's neighborhood
// (relations, certificates, volumes, owners) lives in them; none of it reaches a
// client unless the contract types carry it.
//
// Both directions, because the two failures are different:
//
//   - a member the contract declares and no response carries, and the next one
//     somebody adds to the YAML;
//   - a member a response carries and the contract does not declare.
//
// The expectation is read out of `api/openapi.yaml` **at run time**. A test that
// listed the names it had just read off `types.go` would agree with the struct by
// construction and fail on nothing.
//
// What it cannot see: whether `internal/web` fills any of this from the producers
// that hold it. That package imports this one, so a test here cannot reach it —
// `TestTheDetailPageIsFilledFromTheProducersThatHoldIt` is the other half, and
// neither is sufficient alone. This one drives the real handler over `fakeReads`,
// which proves the contract types can carry every declared member.
func TestTheDetailPageCarriesEveryMemberTheContractDeclares(t *testing.T) {
	mux := http.NewServeMux()
	New(Deps{
		Sessions: fakeSessions{}, Profile: fakeProfile{}, Clusters: fakeClusters{},
		Settings: fakeSettings{}, Prefs: fakePrefs{}, Reads: fakeReads{},
		Acts: fakeActs{}, Dock: fakeDock{}, Probe: fakeProbe{},
		AllowOrigin: func(*http.Request, string) bool { return true },
	}).Mount(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/c/prod/r/pods/default/api-1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/c/prod/r/pods/default/api-1 answered %d: %s", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		State string         `json:"state"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the answer is not an envelope: %v", err)
	}
	// An empty answer, a failed answer and an answer this test walked over
	// without looking at anything are three different things.
	if envelope.State != string(StateOK) || len(envelope.Data) == 0 {
		t.Fatalf("state=%q with a %d-member page; this test measures a page that has something in it",
			envelope.State, len(envelope.Data))
	}

	sections, ok := envelope.Data["sections"].([]any)
	if !ok || len(sections) == 0 {
		t.Fatalf("the page carries no sections; there is nothing to measure")
	}
	relations, ok := envelope.Data["relations"].([]any)
	if !ok || len(relations) == 0 {
		t.Fatalf("the page carries no relations; there is nothing to measure")
	}
	tabs, ok := envelope.Data["tabs"].([]any)
	if !ok || len(tabs) == 0 {
		t.Fatalf("the page carries no tabs; there is nothing to measure")
	}
	containers, ok := envelope.Data["containers"].([]any)
	if !ok || len(containers) == 0 {
		t.Fatalf("the page carries no containers; there is nothing to measure")
	}
	urls, ok := envelope.Data["urls"].([]any)
	if !ok || len(urls) == 0 {
		t.Fatalf("the page carries no addresses; there is nothing to measure")
	}
	subject, ok := envelope.Data["event"].(map[string]any)
	if !ok || len(subject) == 0 {
		t.Fatalf("the page carries no event subject; there is nothing to measure")
	}
	ports, ok := envelope.Data["ports"].([]any)
	if !ok || len(ports) == 0 {
		t.Fatalf("the page carries no ports; there is nothing to measure")
	}
	events, ok := envelope.Data["events"].([]any)
	if !ok || len(events) == 0 {
		t.Fatalf("the page carries no events; there is nothing to measure")
	}
	ruleSet, ok := envelope.Data["rules"].(map[string]any)
	if !ok || len(ruleSet) == 0 {
		t.Fatalf("the page carries no rule set; there is nothing to measure")
	}
	rules, ok := ruleSet["rules"].([]any)
	if !ok || len(rules) == 0 {
		t.Fatalf("the rule set carries no rule; there is nothing to measure")
	}
	// Both members on one object, because `empty` is the state that
	// carries both — and `empty` is the majority answer on the two kinds this
	// runs for, so it is also the one worth pinning.
	usedBy, ok := envelope.Data["usedBy"].(map[string]any)
	if !ok || len(usedBy) == 0 {
		t.Fatalf("the page carries no reverse-walk answer; there is nothing to measure")
	}
	certificates, ok := envelope.Data["certificates"].(map[string]any)
	if !ok || len(certificates) == 0 {
		t.Fatalf("the page carries no certificate set; there is nothing to measure")
	}
	bundles, ok := certificates["bundles"].([]any)
	if !ok || len(bundles) == 0 {
		t.Fatalf("the certificate set carries no bundles; there is nothing to measure")
	}
	// The union across the certificates of every bundle: a bundle that parsed
	// carries certificates and a bundle that did not carries a note, and one
	// bundle carrying both at once is one that cannot exist.
	blocks := []any{}
	for _, item := range bundles {
		bundle, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("a bundle is not an object: %T", item)
		}
		listed, ok := bundle["certificates"].([]any)
		if ok {
			blocks = append(blocks, listed...)
		}
	}
	if len(blocks) == 0 {
		t.Fatalf("no bundle carries a certificate; there is nothing to measure")
	}

	// The union across every environment row of every container, and across both
	// `env` and `imported` — the two are kept apart on purpose and carry the same
	// shape, so measuring one would leave the other unchecked.
	envRows := []any{}
	imports := []any{}
	amounts := []any{}
	sources := []any{}
	// The union across every probe of every container, and across every reported
	// status. Both are per-container members whose own schemas would otherwise go
	// unmeasured - and both exist to keep two absences apart, which is exactly the
	// sort of member that is quietly dropped by a mapper and noticed by nobody.
	probes := []any{}
	statuses := []any{}
	terminations := []any{}
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("a container is not an object: %T", item)
		}
		if listed, ok := container["probes"].([]any); ok {
			probes = append(probes, listed...)
		}
		if reported, ok := container["status"].(map[string]any); ok {
			statuses = append(statuses, reported)
			if last, ok := reported["lastTermination"].(map[string]any); ok {
				terminations = append(terminations, last)
			}
		}
		for _, key := range []string{"env", "imported"} {
			listed, ok := container[key].([]any)
			if !ok {
				continue
			}
			envRows = append(envRows, listed...)
		}
		for _, key := range []string{"requests", "limits"} {
			listed, ok := container[key].([]any)
			if !ok {
				continue
			}
			amounts = append(amounts, listed...)
		}
		listed, ok := container["envFrom"].([]any)
		if ok {
			imports = append(imports, listed...)
		}
	}
	for _, item := range envRows {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("an environment row is not an object: %T", item)
		}
		if source, ok := row["source"]; ok {
			sources = append(sources, source)
		}
	}
	if len(envRows) == 0 || len(imports) == 0 || len(amounts) == 0 || len(sources) == 0 {
		t.Fatalf("%d env rows, %d imports, %d amounts, %d sources; there is nothing to measure",
			len(envRows), len(imports), len(amounts), len(sources))
	}
	// Three probes per container, always - the entry is the sentence "no readiness
	// probe", so a container that declares none still produces three. A response
	// carrying fewer than one per container per kind has filtered the absent ones
	// out on the way, which is the defect the member exists to prevent.
	if want := len(containers) * 3; len(probes) != want {
		t.Fatalf("%d probes across %d containers, want %d; the absent ones are the finding and must not be filtered out",
			len(probes), len(containers), want)
	}
	if len(statuses) != len(containers) {
		t.Fatalf("%d statuses across %d containers; a container nobody reported carries `unreported`, never no member at all",
			len(statuses), len(containers))
	}

	// The union across every field of every section, never one of them: `prose`
	// belongs to a condition message, `pill` to a status, `copy` to a name, and
	// a single field carrying all of them at once is one that cannot exist.
	fields := map[string]bool{}
	for _, section := range sections {
		object, ok := section.(map[string]any)
		if !ok {
			t.Fatalf("a section is not an object: %T", section)
		}
		listed, ok := object["fields"].([]any)
		if !ok {
			continue
		}
		for member := range unionOf(t, listed) {
			fields[member] = true
		}
	}
	if len(fields) == 0 {
		t.Fatalf("no section carries a field; there is nothing to measure")
	}

	for _, subject := range []struct {
		schema  string
		carried map[string]bool
	}{
		{"DetailPage", membersOf(envelope.Data)},
		{"DetailSection", unionOf(t, sections)},
		{"Relation", unionOf(t, relations)},
		{"Tab", unionOf(t, tabs)},
		{"ObjectEvent", unionOf(t, events)},
		{"ObjectPort", unionOf(t, ports)},
		{"EventSubject", membersOf(subject)},
		{"PodContainer", unionOf(t, containers)},
		{"ContainerProbe", unionOf(t, probes)},
		{"ContainerStatus", unionOf(t, statuses)},
		{"ContainerTermination", unionOf(t, terminations)},
		{"EnvVar", unionOf(t, envRows)},
		{"EnvImport", unionOf(t, imports)},
		{"EnvSource", unionOf(t, sources)},
		{"ResourceAmount", unionOf(t, amounts)},
		{"ObjectURL", unionOf(t, urls)},
		{"RuleSet", membersOf(ruleSet)},
		{"UsedBy", membersOf(usedBy)},
		{"PolicyRule", unionOf(t, rules)},
		{"CertificateSet", membersOf(certificates)},
		{"CertificateBundle", unionOf(t, bundles)},
		{"Certificate", unionOf(t, blocks)},
	} {
		declared := schemaMembersInContract(t, subject.schema)
		for _, member := range sorted(declared) {
			if why, allowed := detailMembersWithNoSource[subject.schema+"."+member]; allowed {
				t.Logf("%s.%s is declared and deliberately not carried: %s", subject.schema, member, why)
				continue
			}
			if !subject.carried[member] {
				t.Errorf("the contract declares %s.%s and no %s in the response carries it; "+
					"either fill it from the producer that holds it, or record here why it has no source",
					subject.schema, member, subject.schema)
			}
		}
		for _, member := range sorted(subject.carried) {
			if !declared[member] {
				t.Errorf("the response carries %s.%s and the contract does not declare it",
					subject.schema, member)
			}
		}
	}

	// `DetailSection.fields[]` is an inline schema, so it has no name to look up.
	// Its members are read out of the contract at the path they live at, for the
	// same reason as everything above: a list restated here would agree with
	// itself.
	declared := inlineFieldMembersInContract(t)
	for _, member := range sorted(declared) {
		if why, allowed := detailMembersWithNoSource["DetailField."+member]; allowed {
			t.Logf("DetailField.%s is declared and deliberately not carried: %s", member, why)
			continue
		}
		if !fields[member] {
			t.Errorf("the contract declares DetailSection.fields[].%s and no field in the response carries it", member)
		}
	}
	for _, member := range sorted(fields) {
		if !declared[member] {
			t.Errorf("a field carries %s and the contract does not declare it", member)
		}
	}
}

// detailMembersWithNoSource is the deliberate exceptions, each with the evidence
// that there is nothing to fill it from. **It is empty**, and that is a claim
// about this test rather than about the product: what it measures is the
// *contract types* over `fakeReads`, and a fake can carry anything. The member
// that genuinely has no producer — `DetailPage.actions` — is
// excepted in `TestTheDetailPageIsFilledFromTheProducersThatHoldIt`, which is the
// half that can see it. Putting it here as well would switch off the only check
// that would notice the contract type losing the member altogether.
//
// An entry belongs here only with the search that established the absence. A name
// added to get past a failure is this test switched off one member at a time.
var detailMembersWithNoSource = map[string]string{
	// **Mutually exclusive with `bundles`, which this page has to carry for the
	// bundle members to be measurable at all.** The producer's own words:
	// `CertificateSet.Note` is "why there is no bundle at all — today, only the
	// Secret that claims to be kubernetes.io/tls and has no tls.crt", and it is
	// "empty when Bundles is filled". A fixture carrying both would be a state
	// the producer cannot produce, which is a worse thing to pin than an
	// unmeasured member.
	//
	// It is not unchecked: `TestACertificateSetWithNoBundleSaysWhy` in
	// internal/web drives the mapper with exactly that object and asserts the
	// note arrives.
	"CertificateSet.note": "exclusive with bundles; covered by TestACertificateSetWithNoBundleSaysWhy in internal/web",

	// **Mutually exclusive with `rules`, which this page has to carry for the
	// rule members to be measurable at all.** `RuleSet.reason` is filled on
	// exactly one arm — `unreadable`, where the rules array is present and is not
	// a list of rules — and that arm carries no rule by construction:
	// `ruleSetOf` returns the state and the reason and nothing else. A fixture
	// carrying both would be a payload the producer cannot produce, which is a
	// worse thing to pin than an unmeasured member.
	//
	// It is not unchecked. `TestARoleWhoseRulesCannotBeReadRefusesRatherThanSaying
	// ItGrantsNothing` in internal/web drives the mapper with exactly that object
	// and asserts the state, the code and the detail arrive.
	"RuleSet.reason":    "exclusive with rules; covered by TestARoleWhoseRulesCannotBeReadRefusesRatherThanSayingItGrantsNothing in internal/web",
	"RuleSet.selectors": "only on an aggregated ClusterRole, which this fixture is not; covered by TestAnAggregatedClusterRoleCarriesItsSelectors in internal/detail",

	// **Mutually exclusive with the kind this page has to be.** `DetailPage.data`
	// is the address of the keys inside the details panel.
	//
	// `detail.HasData` fills it on a ConfigMap and a Secret and on nothing else,
	// and this test drives one request, on a **pod** - because a pod is the only
	// kind whose page carries `containers`, `ports`, `probes`, `statuses` and the
	// env members, which are twelve of the schemas measured above. A ConfigMap
	// fixture would fill this member and take those twelve away, so the exclusion
	// is between the two fixtures rather than inside one payload. That is a
	// weaker form of the argument the two entries above make and it is stated as
	// the weaker one: the member has a producer, and this test cannot see it.
	//
	// It is not unchecked. `TestTheKeysAreAMemberAndOnlyWhereThereIsADataBlock`
	// in internal/web drives `detailPageOf` with a ConfigMap and a Secret and
	// asserts the address arrives, carries the object, and is absent on a pod and
	// a node.
	"DetailPage.data":     "no source on a pod, which this fixture is; covered by TestTheKeysAreAMemberAndOnlyWhereThereIsADataBlock in internal/web",
	"DetailPage.deleting": "false on a live pod, which this fixture is, and omitted when false; covered by TestAnObjectBeingDeletedTakesNoVerb in internal/web",

	// **Mutually exclusive with the kind this page has to be**, the same
	// argument as `DetailPage.data` above: `internal/detail` fills it on the events of what a
	// controller owns - a Deployment's ReplicaSets and pods, a CronJob's Jobs
	// and theirs - and a pod owns nothing, so every event on this fixture is
	// the pod's own and carries none (`ownedWords` in internal/detail/owned.go).
	//
	// It is not unchecked. `TestAnOwnedObjectsEventSaysWhichObjectItIsAbout` in
	// internal/web drives `eventsOf` with such an event and asserts the member
	// arrives, and `TestADeploymentReadsTheEventsOfItsReplicaSetsAndPods` in
	// internal/detail pins the producer.
	"ObjectEvent.about": "no source on a pod, which owns nothing; covered by TestAnOwnedObjectsEventSaysWhichObjectItIsAbout in internal/web",
}

// inlineFieldMembersInContract reads
// `components.schemas.DetailSection.properties.fields.items.properties`.
func inlineFieldMembersInContract(t *testing.T) map[string]bool {
	t.Helper()
	spec := loadSpec(t)
	at := func(node any, key string) any {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("the contract has no object at %q", key)
		}
		return object[key]
	}
	node := any(spec)
	for _, key := range []string{"components", "schemas", "DetailSection", "properties", "fields", "items", "properties"} {
		node = at(node, key)
	}
	properties, ok := node.(map[string]any)
	if !ok || len(properties) == 0 {
		t.Fatal("DetailSection.fields[] declares no properties")
	}
	out := make(map[string]bool, len(properties))
	for member := range properties {
		out[member] = true
	}
	return out
}
